package process

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"workjournal/internal/media"
	"workjournal/internal/store"
)

// currentFixture sets up the in-progress hour 15 at Now (15:10:00): a
// finished mic chunk, the newest mic chunk still being written, the newest
// system chunk untouched for a minute, a screenshot and one taken "later".
func currentFixture(e *env) time.Time {
	now := e.p.Now()
	e.activity(now.Add(-5*time.Second), ev(15, 0, "start"), ev(15, 0, "app", "Code", "main.go"))
	e.file("audio/1500-mic.m4a", at(15, 5, 0))
	e.file("audio/1505-mic.m4a", now.Add(-10*time.Second))
	e.file("audio/1500-system.m4a", now.Add(-time.Minute))
	e.file("screens/150500.jpg", at(15, 5, 0))
	e.file("screens/151030.jpg", now.Add(-time.Minute))
	e.tr.utts["1500-mic"] = []fu{{1, 3, "partial mic"}}
	e.tr.utts["1505-mic"] = []fu{{1, 2, "said just now"}}
	e.tr.utts["1500-system"] = []fu{{2, 5, "system words"}}
	e.ocr.texts["150500.jpg"] = "editor text"
	e.ocr.texts["151030.jpg"] = "future text"
	return now
}

func TestProcessCurrentUsesOnlyReadyInputs(t *testing.T) {
	e := newEnv(t)
	now := currentFixture(e)
	if _, err := e.p.ProcessCurrent(context.Background(), now, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"1500-mic", "1500-system"}; !reflect.DeepEqual(e.tr.calls, want) {
		t.Fatalf("transcribed %v, want %v (the newest, fresh mic chunk must wait)", e.tr.calls, want)
	}
	if want := []string{"150500.jpg"}; !reflect.DeepEqual(e.ocr.files, want) {
		t.Fatalf("OCRed %v, want %v", e.ocr.files, want)
	}
	if h, ok := e.hourStatus(15); !ok || h.Status != store.StatusPartial {
		t.Fatalf("hour 15 = %+v %v, want partial", h, ok)
	}
	if want := []HourKey{{day, 15}}; !reflect.DeepEqual(e.sum.calls, want) {
		t.Fatalf("summarizer calls = %v", e.sum.calls)
	}
	doc, err := Load(e.paths.DerivedHour(day, 15))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Status != store.StatusPartial || len(doc.Transcript) != 2 {
		t.Fatalf("derived doc status %s transcript %q", doc.Status, speakers(doc.Transcript))
	}
	// Later the mic chunk is no longer the newest (the recorder rotated):
	// it is used at once although its mtime is fresh.
	e.file("audio/1510-mic.m4a", now.Add(-time.Second))
	e.file("audio/1505-mic.m4a", now.Add(-2*time.Second))
	e.resetCounts()
	if _, err := e.p.ProcessCurrent(context.Background(), now, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"1505-mic"}; !reflect.DeepEqual(e.tr.calls, want) {
		t.Fatalf("second pass transcribed %v, want %v", e.tr.calls, want)
	}
}

