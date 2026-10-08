package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"workjournal/internal/activity"
	"workjournal/internal/capture"
	"workjournal/internal/config"
	"workjournal/internal/diarize"
	"workjournal/internal/media"
	"workjournal/internal/process"
	"workjournal/internal/scheduler"
	"workjournal/internal/store"
)

type fakeControl struct {
	mu      sync.Mutex
	sent    []string
	sendErr error
}

func (f *fakeControl) Pause() error  { return f.Send("pause") }
func (f *fakeControl) Resume() error { return f.Send("resume") }
func (f *fakeControl) Rotate() error { return f.Send("rotate") }
func (f *fakeControl) Status() (capture.Status, error) {
	return capture.Status{}, errors.New("no recorder")
}
func (f *fakeControl) Send(c string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, c)
	return f.sendErr
}

func (f *fakeControl) sentCmds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

var _ scheduler.Jobs = (*Jobs)(nil)

func newJobs(t *testing.T) (*Jobs, *fakeControl, *float64) {
	t.Helper()
	p := config.Paths{Root: t.TempDir()}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Default()
	cfg.DataDir = p.Root
	cfg.MinFreeGB = 10
	ctl := &fakeControl{}
	free := new(float64)
	*free = 50
	j := &Jobs{
		Cfg:     func() config.Config { return cfg },
		Paths:   p,
		Store:   st,
		Control: ctl,
		FreeGB:  func(string) (float64, error) { return *free, nil },
		Now:     func() time.Time { return time.Date(2026, 10, 7, 12, 0, 0, 0, time.Local) },
		Logf:    t.Logf,
	}
	return j, ctl, free
}

