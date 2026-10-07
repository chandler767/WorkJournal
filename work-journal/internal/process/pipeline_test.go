package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"workjournal/internal/activity"
	"workjournal/internal/diarize"
	"workjournal/internal/media"
	"workjournal/internal/store"
)

var old = at(14, 0, 0) // comfortably older than Now-30s

func TestPendingHoursSkipsCurrentHourAndFreshFiles(t *testing.T) {
	e := newEnv(t)
	now := e.p.Now()
	e.activity(old,
		ev(13, 0, "start"), ev(13, 0, "app", "Code"), ev(14, 0, "app", "Arc"), ev(15, 0, "app", "Slack"))
	e.file("audio/1300-mic.m4a", old)
	e.file("screens/131000.jpg", old)
	e.file("audio/1450-system.m4a", old)
	e.file("audio/1455-system.m4a", now.Add(-10*time.Second))
	e.file("screens/145000.jpg", old)
	e.file("audio/1500-mic.m4a", old)
	e.file("audio/notes.txt", old) // ignored

	got, err := e.p.PendingHours()
	if err != nil {
		t.Fatal(err)
	}
	if want := []HourKey{{day, 13}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PendingHours = %+v, want %+v", got, want)
	}

	// A fresh activity.jsonl holds back only the latest hour with data (15).
	later := at(16, 10, 0)
	e.p.Now = func() time.Time { return later }
	e.activity(later.Add(-5*time.Second),
		ev(13, 0, "start"), ev(13, 0, "app", "Code"), ev(14, 0, "app", "Arc"), ev(15, 0, "app", "Slack"))
	got, err = e.p.PendingHours()
	if err != nil {
		t.Fatal(err)
	}
	if want := []HourKey{{day, 13}, {day, 14}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("with fresh activity log: PendingHours = %+v, want %+v", got, want)
	}
	e.p.Now = func() time.Time { return now }

	for _, c := range []struct {
		status string
		want   []HourKey
	}{
		{store.StatusDone, nil},
		{store.StatusNeedsReview, nil},
		{store.StatusTranscribed, []HourKey{{day, 13}}},
	} {
		if err := e.st.UpsertHour(store.HourRecord{Day: day, Hour: 13, Status: c.status, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		got, err := e.p.PendingHours()
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(c.want) || (len(got) > 0 && got[0] != c.want[0]) {
			t.Errorf("status %s: PendingHours = %+v, want %+v", c.status, got, c.want)
		}
	}
}

// transcribeFixture sets up hour 13 with mic, two system chunks and a screenshot.
func transcribeFixture(e *env) {
	e.activity(old, ev(13, 0, "start"), ev(13, 0, "app", "Code", "main.go"))
	for _, f := range []string{"audio/1300-mic.m4a", "audio/1300-system.m4a", "audio/1305-system.m4a", "audio/1405-mic.m4a", "screens/131000.jpg"} {
		e.file(f, old)
	}
	e.tr.utts["1300-mic"] = []fu{{1, 3, "mic hello"}}
	e.tr.utts["1300-system"] = []fu{{2, 5, "first voice"}, {12, 15, "second voice"}}
	e.tr.utts["1305-system"] = []fu{{1, 4, "second again"}}
	e.dz.by["1300-system"] = diar{
		turns: []diarize.Turn{tn(0, 10, 0), tn(10, 20, 1)},
		embs:  map[int][]float32{0: {1, 0, 0}, 1: {0, 1, 0}},
	}
	e.dz.by["1305-system"] = diar{
		turns: []diarize.Turn{tn(0, 10, 0)},
		embs:  map[int][]float32{0: {0.05, 1, 0}}, // same voice as Speaker 2
	}
	e.ocr.texts["131000.jpg"] = "hello world on screen"
}

func speakers(us []media.Utterance) []string {
	var out []string
	for _, u := range us {
		out = append(out, u.Speaker+": "+u.Text)
	}
	return out
}

func TestTranscribeBuildsHourDoc(t *testing.T) {
	e := newEnv(t)
	transcribeFixture(e)

	doc, err := e.p.Transcribe(context.Background(), day, 13)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Status != store.StatusTranscribed || doc.Day != day || doc.Hour != 13 {
		t.Fatalf("doc = %s %d %s", doc.Day, doc.Hour, doc.Status)
	}
	want := []string{"me: mic hello", "Speaker 1: first voice", "Speaker 2: second voice", "Speaker 2: second again"}
	if got := speakers(doc.Transcript); !reflect.DeepEqual(got, want) {
		t.Fatalf("transcript = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(e.tr.calls, []string{"1300-mic", "1300-system", "1305-system"}) {
		t.Errorf("transcribed chunks = %v", e.tr.calls)
	}
	for _, w := range e.tr.wavs {
		if _, err := os.Stat(filepath.Dir(w)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("temp wav dir %s not removed: %v", filepath.Dir(w), err)
		}
	}
	if len(doc.Screens) != 1 || doc.Screens[0].Text != "hello world on screen" {
		t.Errorf("screens = %+v", doc.Screens)
	}
	if _, ok := doc.Embeddings["Speaker 1"]; !ok {
		t.Errorf("embeddings kept in memory = %v", doc.Embeddings)
	}

	h, ok := e.hourStatus(13)
	if !ok || h.Status != store.StatusTranscribed {
		t.Fatalf("hour record = %+v %v", h, ok)
	}
	loaded, err := Load(e.paths.DerivedHour(day, 13))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != store.StatusTranscribed || len(loaded.Transcript) != 4 || len(loaded.Segments) != 1 {
		t.Fatalf("derived doc = %+v", loaded)
	}

	segs, err := e.st.SegmentsOn(day)
	if err != nil || len(segs) != 1 || segs[0].App != "Code" || segs[0].Source != "auto" || segs[0].Category != "" || segs[0].Kind != activity.KindActive {
		t.Fatalf("segments = %+v %v", segs, err)
	}
	hits, err := e.st.Search("first voice", 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("search transcript = %+v %v", hits, err)
	}
	if hits[0].Kind != "transcript" || hits[0].Text != "Speaker 1: first voice" || hits[0].Ref != "hour/2026-10-07/13#2" || !hits[0].TS.Equal(at(13, 0, 2)) {
		t.Errorf("transcript hit = %+v", hits[0])
	}
	hits, err = e.st.Search("screen", 10)
	if err != nil || len(hits) != 1 || hits[0].Kind != "ocr" || hits[0].Ref != "hour/2026-10-07/13#600" {
		t.Fatalf("search ocr = %+v %v", hits, err)
	}
}

func TestDiarizationOffLabelsOthers(t *testing.T) {
	e := newEnv(t)
	transcribeFixture(e)
	e.p.Cfg.Diarization = "off"
	doc, err := e.p.Transcribe(context.Background(), day, 13)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"me: mic hello", "others: first voice", "others: second voice", "others: second again"}
	if got := speakers(doc.Transcript); !reflect.DeepEqual(got, want) {
		t.Fatalf("transcript = %q", got)
	}
}

func TestKnownVoiceNamed(t *testing.T) {
	e := newEnv(t)
	transcribeFixture(e)
	if err := e.p.Voices.Enroll("Priya", []float32{0, 0, 1}); err != nil {
		t.Fatal(err)
	}
	e.dz.by["1305-system"] = diar{
		turns: []diarize.Turn{tn(0, 10, 0)},
		embs:  map[int][]float32{0: {0, 0.1, 1}},
	}
	doc, err := e.p.Transcribe(context.Background(), day, 13)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"me: mic hello", "Speaker 1: first voice", "Speaker 2: second voice", "Priya: second again"}
	if got := speakers(doc.Transcript); !reflect.DeepEqual(got, want) {
		t.Fatalf("transcript = %q", got)
	}
}

func TestExcludedSpanTextNotIndexed(t *testing.T) {
	e := newEnv(t)
	e.activity(old, ev(13, 0, "start"), ev(13, 0, "app", "Code"), ev(13, 20, "excluded"), ev(13, 30, "included"))
	e.file("screens/131000.jpg", old)
	e.file("screens/132500.jpg", old)
	e.ocr.texts["131000.jpg"] = "visible alpha"
	e.ocr.texts["132500.jpg"] = "secretbanana"

	doc, err := e.p.Transcribe(context.Background(), day, 13)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.ocr.files, []string{"131000.jpg"}) {
		t.Fatalf("OCR saw %v; excluded screenshot must be dropped first", e.ocr.files)
	}
	if hits, err := e.st.Search("secretbanana", 10); err != nil || len(hits) != 0 {
		t.Fatalf("excluded text indexed: %+v %v", hits, err)
	}
	if hits, err := e.st.Search("alpha", 10); err != nil || len(hits) != 1 {
		t.Fatalf("visible text missing: %+v %v", hits, err)
	}
	if len(doc.Screens) != 1 {
		t.Errorf("screens = %+v", doc.Screens)
	}
}

