package main

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"workjournal/internal/notes"
	"workjournal/internal/store"
)

// A correction on a processed hour reprocesses it (with the note in the
// skill input), then rewrites the day's journal entry and the week's
// rollup that were already written, and empties the queue. A pending hour
// is one whose reprocessing was interrupted, so it is finished, not
// skipped.
func TestProcessNotesReprocessesAndRewritesJournals(t *testing.T) {
	j, _, _ := newJobs(t)
	fk := &closeSkills{}
	const day = "2026-10-07"
	withActivity(t, j, fk, day, 9, time.Date(2026, 10, 7, 12, 0, 0, 0, time.Local))
	ctx := context.Background()
	if err := j.ProcessPending(ctx); err != nil {
		t.Fatal(err)
	}
	if err := j.CloseDay(ctx, day); err != nil {
		t.Fatal(err)
	}
	if err := j.Rollup(ctx, "2026-W41"); err != nil {
		t.Fatal(err)
	}
	if err := j.Store.UpsertHour(store.HourRecord{Day: day, Hour: 10, Status: store.StatusPending}); err != nil {
		t.Fatal(err)
	}
	fk.called = nil
	if _, err := notes.Add(j.Store, notes.Draft{Kind: "correction", Day: day, From: "09:10", To: "10:20", Text: "disregard speech, bystanders"}, store.SourceDashboard, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := j.ProcessNotes(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"summarize-hour", "close-day", "weekly-rollup"}
	if !slices.Equal(fk.called, want) {
		t.Fatalf("skills = %v, want %v", fk.called, want)
	}
	if !strings.Contains(fk.closeInput, `"user_notes":[{"kind":"correction","from":"09:10","to":"10:20","title":"","context_lines":["disregard speech, bystanders"]}]`) {
		t.Errorf("close-day input lacks the note: %s", fk.closeInput)
	}
	if q, _ := j.Store.NoteQueue(); len(q) != 0 {
		t.Errorf("queue left %+v", q)
	}
	if rec, _, _ := j.Store.Hour(day, 10); rec.Status == store.StatusPending {
		t.Error("interrupted hour left pending")
	}
}

// A context note on a day without a journal entry summarizes its hour
// again from the derived doc and writes no journal; once retention has
// removed the raw media a correction still resummarizes.
func TestProcessNotesContextResummarizes(t *testing.T) {
	j, _, _ := newJobs(t)
	fk := &closeSkills{}
	const day = "2026-10-07"
	withActivity(t, j, fk, day, 9, time.Date(2026, 10, 7, 12, 0, 0, 0, time.Local))
	ctx := context.Background()
	if err := j.ProcessPending(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(j.Paths.RawDay(day)); err != nil {
		t.Fatal(err)
	}
	fk.called = nil
	notes.Add(j.Store, notes.Draft{Kind: "context", Day: day, From: "09:00", To: "09:30", Text: "Cloudflare planning call"}, store.SourceMCP, time.Now())
	notes.Add(j.Store, notes.Draft{Kind: "correction", Day: day, From: "09:00", To: "09:30", Text: "disregard"}, store.SourceMCP, time.Now())
	if err := j.ProcessNotes(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fk.called, []string{"summarize-hour"}) {
		t.Fatalf("skills = %v", fk.called)
	}
	if q, _ := j.Store.NoteQueue(); len(q) != 0 {
		t.Errorf("queue left %+v", q)
	}
}

// failSkills fails summarize-hour while fail is set.
type failSkills struct {
	*closeSkills
	fail bool
}

func (f *failSkills) Run(ctx context.Context, skill, input string, out any) error {
	if f.fail && skill == "summarize-hour" {
		return errors.New("claude failed")
	}
	return f.closeSkills.Run(ctx, skill, input, out)
}

// A correction whose reprocessing fails stays queued with its journal
// until a later pass finishes it.
func TestProcessNotesRetriesFailedCorrection(t *testing.T) {
	j, _, _ := newJobs(t)
	fk := &closeSkills{}
	const day = "2026-10-07"
	withActivity(t, j, fk, day, 9, time.Date(2026, 10, 7, 12, 0, 0, 0, time.Local))
	ctx := context.Background()
	if err := j.ProcessPending(ctx); err != nil {
		t.Fatal(err)
	}
	if err := j.CloseDay(ctx, day); err != nil {
		t.Fatal(err)
	}
	ff := &failSkills{closeSkills: fk, fail: true}
	j.Runner, j.Summarizer.Runner = ff, ff
	if _, err := notes.Add(j.Store, notes.Draft{Kind: "correction", Day: day, From: "09:10", To: "09:20", Text: "disregard"}, store.SourceDashboard, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := j.ProcessNotes(ctx); err == nil {
		t.Fatal("failure not reported")
	}
	for i := 0; i < 2; i++ {
		if q, _ := j.Store.NoteQueue(); len(q) != 2 {
			t.Fatalf("pass %d: queue %+v, want the hour and its journal", i, q)
		}
		if i == 0 {
			j.ProcessNotes(ctx)
		}
	}
	ff.fail, fk.called = false, nil
	if err := j.ProcessNotes(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fk.called, []string{"summarize-hour", "close-day"}) {
		t.Fatalf("skills = %v", fk.called)
	}
	if q, _ := j.Store.NoteQueue(); len(q) != 0 {
		t.Errorf("queue left %+v", q)
	}
}
