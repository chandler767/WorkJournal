package activity

import (
	"fmt"
	"sort"
	"time"
)

// ZoneTimeline gives the UTC offset in force at each wall-clock time of a
// recorded day. Raw file names (audio chunks HHMM…, screenshots HHMMSS) and
// hour keys are the wall clock where they were recorded, which is not the
// Mac's current zone after travel; the recorder's activity events carry that
// offset (RFC 3339 local time), so the offset in force at a wall time is the
// one of the latest event at or before it, the first event's before any.
type ZoneTimeline struct {
	points   []zonePoint // by wall clock
	runs     []zoneRun   // by instant: where the offset in force changes
	fallback *time.Location
}

// zoneRun is the offset in force from an instant until the next run.
type zoneRun struct {
	from time.Time
	off  int
}

type zonePoint struct {
	wall time.Time // wall clock fields, as UTC
	off  int
}

// NewZoneTimeline builds the timeline of one day's events; with no events
// every time is in fallback (normally time.Local).
func NewZoneTimeline(events []Event, fallback *time.Location) ZoneTimeline {
	z := ZoneTimeline{fallback: fallback}
	for _, e := range events {
		_, off := e.TS.Zone()
		z.points = append(z.points, zonePoint{wall: asWall(e.TS), off: off})
	}
	sort.SliceStable(z.points, func(i, j int) bool { return z.points[i].wall.Before(z.points[j].wall) })
	byInstant := append([]Event(nil), events...)
	sort.SliceStable(byInstant, func(i, j int) bool { return byInstant[i].TS.Before(byInstant[j].TS) })
	for _, e := range byInstant {
		if _, off := e.TS.Zone(); len(z.runs) == 0 || z.runs[len(z.runs)-1].off != off {
			z.runs = append(z.runs, zoneRun{from: e.TS, off: off})
		}
	}
	return z
}

func asWall(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.UTC)
}

// At returns the location in force at the wall clock of wall (its
// location is ignored): a fixed zone, or the fallback with no events.
func (z ZoneTimeline) At(wall time.Time) *time.Location {
	if len(z.points) == 0 {
		return z.fallback
	}
	w := asWall(wall)
	i := sort.Search(len(z.points), func(i int) bool { return z.points[i].wall.After(w) })
	return time.FixedZone("", z.points[max(i-1, 0)].off)
}

// Date is the instant whose recorded wall clock is day ("YYYY-MM-DD")
// hh:mm:ss.
func (z ZoneTimeline) Date(day string, hh, mm, ss int) (time.Time, error) {
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		return time.Time{}, fmt.Errorf("bad day %q: %w", day, err)
	}
	w := time.Date(d.Year(), d.Month(), d.Day(), hh, mm, ss, 0, time.UTC)
	loc := z.At(w)
	return time.Date(w.Year(), w.Month(), w.Day(), hh, mm, ss, 0, loc), nil
}

// LoadZoneTimeline builds the timeline of the day's activity log at path;
// a missing or unreadable log gives fallback for every time.
func LoadZoneTimeline(path string, fallback *time.Location) ZoneTimeline {
	evs, _, _ := ReadEvents(path)
	return NewZoneTimeline(evs, fallback)
}

// Span is an absolute interval [Start, End).
type Span struct{ Start, End time.Time }

// HourSpans returns the instants of the recorded wall-clock hour hour of
// day: those whose wall clock, read in the zone in force at that instant,
// is day hour:mm. The zone in force at an instant is the offset of the
// latest event at or before it (the first event's before any; fallback with
// no events). So every instant belongs to exactly one hour: the hour that
// holds a flight's zone change is the old clock's piece plus the new
// clock's, and the flight itself is filed under the old clock's later
// hours. Spans are ordered and disjoint, each starting and ending on the
// clock of its piece (so a span's Start reads hour h).
func (z ZoneTimeline) HourSpans(day string, hour int) ([]Span, error) {
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		return nil, fmt.Errorf("bad day %q: %w", day, err)
	}
	if len(z.runs) == 0 {
		s := time.Date(d.Year(), d.Month(), d.Day(), hour, 0, 0, 0, z.fallback)
		e := time.Date(d.Year(), d.Month(), d.Day(), hour+1, 0, 0, 0, z.fallback)
		return []Span{{s, e}}, nil
	}
	var out []Span
	for i, r := range z.runs {
		loc := time.FixedZone("", r.off)
		s := time.Date(d.Year(), d.Month(), d.Day(), hour, 0, 0, 0, loc)
		e := s.Add(time.Hour)
		if i > 0 && s.Before(r.from) {
			s = r.from.In(loc)
		}
		if i+1 < len(z.runs) && z.runs[i+1].from.Before(e) {
			e = z.runs[i+1].from.In(loc)
		}
		if !s.Before(e) {
			continue
		}
		if n := len(out); n > 0 && !out[n-1].End.Before(s) {
			if e.After(out[n-1].End) {
				out[n-1].End = e
			}
			continue
		}
		out = append(out, Span{s, e})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}
