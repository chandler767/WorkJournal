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
	fallback *time.Location
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

// HourBounds returns the absolute window of the recorded wall-clock hour
// hour of day. The hour takes the offset of every event of the timeline and
// every other recorded time in walls (raw file times, already in their
// zones) whose wall clock lies inside it, so an hour holding a zone change
// (a flight, a DST change after sleep) covers both sides and drops nothing:
// it runs from the earliest h:00 to the latest h+1:00 among those offsets.
// An hour with none of them uses the offset in force at h:00. start and end
// read h:00 and h+1:00 on their own clocks.
func (z ZoneTimeline) HourBounds(day string, hour int, walls []time.Time) (time.Time, time.Time, error) {
	h0, err := z.Date(day, hour, 0, 0)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	from := time.Date(h0.Year(), h0.Month(), h0.Day(), hour, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	inHour := func(w time.Time) bool { return !w.Before(from) && w.Before(to) }
	var offs []int
	for _, p := range z.points {
		if inHour(p.wall) {
			offs = append(offs, p.off)
		}
	}
	for _, w := range walls {
		if inHour(asWall(w)) {
			_, off := w.Zone()
			offs = append(offs, off)
		}
	}
	if len(offs) == 0 {
		return h0, h0.Add(time.Hour), nil
	}
	var start, end time.Time
	for i, off := range offs {
		loc := time.FixedZone("", off)
		s := time.Date(from.Year(), from.Month(), from.Day(), hour, 0, 0, 0, loc)
		e := s.Add(time.Hour)
		if i == 0 || s.Before(start) {
			start = s
		}
		if i == 0 || e.After(end) {
			end = e
		}
	}
	return start, end, nil
}
