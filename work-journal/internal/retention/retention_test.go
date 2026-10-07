package retention

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"workjournal/internal/config"
	"workjournal/internal/store"
)

func setup(t *testing.T, days ...string) (config.Paths, *store.Store) {
	t.Helper()
	root := t.TempDir()
	p := config.Paths{Root: root}
	for _, d := range days {
		if err := os.MkdirAll(p.Audio(d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(filepath.Join(root, "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return p, st
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestSweepDeletesOldDays(t *testing.T) {
	p, st := setup(t, "2026-10-07", "2026-10-08", "2026-10-14")
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local)
	removed, err := Sweep(p, st, now, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(removed, []string{"2026-10-07"}) {
		t.Fatalf("removed = %v", removed)
	}
	if exists(p.RawDay("2026-10-07")) || !exists(p.RawDay("2026-10-08")) || !exists(p.RawDay("2026-10-14")) {
		t.Fatal("wrong dirs deleted")
	}
}

func TestSweepSkipsUnprocessedDay(t *testing.T) {
	p, st := setup(t, "2026-10-01", "2026-10-02", "2026-10-03")
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.UpsertHour(store.HourRecord{Day: "2026-10-01", Hour: 9, Status: store.StatusPending}))
	must(st.UpsertHour(store.HourRecord{Day: "2026-10-02", Hour: 9, Status: store.StatusTranscribed}))
	must(st.UpsertHour(store.HourRecord{Day: "2026-10-03", Hour: 9, Status: store.StatusDone}))
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local)
	removed, err := Sweep(p, st, now, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(removed, []string{"2026-10-03"}) {
		t.Fatalf("removed = %v", removed)
	}
	if !exists(p.RawDay("2026-10-01")) || !exists(p.RawDay("2026-10-02")) {
		t.Fatal("unprocessed day deleted")
	}
}

func TestSweepIgnoresMalformedAndMissing(t *testing.T) {
	p, st := setup(t, "2026-10-01")
	if err := os.MkdirAll(filepath.Join(filepath.Dir(p.RawDay("x")), "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local)
	removed, err := Sweep(p, st, now, 7)
	if err != nil || !reflect.DeepEqual(removed, []string{"2026-10-01"}) {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	if !exists(filepath.Join(filepath.Dir(p.RawDay("x")), "notes")) {
		t.Fatal("malformed dir removed")
	}
	// No raw dir at all.
	p2, st2 := setup(t)
	if r, err := Sweep(p2, st2, now, 7); err != nil || len(r) != 0 {
		t.Fatalf("missing raw: %v %v", r, err)
	}
}

func TestGuardThresholds(t *testing.T) {
	for _, c := range []struct {
		free float64
		want string
	}{{6, "normal"}, {5, "normal"}, {3, "screens_off"}, {2.5, "screens_off"}, {2, "audio_off"}} {
		if got := Guard(c.free, 5); got != c.want {
			t.Errorf("Guard(%v,5) = %s, want %s", c.free, got, c.want)
		}
	}
}

func TestDiskAction(t *testing.T) {
	cases := []struct {
		prev string
		free float64
		next string
		send bool
	}{
		{"", 10, "normal", true},
		{"normal", 10, "normal", false},
		{"normal", 3, "screens_off", true},
		{"screens_off", 3, "screens_off", false},
		{"screens_off", 1, "audio_off", true},
		{"audio_off", 10, "normal", true},
	}
	for _, c := range cases {
		next, send := DiskAction(c.prev, c.free, 5)
		if next != c.next || send != c.send {
			t.Errorf("DiskAction(%q,%v) = %s,%v want %s,%v", c.prev, c.free, next, send, c.next, c.send)
		}
	}
}

func TestFreeGB(t *testing.T) {
	g, err := FreeGB(t.TempDir())
	if err != nil || g <= 0 {
		t.Fatalf("FreeGB = %v, %v", g, err)
	}
}

func TestSweepSkipsDayWithUnrecordedRawHour(t *testing.T) {
	p, st := setup(t, "2026-10-01", "2026-10-02", "2026-10-03", "2026-10-04")
	touch := func(path string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 10-01: audio for hour 9 with no record -> keep.
	touch(filepath.Join(p.Audio("2026-10-01"), "0905-mic.m4a"))
	// 10-02: screenshot for hour 14 with no record -> keep.
	touch(filepath.Join(p.Screens("2026-10-02"), "142530.jpg"))
	// 10-03: audio for hour 9 recorded done -> delete.
	touch(filepath.Join(p.Audio("2026-10-03"), "090500-system.m4a"))
	if err := st.UpsertHour(store.HourRecord{Day: "2026-10-03", Hour: 9, Status: store.StatusDone}); err != nil {
		t.Fatal(err)
	}
	// 10-04: audio for hour 9 recorded done, screenshot hour 10 only needs_review -> delete.
	touch(filepath.Join(p.Audio("2026-10-04"), "0905-mic.m4a"))
	touch(filepath.Join(p.Screens("2026-10-04"), "100000.jpg"))
	for h, s := range map[int]string{9: store.StatusDone, 10: store.StatusNeedsReview} {
		if err := st.UpsertHour(store.HourRecord{Day: "2026-10-04", Hour: h, Status: s}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local)
	removed, err := Sweep(p, st, now, 7)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(removed, []string{"2026-10-03", "2026-10-04"}) {
		t.Fatalf("removed = %v", removed)
	}
}

func TestSweepSkipsDayWhenMediaDirUnreadable(t *testing.T) {
	p, st := setup(t, "2026-10-01", "2026-10-02")
	// audio becomes a regular file, so reading it as a directory fails.
	if err := os.RemoveAll(p.Audio("2026-10-01")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Audio("2026-10-01"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local)
	removed, err := Sweep(p, st, now, 7)
	if err == nil {
		t.Fatal("want error for unreadable media dir")
	}
	if !reflect.DeepEqual(removed, []string{"2026-10-02"}) {
		t.Fatalf("removed = %v", removed)
	}
	if !exists(p.RawDay("2026-10-01")) {
		t.Fatal("day with unreadable media deleted")
	}
}

func TestSweepSkipsDayWithUnparseableMediaName(t *testing.T) {
	p, st := setup(t, "2026-10-01", "2026-10-02")
	if err := os.WriteFile(filepath.Join(p.Audio("2026-10-01"), "weird.m4a"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p.Screens("2026-10-02"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Screens("2026-10-02"), "shot.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local)
	removed, err := Sweep(p, st, now, 7)
	if err != nil || len(removed) != 0 {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
}