func TestUnreadableChunkSkipped(t *testing.T) {
	e := newEnv(t)
	transcribeFixture(e)
	e.bad["1300-system.m4a"] = true
	doc, err := e.p.Transcribe(context.Background(), day, 13)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Transcript) != 2 || doc.Status != store.StatusTranscribed {
		t.Fatalf("transcript = %q", speakers(doc.Transcript))
	}
	if !strings.Contains(strings.Join(e.logs, "\n"), "1300-system.m4a") {
		t.Errorf("skipped chunk not logged: %q", e.logs)
	}
}

func TestEmptyHourSkipsSkills(t *testing.T) {
	e := newEnv(t)
	e.activity(old, ev(13, 0, "start"), ev(13, 0, "idle"), ev(13, 0, "app", "Code"))
	if err := e.p.ProcessPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	h, ok := e.hourStatus(13)
	if !ok || h.Status != store.StatusDone || h.SummaryJSON != "{}" {
		t.Fatalf("hour = %+v %v", h, ok)
	}
	if len(e.sum.calls) != 0 {
		t.Fatalf("summarizer called for empty hour: %v", e.sum.calls)
	}
	if got, _ := e.p.PendingHours(); len(got) != 0 {
		t.Fatalf("done hour still pending: %v", got)
	}
}

