package process

import (
	"fmt"
	"math"
	"time"

	"workjournal/internal/store"
	"workjournal/internal/wallclock"
)

// What the dashboard and the journal MCP server show of derived data, kept
// in one place so both apply the same rules.

// RemovedNotices describes the spans the privacy filter removed, without
// reason or content: "4 min of non-work audio removed (13:12–13:16)". Spans
// less than a minute apart read as one.
func RemovedNotices(spans []RemovedSpan) []string {
	var merged []RemovedSpan
	for _, r := range spans {
		if n := len(merged); n > 0 && r.Start.Sub(merged[n-1].End) < time.Minute {
			if r.End.After(merged[n-1].End) {
				merged[n-1].End = r.End
			}
			continue
		}
		merged = append(merged, r)
	}
	var out []string
	for _, r := range merged {
		span := wallclock.Span(r.Start, r.End)
		if d := r.End.Sub(r.Start); d < time.Minute {
			out = append(out, "Under a minute of non-work audio removed ("+span+")")
		} else {
			out = append(out, fmt.Sprintf("%d min of non-work audio removed (%s)", int(math.Round(d.Minutes())), span))
		}
	}
	return out
}

// SpeakerDisplay is the one name-display rule for meeting speakers: the
// name when confirmed or confident enough, else the label with the guess.
func SpeakerDisplay(speaker string, p *store.ParticipantRecord, threshold float64) string {
	switch {
	case p == nil || p.Name == "":
		return speaker
	case p.Confirmed || p.Confidence >= threshold:
		return p.Name
	default:
		return speaker + " (" + p.Name + "?)"
	}
}

// HourStarts returns the clock hours (h:00 in loc) that [start, end)
// touches, starting with the hour holding start. For a recorded span pass
// start.Location(): derived hours are keyed by the recorded wall clock.
func HourStarts(start, end time.Time, loc *time.Location) []time.Time {
	s := start.In(loc)
	var out []time.Time
	for h := 0; ; h++ {
		t := time.Date(s.Year(), s.Month(), s.Day(), s.Hour()+h, 0, 0, 0, loc)
		if !t.Before(end) && h > 0 {
			return out
		}
		out = append(out, t)
	}
}
