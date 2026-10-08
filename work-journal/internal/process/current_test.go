package process

import (
	"context"
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
	if err := e.p.ProcessCurrent(context.Background(), now); err != nil {
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
	if err := e.p.ProcessCurrent(context.Background(), now); err != nil {
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
	if err := e.p.ProcessCurrent(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := e.p.ProcessCurrent(ctx, now); err != nil {
		t.Fatal(err)
	}
	if len(e.sum.calls) != 1 {
		t.Fatalf("summarizer ran %d times on identical inputs", len(e.sum.calls))
	}
	later := now.Add(time.Minute) // the fresh mic chunk is ready now
	if err := e.p.ProcessCurrent(ctx, later); err != nil {
		t.Fatal(err)
	}
	if len(e.sum.calls) != 2 {
		t.Fatalf("summarizer calls = %d after new input, want 2", len(e.sum.calls))
	}
}

func TestProcessCurrentEmptyHourDoesNothing(t *testing.T) {
	e := newEnv(t)
	if err := e.p.ProcessCurrent(context.Background(), e.p.Now()); err != nil {
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
	if err := e.p.ProcessCurrent(ctx, now); err != nil {
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
