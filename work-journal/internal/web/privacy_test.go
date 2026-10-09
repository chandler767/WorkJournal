package web

import (
	"strings"
	"testing"
	"time"

	"workjournal/internal/process"
)

// seedRemoved records two removed spans on the seeded hour 09.
func seedRemoved(t *testing.T, e *env) {
	t.Helper()
	doc, err := process.Load(e.p.DerivedHour(seedDay, 9))
	if err != nil {
		t.Fatal(err)
	}
	doc.Removed = []process.RemovedSpan{
		{Start: seedTime(9, 12), End: seedTime(9, 16), Reason: "bystander"},
		{Start: seedTime(9, 40), End: seedTime(9, 40).Add(20 * time.Second), Reason: "personal_non_work"},
	}
	if err := doc.Save(e.p.DerivedHour(seedDay, 9)); err != nil {
		t.Fatal(err)
	}
}

func TestHourPageShowsRemovedSpans(t *testing.T) {
	e := newEnv(t)
	seedRemoved(t, e)
	b := e.get("/hour/2026-10-07/09").Body.String()
	mustContain(t, b, "4 min of non-work audio removed (09:12–09:16)", "Under a minute of non-work audio removed (09:40–09:40)")
	for _, leak := range []string{"bystander", "personal_non_work"} {
		if strings.Contains(b, leak) {
			t.Errorf("hour page shows the reason %q", leak)
		}
	}
}

func TestDayPageNotesRemovedAudio(t *testing.T) {
	e := newEnv(t)
	if b := e.get("/day/2026-10-07").Body.String(); strings.Contains(b, "non-work audio removed") {
		t.Fatal("note shown without removed audio")
	}
	seedRemoved(t, e)
	b := e.get("/day/2026-10-07").Body.String()
	mustContain(t, b, `class="removed-note muted"`, "Some non-work audio removed")
}

func TestHourPageMergesAdjacentRemovedSpans(t *testing.T) {
	e := newEnv(t)
	doc, err := process.Load(e.p.DerivedHour(seedDay, 9))
	if err != nil {
		t.Fatal(err)
	}
	doc.Removed = []process.RemovedSpan{
		{Start: seedTime(9, 12), End: seedTime(9, 14), Reason: "bystander"},
		{Start: seedTime(9, 14).Add(10 * time.Second), End: seedTime(9, 16), Reason: "personal_non_work"},
		{Start: seedTime(9, 30), End: seedTime(9, 30).Add(59 * time.Second), Reason: "bystander"},
	}
	if err := doc.Save(e.p.DerivedHour(seedDay, 9)); err != nil {
		t.Fatal(err)
	}
	b := e.get("/hour/2026-10-07/09").Body.String()
	mustContain(t, b, "4 min of non-work audio removed (09:12–09:16)", "Under a minute of non-work audio removed (09:30–09:30)")
	if strings.Contains(b, "(09:12–09:14)") || strings.Contains(b, "(09:14–09:16)") {
		t.Error("adjacent spans not merged")
	}
}

func TestHourPageNotesWithheldSpeech(t *testing.T) {
	e := newEnv(t)
	doc, err := process.Load(e.p.DerivedHour(seedDay, 9))
	if err != nil {
		t.Fatal(err)
	}
	doc.Transcript, doc.SpeechWithheld = nil, true
	if err := doc.Save(e.p.DerivedHour(seedDay, 9)); err != nil {
		t.Fatal(err)
	}
	mustContain(t, e.get("/hour/2026-10-07/09").Body.String(), "Speech not shown: privacy check failed")
}
