package activity

import (
	"fmt"
	"slices"
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

// InPersonApp is the App of a meeting detected from mic-only conversation.
const InPersonApp = "In person"

// inPersonWindow is the rolling window over which mic speech density is measured.
const inPersonWindow = 5 * time.Minute

type interval struct {
	start, end time.Time
	app, title string
	fromApp    bool
}

// DetectMeetings finds meetings from meeting-app/title active segments and from
// two-way speech (overlapping mic and system speech totaling at least
// MeetingMinTwoWayMinutes). Candidates less than 2 minutes apart are merged.
// It then adds in-person meetings (App InPersonApp) from mic-only
// conversation, see inPersonRegions, except where one overlaps a meeting
// already found. Meetings are returned in start order.
func DetectMeetings(segs []Segment, speech []SpeechSpan, c config.Config) []Meeting {
	out := appMeetings(segs, speech, c)
	n := len(out)
	for _, r := range inPersonRegions(speech, c) {
		if slices.ContainsFunc(out[:n], func(m Meeting) bool { return m.Start.Before(r.end) && r.start.Before(m.End) }) {
			continue
		}
		out = append(out, Meeting{
			ID:    fmt.Sprintf("%s-%s-%s", r.start.Format("2006-01-02"), r.start.Format("1504"), slug(InPersonApp)),
			Start: r.start, End: r.end, App: InPersonApp,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

// inPersonRegions finds conversations from mic speech alone: every 5-minute
// window starting at a mic utterance in which mic speech covers at least
// InPersonSpeechRatio marks the span from that utterance to the window's last
// speech. Marked spans less than 2 minutes apart merge, and regions shorter
// than InPersonMinMinutes are dropped. Screen state plays no part: a locked
// or idle screen does not prevent detection.
func inPersonRegions(speech []SpeechSpan, c config.Config) []interval {
	var mic []interval
	for _, s := range speech {
		if s.Track == "mic" && s.End.After(s.Start) {
			mic = append(mic, interval{start: s.Start, end: s.End})
		}
	}
	mic = union(mic)
	need := time.Duration(c.InPersonSpeechRatio * float64(inPersonWindow))
	var marked []interval
	for i, a := range mic {
		wEnd := a.start.Add(inPersonWindow)
		var covered time.Duration
		last := a.start
		for _, b := range mic[i:] {
			if !b.start.Before(wEnd) {
				break
			}
			e := earlierOf(b.end, wEnd)
			covered += e.Sub(b.start)
			last = e
		}
		if covered >= need {
			marked = append(marked, interval{start: a.start, end: last})
		}
	}
	var out []interval
	for _, m := range marked {
		if n := len(out); n > 0 && m.start.Sub(out[n-1].end) < mergeGap {
			if m.end.After(out[n-1].end) {
				out[n-1].end = m.end
			}
			continue
		}
		out = append(out, m)
	}
	min := time.Duration(c.InPersonMinMinutes) * time.Minute
	return slices.DeleteFunc(out, func(r interval) bool { return r.end.Sub(r.start) < min })
}

// union sorts ivs and merges overlapping or touching intervals.
func union(ivs []interval) []interval {
	sort.Slice(ivs, func(i, j int) bool { return ivs[i].start.Before(ivs[j].start) })
	var out []interval
	for _, iv := range ivs {
		if n := len(out); n > 0 && !iv.start.After(out[n-1].end) {
			if iv.end.After(out[n-1].end) {
				out[n-1].end = iv.end
			}
			continue
		}
		out = append(out, iv)
	}
	return out
}

// appMeetings finds the meeting-app/title and two-way speech meetings.
func appMeetings(segs []Segment, speech []SpeechSpan, c config.Config) []Meeting {
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