func TestProcessPendingContinuesAfterError(t *testing.T) {
	e := newEnv(t)
	e.activity(old, ev(13, 0, "start"), ev(13, 0, "app", "Code"), ev(14, 0, "app", "Arc"))
	e.file("audio/1300-mic.m4a", old)
	e.file("audio/1400-mic.m4a", old)
	e.tr.utts["1400-mic"] = []fu{{0, 2, "hour fourteen"}}
	e.tr.errs["1300-mic"] = errors.New("whisper exploded")

	err := e.p.ProcessPending(context.Background())
	if err == nil || !strings.Contains(err.Error(), "whisper exploded") {
		t.Fatalf("want joined error, got %v", err)
	}
	if !strings.Contains(strings.Join(e.logs, "\n"), "whisper exploded") {
		t.Errorf("error not logged: %q", e.logs)
	}
	if _, ok := e.hourStatus(13); ok {
		t.Error("failed hour's status must stay unchanged")
	}
	if h, ok := e.hourStatus(14); !ok || h.Status != store.StatusTranscribed {
		t.Fatalf("hour 14 = %+v %v", h, ok)
	}
	if !reflect.DeepEqual(e.sum.calls, []HourKey{{day, 14}}) {
		t.Fatalf("summarizer calls = %v", e.sum.calls)
	}

	// A transcribed hour is summarized again from its derived doc, not re-transcribed.
	delete(e.tr.errs, "1300-mic")
	e.tr.calls = nil
	e.sum.calls = nil
	if err := e.p.ProcessPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.tr.calls, []string{"1300-mic"}) {
		t.Errorf("transcribed again: %v", e.tr.calls)
	}
	if !reflect.DeepEqual(e.sum.calls, []HourKey{{day, 13}, {day, 14}}) {
		t.Errorf("summarizer calls = %v", e.sum.calls)
	}
}

func TestNilSummarizerLeavesTranscribed(t *testing.T) {
	e := newEnv(t)
	transcribeFixture(e)
	e.p.Summarize = nil
	if err := e.p.ProcessPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h, ok := e.hourStatus(13); !ok || h.Status != store.StatusTranscribed {
		t.Fatalf("hour = %+v %v", h, ok)
	}
}

func TestDerivedJSONHasNoEmbeddings(t *testing.T) {
	e := newEnv(t)
	transcribeFixture(e)
	doc, err := e.p.Transcribe(context.Background(), day, 13)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Embeddings) == 0 {
		t.Fatal("expected in-memory embeddings")
	}
	b, err := os.ReadFile(e.paths.DerivedHour(day, 13))
	if err != nil {
		t.Fatal(err)
	}
	// (The temp path itself contains the test name, so look for the JSON key.)
	if s := strings.ToLower(string(b)); strings.Contains(s, `"embeddings"`) || strings.Contains(s, "0.05") {
		t.Fatalf("derived JSON leaks embeddings: %s", b)
	}
}