func TestProcessCurrentSkipsSummarizerOnIdenticalInputs(t *testing.T) {
	e := newEnv(t)
	now := currentFixture(e)
	ctx := context.Background()
	if _, err := e.p.ProcessCurrent(ctx, now, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.p.ProcessCurrent(ctx, now, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if len(e.sum.calls) != 1 {
		t.Fatalf("summarizer ran %d times on identical inputs", len(e.sum.calls))
	}
	later := now.Add(time.Minute) // the fresh mic chunk is ready now
	if _, err := e.p.ProcessCurrent(ctx, later, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if len(e.sum.calls) != 2 {
		t.Fatalf("summarizer calls = %d after new input, want 2", len(e.sum.calls))
	}
}

func TestProcessCurrentEmptyHourDoesNothing(t *testing.T) {
	e := newEnv(t)
	if _, err := e.p.ProcessCurrent(context.Background(), e.p.Now(), time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.hourStatus(15); ok || len(e.sum.calls) != 0 {
		t.Fatalf("empty hour got a record (%v) or a summary (%v)", ok, e.sum.calls)
	}
}

func TestPartialHourGetsFinalPassOnceEnded(t *testing.T) {
	e := newEnv(t)
	now := currentFixture(e)
	fk := &fakeSkills{out: map[string]string{"summarize-hour": hourOut, "categorize-time": `{"segments":[{"index":0,"category":"Deep work","confidence":0.9}]}`}, errs: map[string]error{}}
	e.p.Summarize = &SkillSummarizer{Runner: fk, Store: e.st, Paths: e.paths, Cfg: e.p.Cfg, Logf: t.Logf}
	ctx := context.Background()
	if _, err := e.p.ProcessCurrent(ctx, now, time.Time{}); err != nil {
		t.Fatal(err)
	}
	h, _ := e.hourStatus(15)
	if h.Status != store.StatusPartial || !strings.Contains(h.SummaryJSON, "Checkout design") {
		t.Fatalf("after partial pass: %+v", h)
	}
	if pend, err := e.p.PendingHours(); err != nil || len(pend) != 0 {
		t.Fatalf("in-progress hour pending: %v %v", pend, err)
	}

	e.p.Now = func() time.Time { return at(16, 10, 0) }
	pend, err := e.p.PendingHours()
	if err != nil || !reflect.DeepEqual(pend, []HourKey{{day, 15}}) {
		t.Fatalf("PendingHours after the hour ended = %v %v", pend, err)
	}
	e.resetCounts()
	if err := e.p.ProcessPending(ctx); err != nil {
		t.Fatal(err)
	}
	if want := []string{"1505-mic"}; !reflect.DeepEqual(e.tr.calls, want) {
		t.Fatalf("final pass transcribed %v, want only the chunk not cached yet", e.tr.calls)
	}
	if h, _ := e.hourStatus(15); h.Status != store.StatusDone {
		t.Fatalf("after final pass: %+v", h)
	}
}

func TestSkillSummarizerInputHash(t *testing.T) {
	e := newSumEnv(t)
	doc := &HourDoc{Day: day, Hour: 13, Segments: nil,
		Transcript: []media.Utterance{{Start: at(13, 1, 0), End: at(13, 1, 5), Track: "mic", Text: "hello"}}}
	a, err := e.s.InputHash(doc)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := e.s.InputHash(doc)
	doc.Transcript[0].Text = "hello there"
	c, _ := e.s.InputHash(doc)
	if a == "" || a != b || a == c {
		t.Fatalf("hashes %q %q %q", a, b, c)
	}
	if len(e.fk.calls) != 0 {
		t.Fatal("hashing ran a skill")
	}
}

// Readiness after a rotate: every chunk that started before the rotate is
// finished, even the newest one with a fresh mtime (the recorder opens the
// next chunk only on its next sample).
func TestProcessCurrentRotatedChunkIsReady(t *testing.T) {
	e := newEnv(t)
	now := currentFixture(e)
	covered, err := e.p.ProcessCurrent(context.Background(), now, now.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"1500-mic", "1500-system", "1505-mic"}; !reflect.DeepEqual(e.tr.calls, want) {
		t.Fatalf("transcribed %v, want %v", e.tr.calls, want)
	}
	if !covered.Equal(now) {
		t.Fatalf("covered = %v, want %v", covered, now)
	}
	// Without a rotate the fresh newest chunk is held back: covered stops at its start.
	e2 := newEnv(t)
	currentFixture(e2)
	covered, err = e2.p.ProcessCurrent(context.Background(), now, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !covered.Equal(at(15, 5, 0)) {
		t.Fatalf("covered without rotate = %v, want 15:05", covered)
	}
}

func TestFinalPassKeepsPartialSummaryUntilSummarized(t *testing.T) {
	e := newEnv(t)
	now := currentFixture(e)
	fk := &fakeSkills{out: map[string]string{"summarize-hour": hourOut, "categorize-time": `{"segments":[{"index":0,"category":"Deep work","confidence":0.9}]}`}, errs: map[string]error{}}
	e.p.Summarize = &SkillSummarizer{Runner: fk, Store: e.st, Paths: e.paths, Cfg: e.p.Cfg, Logf: t.Logf}
	ctx := context.Background()
	if _, err := e.p.ProcessCurrent(ctx, now, time.Time{}); err != nil {
		t.Fatal(err)
	}
	e.p.Now = func() time.Time { return at(16, 10, 0) }
	fk.errs["summarize-hour"] = errors.New("claude is down")
	if err := e.p.ProcessPending(ctx); err == nil {
		t.Fatal("summarizer failure not reported")
	}
	h, _ := e.hourStatus(15)
	if h.Status != store.StatusTranscribed || !strings.Contains(h.SummaryJSON, "Checkout design") {
		t.Fatalf("after failed final pass: %+v; the partial summary must stay", h)
	}
}

func TestPartialPathFiltersExcludedAndPaused(t *testing.T) {
	e := newEnv(t)
	now := e.p.Now()
	e.activity(now.Add(-time.Minute), ev(15, 0, "start"), ev(15, 0, "app", "Code", "main.go"),
		ev(15, 2, "excluded"), ev(15, 4, "included"), ev(15, 5, "pause"), ev(15, 7, "resume"))
	e.file("audio/1500-mic.m4a", at(15, 9, 0))
	e.tr.utts["1500-mic"] = []fu{{150, 170, "whisperexcluded"}, {330, 350, "whisperpaused"}, {500, 510, "spoken visible"}}
	for name, text := range map[string]string{"150300.jpg": "secretexcluded", "150600.jpg": "secretpaused", "150800.jpg": "visible screen"} {
		e.file("screens/"+name, at(15, 9, 0))
		e.ocr.texts[name] = text
	}
	e.p.Cfg.Rules = map[string]string{"Code": "Deep work"}
	fk := &fakeSkills{out: map[string]string{"summarize-hour": hourOut}, errs: map[string]error{}}
	e.p.Summarize = &SkillSummarizer{Runner: fk, Store: e.st, Paths: e.paths, Cfg: e.p.Cfg, Logf: t.Logf}
	if _, err := e.p.ProcessCurrent(context.Background(), now, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"150800.jpg"}; !reflect.DeepEqual(e.ocr.files, want) {
		t.Fatalf("OCR saw %v, want %v", e.ocr.files, want)
	}
	for _, secret := range []string{"secretexcluded", "secretpaused", "whisperexcluded", "whisperpaused"} {
		if hits, err := e.st.Search(secret, 10); err != nil || len(hits) != 0 {
			t.Errorf("%s indexed: %+v %v", secret, hits, err)
		}
	}
	if hits, err := e.st.Search("visible", 10); err != nil || len(hits) == 0 {
		t.Errorf("visible text not indexed: %v", err)
	}
	var hourIn string
	for _, c := range fk.calls {
		if c.skill == "summarize-hour" {
			b, _ := json.Marshal(c.input)
			hourIn = string(b)
		}
	}
	if !strings.Contains(hourIn, "spoken visible") || !strings.Contains(hourIn, "visible screen") {
		t.Fatalf("summarize-hour input lacks the visible content: %s", hourIn)
	}
	for _, secret := range []string{"secretexcluded", "secretpaused", "whisperexcluded", "whisperpaused"} {
		if strings.Contains(hourIn, secret) {
			t.Errorf("summarize-hour input contains %s", secret)
		}
	}
	assertDerivedLacks(t, e, 15, "spoken visible", "whisperexcluded", "whisperpaused", "secretexcluded", "secretpaused")
}

// The final pass drops hidden speech from the derived doc too: derived/ is
// readable by Claude.
func TestFinalPathDropsExcludedAndPausedSpeech(t *testing.T) {
	e := newEnv(t)
	e.activity(old, ev(13, 0, "start"), ev(13, 0, "app", "Code", "main.go"),
		ev(13, 2, "excluded"), ev(13, 4, "included"), ev(13, 5, "pause"), ev(13, 7, "resume"))
	e.file("audio/1300-mic.m4a", old)
	e.tr.utts["1300-mic"] = []fu{{150, 170, "whisperexcluded"}, {330, 350, "whisperpaused"}, {500, 510, "spoken visible"}}
	doc, err := e.p.Transcribe(context.Background(), day, 13)
	if err != nil {
		t.Fatal(err)
	}
	if got := speakers(doc.Transcript); !reflect.DeepEqual(got, []string{"me: spoken visible"}) {
		t.Fatalf("transcript = %q", got)
	}
	assertDerivedLacks(t, e, 13, "spoken visible", "whisperexcluded", "whisperpaused")
}

// assertDerivedLacks checks derived/<day>/HH.json holds want but none of hidden.
func assertDerivedLacks(t *testing.T, e *env, hour int, want string, hidden ...string) {
	t.Helper()
	b, err := os.ReadFile(e.paths.DerivedHour(day, hour))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), want) {
		t.Fatalf("derived doc lacks %q", want)
	}
	for _, h := range hidden {
		if strings.Contains(string(b), h) {
			t.Errorf("derived/%s/%02d.json contains %q", day, hour, h)
		}
	}
}