func diskState(t *testing.T, j *Jobs) string {
	t.Helper()
	v, _, err := j.Store.GetKV("disk_state")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestDiskCheckSendsOnlyOnChange(t *testing.T) {
	j, ctl, free := newJobs(t)
	ctx := context.Background()

	// Plenty of space and no stored state: recorder already starts normal.
	if err := j.DiskCheck(ctx); err != nil {
		t.Fatal(err)
	}
	if len(ctl.sent) != 0 {
		t.Fatalf("sent %v with plenty of space", ctl.sent)
	}

	*free = 6 // below 10, above 5
	j.DiskCheck(ctx)
	j.DiskCheck(ctx)
	if want := []string{"screens_off"}; !equal(ctl.sent, want) {
		t.Fatalf("sent %v, want %v", ctl.sent, want)
	}
	if s := diskState(t, j); s != "screens_off" {
		t.Fatalf("disk_state = %q", s)
	}

	*free = 2
	j.DiskCheck(ctx)
	*free = 20
	j.DiskCheck(ctx)
	j.DiskCheck(ctx)
	if want := []string{"screens_off", "audio_off", "normal"}; !equal(ctl.sent, want) {
		t.Fatalf("sent %v, want %v", ctl.sent, want)
	}
	if s := diskState(t, j); s != "normal" {
		t.Fatalf("disk_state = %q", s)
	}
}

func TestDiskCheckControlErrorLeavesKVForRetry(t *testing.T) {
	j, ctl, free := newJobs(t)
	ctl.sendErr = errors.New("dial recorder: no such file")
	*free = 6
	if err := j.DiskCheck(context.Background()); err == nil {
		t.Fatal("send error not reported")
	}
	if s := diskState(t, j); s != "" {
		t.Fatalf("disk_state = %q after a failed send", s)
	}
	ctl.sendErr = nil
	if err := j.DiskCheck(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"screens_off", "screens_off"}; !equal(ctl.sentCmds(), want) {
		t.Fatalf("sent %v, want retry %v", ctl.sentCmds(), want)
	}
	if s := diskState(t, j); s != "screens_off" {
		t.Fatalf("disk_state = %q", s)
	}
}

func TestDiskCheckFreeGBError(t *testing.T) {
	j, ctl, _ := newJobs(t)
	j.FreeGB = func(string) (float64, error) { return 0, errors.New("statfs failed") }
	if err := j.DiskCheck(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if len(ctl.sent) != 0 || diskState(t, j) != "" {
		t.Fatalf("acted on a failed measurement: sent %v", ctl.sent)
	}
}

func TestEnrollSpeakerDiarizationOff(t *testing.T) {
	j, _, _ := newJobs(t)
	cfg := j.Cfg()
	cfg.Diarization = "off"
	j.Cfg = func() config.Config { return cfg }
	err := j.EnrollSpeaker(context.Background(), "m1", "Speaker 1", "Ana")
	if err == nil || err.Error() != "diarization unavailable" {
		t.Fatalf("err = %v", err)
	}
}

type oneTurnDiarizer struct{}

func (oneTurnDiarizer) Diarize(context.Context, string) ([]diarize.Turn, map[int][]float32, error) {
	return []diarize.Turn{{Start: 0, End: 10, Speaker: 0}}, map[int][]float32{0: {1, 0}}, nil
}

// enrollJobs gives j a sherpa-mode pipeline with diarizer d, a 10:00–10:30
// meeting and hour 10's doc where "Speaker 1" talks at 10:00:02.
func enrollJobs(t *testing.T, d diarize.Diarizer) (*Jobs, string) {
	t.Helper()
	j, _, _ := newJobs(t)
	cfg := j.Cfg()
	cfg.Diarization = "sherpa"
	j.Cfg = func() config.Config { return cfg }
	j.Pipeline = &process.Pipeline{
		Cfg: cfg, Paths: j.Paths, Store: j.Store, Diarizer: d,
		Voices: &diarize.Voices{Dir: j.Paths.Voices(), Threshold: cfg.VoiceMatchThreshold},
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			return nil, os.WriteFile(args[len(args)-1], []byte("RIFF"), 0o644)
		},
		Logf: t.Logf,
	}
	at := func(m, s int) time.Time { return time.Date(2026, 10, 1, 10, m, s, 0, time.Local) }
	doc := &process.HourDoc{Day: "2026-10-01", Hour: 10, Transcript: []media.Utterance{
		{Start: at(0, 2), End: at(0, 6), Track: "system", Speaker: "Speaker 1", Text: "hello"},
	}}
	if err := doc.Save(j.Paths.DerivedHour("2026-10-01", 10)); err != nil {
		t.Fatal(err)
	}
	m := store.MeetingRecord{ID: "2026-10-01-1000-zoom-us", Day: "2026-10-01", Start: at(0, 0), End: at(30, 0), App: "zoom.us"}
	if err := j.Store.UpsertMeeting(m); err != nil {
		t.Fatal(err)
	}
	mkfile(t, filepath.Join(j.Paths.Audio("2026-10-01"), "1000-system.m4a"), "audio")
	return j, m.ID
}

func TestEnrollSpeakerEnrolsAndRelabels(t *testing.T) {
	j, id := enrollJobs(t, oneTurnDiarizer{})
	if err := j.EnrollSpeaker(context.Background(), id, "Speaker 1", "Ana"); err != nil {
		t.Fatal(err)
	}
	if names, _ := j.Pipeline.Voices.List(); !equal(names, []string{"Ana"}) {
		t.Fatalf("voices = %v", names)
	}
	doc, err := process.Load(j.Paths.DerivedHour("2026-10-01", 10))
	if err != nil || doc.Transcript[0].Speaker != "Ana" {
		t.Fatalf("doc = %+v, %v", doc, err)
	}
}

func TestEnrollSpeakerMediaExpired(t *testing.T) {
	j, id := enrollJobs(t, oneTurnDiarizer{})
	if err := os.RemoveAll(j.Paths.RawDay("2026-10-01")); err != nil {
		t.Fatal(err)
	}
	if err := j.EnrollSpeaker(context.Background(), id, "Speaker 1", "Ana"); !errors.Is(err, scheduler.ErrMediaExpired) {
		t.Fatalf("err = %v", err)
	}
}

func TestEnrollSpeakerWithoutModelsIsUnavailable(t *testing.T) {
	j, id := enrollJobs(t, diarize.NoopDiarizer{}) // Build's fallback when models are missing
	if err := j.EnrollSpeaker(context.Background(), id, "Speaker 1", "Ana"); !errors.Is(err, errDiarizationUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestEnrollSpeakerUnknownMeeting(t *testing.T) {
	j, _ := enrollJobs(t, oneTurnDiarizer{})
	if err := j.EnrollSpeaker(context.Background(), "2026-10-01-0900-x", "Speaker 1", "Ana"); err == nil {
		t.Fatal("enrolled from an unknown meeting")
	}
}

func TestReprocessHourMediaExpired(t *testing.T) {
	j, _, _ := newJobs(t)
	err := j.ReprocessHour(context.Background(), "2026-09-01", 9)
	if !errors.Is(err, scheduler.ErrMediaExpired) {
		t.Fatalf("err = %v", err)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// fakeSkills answers summarize-hour and records which skills ran.
type fakeSkills struct{ called []string }

func (f *fakeSkills) Run(_ context.Context, skill, _ string, out any) error {
	f.called = append(f.called, skill)
	if skill != "summarize-hour" {
		return fmt.Errorf("fake: no output for %s", skill)
	}
	return json.Unmarshal([]byte(`{"notes":["Drew the checkout"],"main_focus":"Checkout","action_items":[]}`), out)
}

type noTranscriber struct{}

func (noTranscriber) Transcribe(context.Context, string, time.Time, string) ([]media.Utterance, error) {
	return nil, errors.New("no audio expected")
}

type noOCR struct{}

func (noOCR) OCR(context.Context, []string) ([]media.ScreenText, error) { return nil, nil }

func TestJobsUseLiveConfig(t *testing.T) {
	j, _, _ := newJobs(t)
	live := j.Cfg() // Build-time config: no rules, default categories
	j.Cfg = func() config.Config { return live }
	fk := &fakeSkills{}
	j.Runner = fk
	j.Summarizer = &process.SkillSummarizer{Runner: fk, Store: j.Store, Paths: j.Paths, Cfg: live, Logf: t.Logf}
	j.Pipeline = &process.Pipeline{
		Cfg: live, Paths: j.Paths, Store: j.Store,
		Transcriber: noTranscriber{}, OCR: noOCR{},
		Run:       func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("no media expected") },
		Now:       func() time.Time { return time.Date(2026, 10, 1, 15, 0, 0, 0, time.Local) },
		Summarize: j.Summarizer,
		Logf:      t.Logf,
	}

	// One active Figma stretch in 10:00-11:00 on 2026-10-01.
	act := j.Paths.Activity("2026-10-01")
	ts := func(m int) string { return time.Date(2026, 10, 1, 10, m, 0, 0, time.Local).Format(time.RFC3339) }
	lines := `{"ts":"` + ts(0) + `","type":"start"}` + "\n" +
		`{"ts":"` + ts(0) + `","type":"app","app":"Figma","title":"Checkout"}` + "\n" +
		`{"ts":"` + ts(40) + `","type":"stop"}` + "\n"
	if err := os.MkdirAll(filepath.Dir(act), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(act, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)
	os.Chtimes(act, old, old)

	// Settings saved after Build: a new category and a rule for Figma.
	next := live
	next.Categories = append(append([]string(nil), live.Categories...), "Design")
	next.Rules = map[string]string{"Figma": "Design"}
	live = next

	if err := j.ProcessPending(context.Background()); err != nil {
		t.Fatalf("process: %v (skills called %v)", err, fk.called)
	}
	if !equal(fk.called, []string{"summarize-hour"}) {
		t.Fatalf("skills called = %v; the new Figma rule should have made categorize-time unnecessary", fk.called)
	}
	segs, err := j.Store.SegmentsOn("2026-10-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) == 0 || segs[0].Category != "Design" || segs[0].Source != "rule" {
		t.Fatalf("segments = %+v", segs)
	}
	// The shared components keep their Build-time config.
	if j.Summarizer.Cfg.Rules["Figma"] != "" || j.Pipeline.Cfg.Rules["Figma"] != "" {
		t.Fatal("live config leaked into the shared summarizer or pipeline")
	}
}

func TestJobsPipelineRebuildsWhisperModel(t *testing.T) {
	j, _, _ := newJobs(t)
	cfg := j.Cfg()
	cfg.WhisperModel = "small.en"
	j.Cfg = func() config.Config { return cfg }
	j.Pipeline = &process.Pipeline{Transcriber: media.Whisper{Bin: "whisper-cli", Model: "/old/ggml-base.en.bin"}}
	p := j.pipeline()
	w, ok := p.Transcriber.(media.Whisper)
	if !ok || w.Model != filepath.Join(j.Paths.Models(), "ggml-small.en.bin") || w.Bin != "whisper-cli" {
		t.Fatalf("transcriber = %+v", p.Transcriber)
	}
	if j.Pipeline.Transcriber.(media.Whisper).Model != "/old/ggml-base.en.bin" {
		t.Fatal("shared pipeline mutated")
	}
}

// closeSkills answers summarize-hour, close-day and weekly-rollup, keeping the
// close-day input it was given.
type closeSkills struct {
	mu          sync.Mutex
	called      []string
	closeInput  string
	inFlight    int
	maxInFlight int
	delay       time.Duration
}

func (f *closeSkills) Run(_ context.Context, skill, input string, out any) error {
	f.mu.Lock()
	f.called = append(f.called, skill)
	f.inFlight++
	f.maxInFlight = max(f.maxInFlight, f.inFlight)
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.inFlight--; f.mu.Unlock() }()
	time.Sleep(f.delay)
	switch skill {
	case "summarize-hour":
		return json.Unmarshal([]byte(`{"notes":["Drew the checkout"],"main_focus":"Checkout","action_items":[]}`), out)
	case "close-day":
		b, err := os.ReadFile(input)
		if err != nil {
			return err
		}
		f.mu.Lock()
		f.closeInput = string(b)
		f.mu.Unlock()
		return json.Unmarshal([]byte(`{"markdown":"# Day"}`), out)
	case "weekly-rollup":
		return json.Unmarshal([]byte(`{"markdown":"# Week"}`), out)
	}
	return fmt.Errorf("fake: no output for %s", skill)
}

// withActivity gives j a pipeline over one Figma stretch on day in hour h.
func withActivity(t *testing.T, j *Jobs, fk *closeSkills, day string, h int, now time.Time) {
	t.Helper()
	cfg := j.Cfg()
	j.Runner = fk
	j.Summarizer = &process.SkillSummarizer{Runner: fk, Store: j.Store, Paths: j.Paths, Cfg: cfg, Logf: t.Logf}
	j.Pipeline = &process.Pipeline{
		Cfg: cfg, Paths: j.Paths, Store: j.Store,
		Transcriber: noTranscriber{}, OCR: noOCR{},
		Run:       func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("no media expected") },
		Now:       func() time.Time { return now },
		Summarize: j.Summarizer,
		Logf:      t.Logf,
	}
	j.Now = func() time.Time { return now }
	d, _ := time.ParseInLocation("2006-01-02", day, time.Local)
	ts := func(m int) string {
		return time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, time.Local).Format(time.RFC3339)
	}
	cfg.Rules = map[string]string{"Figma": "Deep work"}
	j.Cfg = func() config.Config { return cfg }
	act := j.Paths.Activity(day)
	lines := `{"ts":"` + ts(0) + `","type":"start"}` + "\n" +
		`{"ts":"` + ts(0) + `","type":"app","app":"Figma","title":"Checkout"}` + "\n" +
		`{"ts":"` + ts(40) + `","type":"stop"}` + "\n"
	if err := os.MkdirAll(filepath.Dir(act), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(act, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-10 * time.Minute)
	os.Chtimes(act, old, old)
}

// I1/I2: at 18:02 CloseDay processes hour 17 first (it has settled) and
// records the attempt in the scheduler's hourly marker.
func TestCloseDayIncludesSettledPreviousHour(t *testing.T) {
	j, _, _ := newJobs(t)
	fk := &closeSkills{}
	withActivity(t, j, fk, "2026-10-07", 17, time.Date(2026, 10, 7, 18, 2, 0, 0, time.Local))
	if err := j.CloseDay(context.Background(), "2026-10-07"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fk.closeInput, `"hour":17`) {
		t.Fatalf("close-day input lacks hour 17: %s", fk.closeInput)
	}
	if v, _, _ := j.Store.GetKV("last_process_hour"); v != "2026-10-07T18" {
		t.Fatalf("last_process_hour = %q", v)
	}
}

// I2: CloseDay's internal ProcessPending goes through the hourly guard.
func TestCloseDaySkipsProcessingAlreadyAttemptedThisHour(t *testing.T) {
	j, _, _ := newJobs(t)
	fk := &closeSkills{}
	withActivity(t, j, fk, "2026-10-07", 17, time.Date(2026, 10, 7, 18, 30, 0, 0, time.Local))
	if err := j.Store.SetKV("last_process_hour", "2026-10-07T18"); err != nil {
		t.Fatal(err)
	}
	if err := j.CloseDay(context.Background(), "2026-10-07"); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(fk.called, "summarize-hour") {
		t.Fatalf("processed again in an attempted hour: %v", fk.called)
	}
}

// M16: pipeline and journal jobs never run concurrently.
func TestJobsSerializeHeavyJobs(t *testing.T) {
	j, _, _ := newJobs(t)
	fk := &closeSkills{delay: 30 * time.Millisecond}
	withActivity(t, j, fk, "2026-10-07", 10, time.Date(2026, 10, 7, 18, 2, 0, 0, time.Local))
	for _, d := range []string{"2026-10-05", "2026-10-06"} {
		if err := os.WriteFile(j.Paths.DailyJournal(d), []byte("# "+d), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Go(func() { j.Rollup(ctx, "2026-W41") })
	wg.Go(func() { j.Rollup(ctx, "2026-W41") })
	wg.Go(func() { j.ProcessPending(ctx) })
	wg.Go(func() { j.CloseDay(ctx, "2026-10-07") })
	wg.Wait()
	if fk.maxInFlight != 1 {
		t.Fatalf("max concurrent skill runs = %d (calls %v)", fk.maxInFlight, fk.called)
	}
}

// I3: after retention removed the raw media, the derived doc is enough to
// summarize the hour again.
func TestReprocessHourFromDerivedWhenMediaExpired(t *testing.T) {
	j, _, _ := newJobs(t)
	fk := &closeSkills{}
	withActivity(t, j, fk, "2026-10-07", 9, time.Date(2026, 10, 7, 12, 0, 0, 0, time.Local))
	if err := os.RemoveAll(j.Paths.RawDay("2026-09-01")); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 1, 9, 0, 0, 0, time.Local)
	doc := &process.HourDoc{Day: "2026-09-01", Hour: 9, Status: store.StatusNeedsReview,
		Segments: []activity.Segment{{Start: start, End: start.Add(30 * time.Minute), Kind: activity.KindActive, App: "Figma", Title: "Checkout"}}}
	if err := doc.Save(j.Paths.DerivedHour("2026-09-01", 9)); err != nil {
		t.Fatal(err)
	}
	if err := j.Store.UpsertHour(store.HourRecord{Day: "2026-09-01", Hour: 9, Status: store.StatusNeedsReview}); err != nil {
		t.Fatal(err)
	}
	if err := j.ReprocessHour(context.Background(), "2026-09-01", 9); err != nil {
		t.Fatalf("reprocess from derived: %v", err)
	}
	if !slices.Contains(fk.called, "summarize-hour") {
		t.Fatalf("skills called = %v", fk.called)
	}
	rec, _, _ := j.Store.Hour("2026-09-01", 9)
	if rec.Status != store.StatusDone || !strings.Contains(rec.SummaryJSON, "Drew the checkout") {
		t.Fatalf("hour = %+v", rec)
	}
}

// A recorder that stays down must not produce a log line every minute: the
// send is retried each tick but its error is reported only when it changes.
func TestDiskCheckReportsSendErrorOnce(t *testing.T) {
	j, ctl, free := newJobs(t)
	var logs []string
	j.Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	ctl.sendErr = errors.New("dial recorder: no such file")
	*free = 6
	ctx := context.Background()
	if err := j.DiskCheck(ctx); err == nil {
		t.Fatal("first failure not reported")
	}
	for range 3 {
		if err := j.DiskCheck(ctx); err != nil {
			t.Fatalf("repeated failure reported again: %v", err)
		}
	}
	if n := len(ctl.sentCmds()); n != 4 {
		t.Fatalf("sends = %d, want a retry every tick", n)
	}
	if len(logs) > 1 {
		t.Fatalf("logged every tick: %q", logs)
	}
	ctl.sendErr = errors.New("recorder \"screens_off\": busy")
	if err := j.DiskCheck(ctx); err == nil {
		t.Fatal("a different error was not reported")
	}
	ctl.sendErr = nil
	if err := j.DiskCheck(ctx); err != nil {
		t.Fatal(err)
	}
	*free = 50
	ctl.sendErr = errors.New("dial recorder: no such file")
	if err := j.DiskCheck(ctx); err == nil {
		t.Fatal("failure after a success not reported")
	}
}

func TestProcessNowRotatesThenProcessesUpToNow(t *testing.T) {
	for _, down := range []bool{false, true} {
		j, ctl, _ := newJobs(t)
		if down {
			ctl.sendErr = errors.New("dial recorder: no such file")
		}
		fk := &closeSkills{}
		now := time.Date(2026, 10, 7, 10, 20, 0, 0, time.Local)
		withActivity(t, j, fk, "2026-10-07", 10, now)
		upto, err := j.ProcessNow(context.Background())
		if err != nil {
			t.Fatalf("down=%v: %v", down, err)
		}
		if !upto.Equal(now) {
			t.Errorf("processed up to %v, want %v", upto, now)
		}
		if got := ctl.sentCmds(); !equal(got, []string{"rotate"}) {
			t.Errorf("down=%v: sent %v, want a rotate first", down, got)
		}
		if !equal(fk.called, []string{"summarize-hour"}) {
			t.Errorf("down=%v: skills %v", down, fk.called)
		}
		h, ok, err := j.Store.Hour("2026-10-07", 10)
		if err != nil || !ok || h.Status != store.StatusPartial {
			t.Errorf("down=%v: hour 10 = %+v %v %v", down, h, ok, err)
		}
	}
}

type oneLineTranscriber struct{ calls []string }

func (f *oneLineTranscriber) Transcribe(_ context.Context, wav string, start time.Time, track string) ([]media.Utterance, error) {
	f.calls = append(f.calls, filepath.Base(wav))
	return []media.Utterance{{Start: start.Add(time.Second), End: start.Add(3 * time.Second), Track: track, Text: "said just before the click"}}, nil
}

// The recorder opens the next chunk only on its next sample, so right after a
// rotate the chunk it just finished is still the newest, with a fresh mtime.
// ProcessNow must use it anyway, and report the time it processed up to.
func TestProcessNowIncludesRotatedChunk(t *testing.T) {
	for _, down := range []bool{false, true} {
		j, ctl, _ := newJobs(t)
		if down {
			ctl.sendErr = errors.New("dial recorder: no such file")
		}
		fk := &closeSkills{}
		now := time.Date(2026, 10, 7, 10, 20, 0, 0, time.Local)
		withActivity(t, j, fk, "2026-10-07", 10, now)
		tr := &oneLineTranscriber{}
		j.Pipeline.Transcriber = tr
		j.Pipeline.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
			return nil, os.WriteFile(args[len(args)-1], []byte("RIFF"), 0o644)
		}
		chunk := filepath.Join(j.Paths.Audio("2026-10-07"), "1015-mic.m4a")
		if err := os.MkdirAll(filepath.Dir(chunk), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(chunk, []byte("m4a"), 0o644); err != nil {
			t.Fatal(err)
		}
		fresh := now.Add(-2 * time.Second)
		os.Chtimes(chunk, fresh, fresh)

		upto, err := j.ProcessNow(context.Background())
		if err != nil {
			t.Fatalf("down=%v: %v", down, err)
		}
		if down {
			// No rotate happened: the open chunk waits, and the notice says so.
			if len(tr.calls) != 0 || !upto.Equal(time.Date(2026, 10, 7, 10, 15, 0, 0, time.Local)) {
				t.Errorf("recorder down: transcribed %v, upto %v; want nothing, 10:15", tr.calls, upto)
			}
			continue
		}
		if !equal(tr.calls, []string{"1015-mic.wav"}) || !upto.Equal(now) {
			t.Errorf("transcribed %v, upto %v; want the rotated chunk, %v", tr.calls, upto, now)
		}
	}
}