func TestMeetingsUpsertedPreservingSummary(t *testing.T) {
	e := newEnv(t)
	e.activity(old,
		ev(12, 0, "start"), ev(12, 0, "app", "Code"),
		ev(12, 50, "app", "zoom.us", "Zoom Meeting"), ev(13, 20, "app", "Code"),
		ev(13, 40, "app", "zoom.us", "Zoom Meeting"))
	first := "2026-10-07-1250-zoom-us"
	if err := e.st.UpsertMeeting(store.MeetingRecord{ID: first, Day: day, Start: at(12, 50, 0), End: at(13, 0, 0), App: "zoom.us", SummaryJSON: `{"keep":true}`}); err != nil {
		t.Fatal(err)
	}
	doc, err := e.p.Transcribe(context.Background(), day, 13)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Meetings) != 2 || doc.Meetings[0].ID != first || !doc.Meetings[0].End.Equal(at(13, 20, 0)) {
		t.Fatalf("meetings = %+v", doc.Meetings)
	}
	if !reflect.DeepEqual(doc.EndedMeetings, []string{first}) {
		t.Fatalf("ended = %v", doc.EndedMeetings)
	}
	m, ok, err := e.st.Meeting(first)
	if err != nil || !ok || m.SummaryJSON != `{"keep":true}` || !m.End.Equal(at(13, 20, 0)) {
		t.Fatalf("meeting record = %+v %v %v", m, ok, err)
	}
	second := "2026-10-07-1340-zoom-us"
	if m, ok, _ := e.st.Meeting(second); !ok || m.SummaryJSON != "" || !m.End.Equal(at(14, 0, 0)) {
		t.Fatalf("second meeting = %+v %v", m, ok)
	}
}

func TestMeetingSpeechSpansIncludeEarlierHours(t *testing.T) {
	e := newEnv(t)
	e.activity(old, ev(12, 0, "start"), ev(12, 0, "app", "Arc", "call"))
	e.p.Cfg.Diarization = "off"
	// Hour 12's derived doc holds one minute of two-way speech ending 12:59:30.
	prev := &HourDoc{Day: day, Hour: 12, Status: store.StatusTranscribed, Transcript: []media.Utterance{
		{Start: at(12, 58, 30), End: at(12, 59, 30), Track: "mic", Speaker: "me", Text: "a"},
		{Start: at(12, 58, 30), End: at(12, 59, 30), Track: "system", Speaker: "others", Text: "b"},
	}}
	if err := prev.Save(e.paths.DerivedHour(day, 12)); err != nil {
		t.Fatal(err)
	}
	// Hour 13 adds one more minute: together they reach the 2-minute minimum.
	e.file("audio/1300-mic.m4a", old)
	e.file("audio/1300-system.m4a", old)
	e.tr.utts["1300-mic"] = []fu{{0, 60, "c"}}
	e.tr.utts["1300-system"] = []fu{{0, 60, "d"}}
	doc, err := e.p.Transcribe(context.Background(), day, 13)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Meetings) != 1 || doc.Meetings[0].ID != "2026-10-07-1258-arc" || len(doc.EndedMeetings) != 1 {
		t.Fatalf("meetings = %+v ended = %v", doc.Meetings, doc.EndedMeetings)
	}
}

func TestHourDocRoundTrip(t *testing.T) {
	doc := &HourDoc{
		Day: day, Hour: 13, Status: store.StatusTranscribed,
		Segments:      []activity.Segment{{Start: at(13, 0, 0), End: at(13, 30, 0), App: "Code", Title: "x", Kind: activity.KindActive}},
		Transcript:    []media.Utterance{{Start: at(13, 0, 1), End: at(13, 0, 2), Track: "mic", Speaker: "me", Text: "hi"}},
		Screens:       []media.ScreenText{{Time: at(13, 1, 0), File: "/x/131000.jpg", Text: "s"}},
		Meetings:      []activity.Meeting{{ID: "m", Start: at(13, 0, 0), End: at(13, 10, 0), App: "zoom.us"}},
		EndedMeetings: []string{"m"},
		Embeddings:    map[string][]float32{"Speaker 1": {1, 2}},
	}
	path := filepath.Join(t.TempDir(), "derived", day, "13.json")
	if err := doc.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	doc.Embeddings = nil
	if !reflect.DeepEqual(got, doc) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, doc)
	}
	b, _ := os.ReadFile(path)
	for _, key := range []string{`"day"`, `"hour"`, `"status"`, `"segments"`, `"transcript"`, `"screens"`, `"meetings"`, `"ended_meetings"`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("derived JSON missing %s: %s", key, b)
		}
	}
	if ents, _ := os.ReadDir(filepath.Dir(path)); len(ents) != 1 {
		t.Errorf("temp files left behind: %v", ents)
	}
}

