package activity

import (
	"sort"
	"time"
)

// Segment kinds.
const (
	KindActive   = "active"
	KindIdle     = "idle"
	KindExcluded = "excluded"
	KindPaused   = "paused"
	KindGap      = "gap"
)

// Segment is a contiguous span with one kind and app context.
type Segment struct {
	Start, End time.Time
	App, Title string
	Kind       string
}

type state struct {
	app, title string
	idle       bool
	paused     bool
	excluded   bool
	down       bool // stopped, asleep, or no recorder state yet
}

func (s state) segment(start, end time.Time) Segment {
	seg := Segment{Start: start, End: end}
	switch {
	case s.down:
		seg.Kind = KindGap
	case s.paused:
		seg.Kind = KindPaused
	case s.excluded:
		seg.Kind = KindExcluded
	case s.idle:
		seg.Kind = KindIdle
		seg.App, seg.Title = s.app, s.title
	default:
		seg.Kind = KindActive
		seg.App, seg.Title = s.app, s.title
	}
	return seg
}

func (s *state) apply(e Event) {
	switch e.Type {
	case "start":
		// A fresh recorder run has no pause/idle/exclusion in effect.
		s.down, s.idle, s.paused, s.excluded = false, false, false, false
	case "stop", "sleep":
		s.down = true
	case "wake":
		s.down = false
	case "app":
		s.app, s.title = e.App, e.Title
	case "idle":
		s.idle = true
	case "active":
		s.idle = false
	case "pause":
		s.paused = true
	case "resume":
		s.paused = false
	case "excluded":
		s.excluded = true
	case "included":
		s.excluded = false
	}
}

// Segments converts a day's events into segments clipped to [from,to). Events
// before from still establish state. Time with no known state is a gap. Paused,
// excluded and gap segments carry no app or title.
func Segments(events []Event, from, to time.Time) []Segment {
	evs := make([]Event, len(events))
	copy(evs, events)
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].TS.Before(evs[j].TS) })

	var out []Segment
	emit := func(s state, start, end time.Time) {
		if start.Before(from) {
			start = from
		}
		if end.After(to) {
			end = to
		}
		if !end.After(start) {
			return
		}
		seg := s.segment(start, end)
		if n := len(out); n > 0 {
			p := &out[n-1]
			if p.Kind == seg.Kind && p.App == seg.App && p.Title == seg.Title && p.End.Equal(seg.Start) {
				p.End = seg.End
				return
			}
		}
		out = append(out, seg)
	}

	st := state{down: true}
	cursor := from
	for _, e := range evs {
		if e.TS.After(cursor) {
			emit(st, cursor, e.TS)
			cursor = e.TS
		}
		st.apply(e)
	}
	emit(st, cursor, to)
	return out
}
