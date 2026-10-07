package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"workjournal/internal/capture"
	"workjournal/internal/config"
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