// prevAt is a time on the day before `day`.
func prevAt(h, m int) time.Time { return time.Date(2026, 10, 6, h, m, 0, 0, time.Local) }

func TestStateCarriesAcrossMidnight(t *testing.T) {
	e := newEnv(t)
	e.activityOn("2026-10-06", old, evAt(prevAt(23, 0), "start"), evAt(prevAt(23, 30), "app", "Xcode", "App.swift"))
	e.file("screens/001000.jpg", old)
	e.ocr.texts["001000.jpg"] = "func main"

	doc, err := e.p.Transcribe(context.Background(), day, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Segments) != 1 {
		t.Fatalf("segments = %+v", doc.Segments)
	}
	s := doc.Segments[0]
	if s.Kind != activity.KindActive || s.App != "Xcode" || !s.Start.Equal(at(0, 0, 0)) || !s.End.Equal(at(1, 0, 0)) {
		t.Fatalf("segment = %+v", s)
	}
	if !reflect.DeepEqual(e.ocr.files, []string{"001000.jpg"}) || len(doc.Screens) != 1 {
		t.Fatalf("OCR saw %v, screens %+v", e.ocr.files, doc.Screens)
	}
	if doc.Status != store.StatusTranscribed {
		t.Fatalf("status = %s", doc.Status)
	}
}

func TestExclusionCarriesAcrossMidnight(t *testing.T) {
	e := newEnv(t)
	e.activityOn("2026-10-06", old,
		evAt(prevAt(23, 0), "start"), evAt(prevAt(23, 0), "app", "Bank", "acct"), evAt(prevAt(23, 50), "excluded"))
	e.activity(old, ev(0, 30, "included"))
	e.file("screens/001000.jpg", old)
	e.file("screens/004000.jpg", old)
	e.ocr.texts["001000.jpg"] = "secretbalance"
	e.ocr.texts["004000.jpg"] = "public page"

	doc, err := e.p.Transcribe(context.Background(), day, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.ocr.files, []string{"004000.jpg"}) {
		t.Fatalf("OCR saw %v; screenshot in carried-over exclusion must be dropped", e.ocr.files)
	}
	if len(doc.Segments) != 2 || doc.Segments[0].Kind != activity.KindExcluded || doc.Segments[1].App != "Bank" {
		t.Fatalf("segments = %+v", doc.Segments)
	}
	if hits, _ := e.st.Search("secretbalance", 10); len(hits) != 0 {
		t.Fatalf("excluded text indexed: %+v", hits)
	}
}

func TestMeetingEndingOnBoundaryEndsNextHour(t *testing.T) {
	e := newEnv(t)
	e.activity(old, ev(12, 0, "start"), ev(12, 0, "app", "Code"),
		ev(12, 30, "app", "zoom.us", "Zoom Meeting"), ev(13, 0, "app", "Code"))
	id := "2026-10-07-1230-zoom-us"

	doc, err := e.p.Transcribe(context.Background(), day, 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Meetings) != 1 || doc.Meetings[0].ID != id || len(doc.EndedMeetings) != 0 {
		t.Fatalf("hour 12: meetings %+v ended %v", doc.Meetings, doc.EndedMeetings)
	}
	doc, err = e.p.Transcribe(context.Background(), day, 13)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Meetings) != 1 || doc.Meetings[0].ID != id || !reflect.DeepEqual(doc.EndedMeetings, []string{id}) {
		t.Fatalf("hour 13: meetings %+v ended %v", doc.Meetings, doc.EndedMeetings)
	}
}

func TestEndedMeetingKeepsHourForSummary(t *testing.T) {
	e := newEnv(t)
	// The recorder stops when the meeting ends on the boundary: hour 13 has no
	// activity of its own, but it is where the meeting is reported ended.
	e.activity(old, ev(12, 0, "start"), ev(12, 30, "app", "zoom.us", "Zoom Meeting"), ev(13, 0, "stop"))
	if err := e.p.ProcessPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	h, ok := e.hourStatus(13)
	if !ok || h.Status != store.StatusTranscribed {
		t.Fatalf("hour 13 = %+v %v; an hour that ends a meeting is not empty", h, ok)
	}
	if !reflect.DeepEqual(e.sum.calls, []HourKey{{day, 12}, {day, 13}}) {
		t.Fatalf("summarizer calls = %v", e.sum.calls)
	}
}
