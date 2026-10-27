package store

import (
	"path/filepath"
	"testing"
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
