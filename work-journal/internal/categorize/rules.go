// Package categorize assigns categories to activity segments from the
// user's config rules, before any segment is sent to Claude.
package categorize

import (
	"strings"

	"workjournal/internal/activity"
)

// BreakIdle is the category for idle time.
const BreakIdle = "Break/Idle"

// Meetings is the category for time inside a detected meeting.
const Meetings = "Meetings"

// Apply categorizes segs. Active and idle segments overlapping one of
// meetings are Meetings, whatever the app (a locked screen included); split
// segments at meeting boundaries first (activity.SplitAtMeetings) so only the
// time inside counts. Other idle segments are BreakIdle; paused, gap (asleep
// or recorder off) and excluded segments get "" and are not counted. Active segments match a rule
// whose key is a case-insensitive substring of the app or title, the longest
// key winning (ties: the lexically smaller key). Active segments with no
// match get "" and their indexes are returned in unmatched.
func Apply(segs []activity.Segment, meetings []activity.Meeting, rules map[string]string) (cats []string, unmatched []int) {
	cats = make([]string, len(segs))
	for i, s := range segs {
		if (s.Kind == activity.KindActive || s.Kind == activity.KindIdle) && InMeeting(s, meetings) {
			cats[i] = Meetings
			continue
		}
		switch s.Kind {
		case activity.KindIdle:
			cats[i] = BreakIdle
		case activity.KindActive:
			if c, ok := match(s, rules); ok {
				cats[i] = c
			} else {
				unmatched = append(unmatched, i)
			}
		}
	}
	return cats, unmatched
}

// InMeeting reports whether s overlaps any of meetings in time.
func InMeeting(s activity.Segment, meetings []activity.Meeting) bool {
	for _, m := range meetings {
		if s.Start.Before(m.End) && m.Start.Before(s.End) {
			return true
		}
	}
	return false
}

func match(s activity.Segment, rules map[string]string) (string, bool) {
	app, title := strings.ToLower(s.App), strings.ToLower(s.Title)
	best, cat := "", ""
	for k, c := range rules {
		lk := strings.ToLower(k)
		if lk == "" || !(strings.Contains(app, lk) || strings.Contains(title, lk)) {
			continue
		}
		if len(lk) > len(best) || (len(lk) == len(best) && lk < best) {
			best, cat = lk, c
		}
	}
	return cat, best != ""
}
