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

func TestHourSpansPartitionAFlight(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	parse := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	// London until 16:30 BST (15:30Z), New York from 15:10 EDT (19:10Z).
	z := NewZoneTimeline([]Event{
		evAt(t, "2026-10-12T09:00:00+01:00"),
		evAt(t, "2026-10-12T16:30:00+01:00"),
		evAt(t, "2026-10-12T15:10:00-04:00"),
	}, ny)
	spans := func(h int) []Span {
		sp, err := z.HourSpans("2026-10-12", h)
		if err != nil {
			t.Fatal(err)
		}
		return sp
	}
	same := func(h int, want ...string) {
		t.Helper()
		got := spans(h)
		if len(got) != len(want)/2 {
			t.Fatalf("hour %d spans %v, want %v", h, got, want)
		}
		for i, sp := range got {
			if !sp.Start.Equal(parse(want[2*i])) || !sp.End.Equal(parse(want[2*i+1])) || sp.Start.Hour() != h {
				t.Errorf("hour %d span %d = %v–%v, want %s–%s", h, i, sp.Start, sp.End, want[2*i], want[2*i+1])
			}
		}
	}
	same(15, "2026-10-12T15:00:00+01:00", "2026-10-12T16:00:00+01:00", "2026-10-12T15:10:00-04:00", "2026-10-12T16:00:00-04:00")
	same(16, "2026-10-12T16:00:00+01:00", "2026-10-12T17:00:00+01:00", "2026-10-12T16:00:00-04:00", "2026-10-12T17:00:00-04:00")
	// The flight's last minutes in BST, then New York's 20:00.
	same(20, "2026-10-12T20:00:00+01:00", "2026-10-12T20:10:00+01:00", "2026-10-12T20:00:00-04:00", "2026-10-12T21:00:00-04:00")
	same(12, "2026-10-12T12:00:00+01:00", "2026-10-12T13:00:00+01:00") // New York's 12:00 never happened
	// Every minute of the day is in exactly one hour.
	for m := parse("2026-10-11T23:00:00Z"); m.Before(parse("2026-10-13T04:00:00Z")); m = m.Add(time.Minute) {
		n := 0
		for h := 0; h < 24; h++ {
			for _, sp := range spans(h) {
				if !m.Before(sp.Start) && m.Before(sp.End) {
					n++
				}
			}
		}
		wall := m.In(time.FixedZone("", 3600))
		if !m.Before(parse("2026-10-12T19:10:00Z")) {
			wall = m.In(time.FixedZone("", -4*3600))
		}
		if want := map[bool]int{true: 1, false: 0}[wall.Format("2006-01-02") == "2026-10-12"]; n != want {
			t.Fatalf("%v (wall %v) is in %d hours, want %d", m, wall, n, want)
		}
	}
}

// Spring forward overnight: the last event before sleep is EST, the wake at
// 08:10 is EDT; hour 8 is EDT's, from the wake.
func TestHourSpansAfterSpringForward(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	z := NewZoneTimeline([]Event{
		evAt(t, "2027-03-14T00:30:00-05:00"),
		evAt(t, "2027-03-14T08:10:00-04:00"),
	}, ny)
	sp, err := z.HourSpans("2027-03-14", 8)
	from, _ := time.Parse(time.RFC3339, "2027-03-14T08:10:00-04:00")
	if err != nil || len(sp) != 1 || !sp[0].Start.Equal(from) || !sp[0].End.Equal(from.Add(50*time.Minute)) {
		t.Errorf("hour 8 = %v (%v), want 08:10–09:00 EDT", sp, err)
	}
	// No events: the fallback zone, DST-aware.
	sp, _ = NewZoneTimeline(nil, ny).HourSpans("2027-03-14", 3)
	if len(sp) != 1 || sp[0].Start.Hour() != 3 || sp[0].End.Sub(sp[0].Start) != time.Hour {
		t.Errorf("fallback hour 3 = %v", sp)
	}
}

// A zone event alone (no app change) re-anchors the clock from its instant.
func TestZoneEventReanchors(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	evs := []Event{evAt(t, "2026-10-07T09:10:00-05:00"), evAt(t, "2026-10-07T10:20:00-04:00")}
	evs[1].Type = "zone"
	z := NewZoneTimeline(evs, ny)
	if got := offsetAt(z, "2026-10-07", 10, 30); got != -4*3600 {
		t.Errorf("10:30 offset %d, want -4h", got)
	}
	sp, _ := z.HourSpans("2026-10-07", 10)
	from, _ := time.Parse(time.RFC3339, "2026-10-07T10:20:00-04:00")
	if len(sp) != 1 || !sp[0].Start.Equal(from) {
		t.Errorf("hour 10 = %v, want from 10:20 EDT", sp)
	}
	// Segments take no state from it.
	evs = append([]Event{{TS: evs[0].TS, Type: "start"}, {TS: evs[0].TS, Type: "app", App: "Arc"}}, evs[1])
	segs := Segments(evs, evs[0].TS, from.Add(time.Hour))
	if len(segs) != 1 || segs[0].App != "Arc" || segs[0].Kind != KindActive {
		t.Errorf("segments = %+v, want one Arc segment", segs)
	}
}

// Between two events of different offsets, a wall time takes the offset
// that puts it between them in time: 10:30 after 09:10 CDT and before
// 10:45 EDT can only be 10:30 EDT.
func TestAtPicksTheConsistentOffset(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	z := NewZoneTimeline([]Event{evAt(t, "2026-10-07T09:10:00-05:00"), evAt(t, "2026-10-07T10:45:00-04:00")}, ny)
	if got := offsetAt(z, "2026-10-07", 10, 30); got != -4*3600 {
		t.Errorf("10:30 offset %d, want -4h", got)
	}
	if got := offsetAt(z, "2026-10-07", 9, 30); got != -5*3600 {
		t.Errorf("09:30 offset %d, want -5h (both fit; the earlier clock)", got)
	}
}
