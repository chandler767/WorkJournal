package activity

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func evAt(t *testing.T, ts string) Event {
	t.Helper()
	v, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		t.Fatal(err)
	}
	return Event{TS: v, Type: "app"}
}

func offsetAt(z ZoneTimeline, day string, hh, mm int) int {
	t, err := z.Date(day, hh, mm, 0)
	if err != nil {
		panic(err)
	}
	_, off := t.Zone()
	return off
}

func TestZoneTimelineFollowsEventOffsets(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	// Morning in London, then a flight: clocks jump to New York at 15:10.
	z := NewZoneTimeline([]Event{
		evAt(t, "2026-10-12T08:30:00+01:00"),
		evAt(t, "2026-10-12T10:05:00+01:00"),
		evAt(t, "2026-10-12T15:10:00-04:00"),
		evAt(t, "2026-10-12T18:00:00-04:00"),
	}, ny)
	for _, c := range []struct {
		hh, mm, off int
	}{
		{0, 0, 3600},  // before the first event: the first event's offset
		{8, 30, 3600}, // at an event
		{10, 5, 3600}, // between London events
		{15, 9, 3600}, // just before the change
		{15, 10, -4 * 3600},
		{23, 0, -4 * 3600},
	} {
		if got := offsetAt(z, "2026-10-12", c.hh, c.mm); got != c.off {
			t.Errorf("%02d:%02d: offset %d, want %d", c.hh, c.mm, got, c.off)
		}
	}
	got, _ := z.Date("2026-10-12", 10, 5, 0)
	if want, _ := time.Parse(time.RFC3339, "2026-10-12T10:05:00+01:00"); !got.Equal(want) || got.Hour() != 10 || got.Minute() != 5 {
		t.Errorf("Date: got %v, want %v", got, want)
	}
}

func TestZoneTimelineFallsBackToLocal(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	z := NewZoneTimeline(nil, ny)
	got, err := z.Date("2026-10-12", 10, 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 12, 10, 5, 0, 0, ny); !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if _, err := z.Date("nope", 0, 0, 0); err == nil {
		t.Error("bad day: want error")
	}
}

func TestLoadZoneTimeline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "activity.jsonl")
	if err := os.WriteFile(path, []byte(`{"ts":"2026-10-12T10:00:00+01:00","type":"start"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := offsetAt(LoadZoneTimeline(path, time.UTC), "2026-10-12", 9, 0); got != 3600 {
		t.Errorf("offset %d, want 3600", got)
	}
	if got := LoadZoneTimeline(filepath.Join(dir, "nope"), time.UTC).At(time.Now()); got != time.UTC {
		t.Errorf("missing log: %v, want fallback", got)
	}
}
