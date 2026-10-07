package activity

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"workjournal/internal/config"
)

// Meeting is a detected meeting.
type Meeting struct {
	ID         string
	Start, End time.Time
	App, Title string
}

// SpeechSpan is a span of detected speech on a track ("mic" or "system").
type SpeechSpan struct {
	Start, End time.Time
	Track      string
}

const mergeGap = 2 * time.Minute

type interval struct {
	start, end time.Time
	app, title string
	fromApp    bool
}

// DetectMeetings finds meetings from meeting-app/title active segments and from
// two-way speech (overlapping mic and system speech totaling at least
// MeetingMinTwoWayMinutes). Candidates less than 2 minutes apart are merged.
func DetectMeetings(segs []Segment, speech []SpeechSpan, c config.Config) []Meeting {
	var cands []interval
	for _, s := range segs {
		if (s.Kind == KindActive || s.Kind == KindIdle) && isMeetingContext(s.App, s.Title, c) {
			cands = append(cands, interval{start: s.Start, end: s.End, app: s.App, title: s.Title, fromApp: true})
		}
	}
	cands = append(cands, twoWayRegions(speech, time.Duration(c.MeetingMinTwoWayMinutes)*time.Minute)...)

	sort.SliceStable(cands, func(i, j int) bool { return cands[i].start.Before(cands[j].start) })
	var merged []interval
	for _, iv := range cands {
		if n := len(merged); n > 0 && iv.start.Sub(merged[n-1].end) < mergeGap {
			m := &merged[n-1]
			if iv.end.After(m.end) {
				m.end = iv.end
			}
			if iv.fromApp && !m.fromApp {
				m.app, m.title, m.fromApp = iv.app, iv.title, true
			}
			continue
		}
		merged = append(merged, iv)
	}

	out := make([]Meeting, 0, len(merged))
	for _, m := range merged {
		app, title := m.app, m.title
		if !m.fromApp {
			app, title = appAt(segs, m.start)
		}
		if app == "" {
			app = "call"
		}
		out = append(out, Meeting{
			ID:    fmt.Sprintf("%s-%s-%s", m.start.Format("2006-01-02"), m.start.Format("1504"), slug(app)),
			Start: m.start, End: m.end, App: app, Title: title,
		})
	}
	return out
}

func isMeetingContext(app, title string, c config.Config) bool {
	for _, a := range c.MeetingApps {
		if a != "" && strings.EqualFold(app, a) {
			return true
		}
	}
	lt := strings.ToLower(title)
	for _, k := range c.MeetingTitleKeywords {
		if k != "" && strings.Contains(lt, strings.ToLower(k)) {
			return true
		}
	}
	return false
}

// appAt returns the app/title of the active or idle segment containing t.
func appAt(segs []Segment, t time.Time) (string, string) {
	for _, s := range segs {
		if (s.Kind == KindActive || s.Kind == KindIdle) && !t.Before(s.Start) && t.Before(s.End) {
			return s.App, s.Title
		}
	}
	return "", ""
}

func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return b.String()
}

// twoWayRegions returns regions of overlapping mic+system speech whose total
// overlap is at least min. Overlaps less than mergeGap apart share a region.
func twoWayRegions(speech []SpeechSpan, min time.Duration) []interval {
	var mic, sys []interval
	for _, s := range speech {
		if !s.End.After(s.Start) {
			continue
		}
		iv := interval{start: s.Start, end: s.End}
		switch s.Track {
		case "mic":
			mic = append(mic, iv)
		case "system":
			sys = append(sys, iv)
		}
	}
	var overlaps []interval
	for _, a := range mic {
		for _, b := range sys {
			s, e := laterOf(a.start, b.start), earlierOf(a.end, b.end)
			if e.After(s) {
				overlaps = append(overlaps, interval{start: s, end: e})
			}
		}
	}
	sort.Slice(overlaps, func(i, j int) bool { return overlaps[i].start.Before(overlaps[j].start) })

	var out []interval
	var cur interval
	var total time.Duration
	started := false
	flush := func() {
		if started && total >= min {
			out = append(out, cur)
		}
	}
	for _, o := range overlaps {
		if started && o.start.Sub(cur.end) < mergeGap {
			if o.end.After(cur.end) {
				// count only the non-double-counted part
				from := laterOf(o.start, cur.end)
				total += o.end.Sub(from)
				cur.end = o.end
			}
			continue
		}
		flush()
		cur, total, started = o, o.end.Sub(o.start), true
	}
	flush()
	return out
}

func laterOf(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func earlierOf(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
