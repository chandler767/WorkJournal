package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestOpenReadOnlyReadsButNeverWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.db")
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.SetKV("k", "v"); err != nil {
		t.Fatal(err)
	}
	check := func(t *testing.T) {
		t.Helper()
		r, err := OpenReadOnly(path)
		if err != nil {
			t.Fatalf("OpenReadOnly: %v", err)
		}
		defer r.Close()
		if v, ok, err := r.GetKV("k"); err != nil || !ok || v != "v" {
			t.Fatalf("GetKV = %q %v %v", v, ok, err)
		}
		if err := r.SetKV("k", "changed"); err == nil {
			t.Fatal("SetKV succeeded on a read-only store")
		}
		if err := r.UpsertHour(HourRecord{Day: "2026-10-07", Hour: 9, Status: StatusDone}); err == nil {
			t.Fatal("UpsertHour succeeded on a read-only store")
		}
	}
	// Beside a live writer (serve), and alone.
	t.Run("with writer", check)
	if v, _, _ := w.GetKV("k"); v != "v" {
		t.Fatalf("value changed to %q", v)
	}
	w.Close()
	t.Run("alone", check)
}

func TestOpenReadOnlyMissingDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "none.db")
	if _, err := OpenReadOnly(path); err == nil {
		t.Fatal("OpenReadOnly created or opened a missing database")
	}
	if _, err := OpenReadOnly(path); err == nil {
		t.Fatal("second open of a missing database succeeded")
	}
}

func TestSearchRangeFiltersDaysAndPages(t *testing.T) {
	s := openTest(t)
	for _, day := range []string{"2026-10-06", "2026-10-07", "2026-10-08"} {
		if err := s.ReplaceText(day, 9, []TextDoc{
			{Kind: "note", TS: at(day, 9, 0), Text: "deploy pipeline " + day, Ref: "hour/" + day + "/09"},
			{Kind: "note", TS: at(day, 9, 1), Text: "deploy again " + day, Ref: "hour/" + day + "/09"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := s.SearchRange("deploy", "2026-10-07", "2026-10-08", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 4 {
		t.Fatalf("got %d hits, want 4", len(hits))
	}
	for _, h := range hits {
		if h.Day == "2026-10-06" {
			t.Fatalf("hit outside range: %+v", h)
		}
	}
	page, err := s.SearchRange("deploy", "", "", 2, 4)
	if err != nil || len(page) != 2 {
		t.Fatalf("offset page: %d hits, %v", len(page), err)
	}
	all, _ := s.Search("deploy", 10)
	if len(all) != 6 {
		t.Fatalf("Search found %d, want 6", len(all))
	}
}

// A database from before imported recordings has no imports table; the
// read-only store (journal mcp before serve ran again) still lists meetings.
func TestOpenReadOnlyOlderSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.db")
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.UpsertMeeting(MeetingRecord{ID: "2026-10-07-0900-zoom", Day: "2026-10-07", Start: at("2026-10-07", 9, 0), End: at("2026-10-07", 9, 30), App: "zoom.us"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Exec(`DROP TABLE imports`); err != nil {
		t.Fatal(err)
	}
	w.Close()
	r, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ms, err := r.MeetingsOn("2026-10-07")
	if err != nil || len(ms) != 1 || ms[0].Imported {
		t.Fatalf("MeetingsOn = %+v, %v", ms, err)
	}
	if err := r.SetKV("k", "v"); err == nil {
		t.Fatal("read-only store writable after the temp table")
	}
	w, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, ok, err := w.Import("x"); err != nil || ok {
		t.Fatalf("schema not applied on the next Open: %v %v", ok, err)
	}
}

// A read-only store opened before notes existed reads the notes a writer
// saves later, instead of its empty stand-in.
func TestOpenReadOnlySeesNotesAddedLater(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j.db")
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Exec(`DROP TABLE notes`); err != nil {
		t.Fatal(err)
	}
	w.Close()
	r, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if ns, err := r.NotesOn("2026-10-07"); err != nil || len(ns) != 0 {
		t.Fatalf("before: %+v %v", ns, err)
	}
	w, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	now := time.Now()
	id, err := w.AddNote(NoteRecord{Kind: NoteContext, Day: "2026-10-07", Text: "planning", Source: SourceMCP, CreatedAt: now, UpdatedAt: now}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ns, err := r.NotesOn("2026-10-07"); err != nil || len(ns) != 1 {
		t.Fatalf("after: %+v %v", ns, err)
	}
	if _, ok, err := r.Note(id); err != nil || !ok {
		t.Fatalf("Note(%d): %v %v", id, ok, err)
	}
}
