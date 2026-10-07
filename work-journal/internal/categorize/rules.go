// Package categorize assigns categories to activity segments from the
// user's config rules, before any segment is sent to Claude.
package categorize

import (
	"strings"

	"workjournal/internal/activity"
)

// BreakIdle is the category for idle, paused and gap time.
const BreakIdle = "Break/Idle"

// Apply categorizes segs. Idle, paused and gap segments are BreakIdle;
// excluded segments get "" and are not counted. Active segments match a rule
// whose key is a case-insensitive substring of the app or title, the longest
// key winning (ties: the lexically smaller key). Active segments with no
// match get "" and their indexes are returned in unmatched.
func Apply(segs []activity.Segment, rules map[string]string) (cats []string, unmatched []int) {
	cats = make([]string, len(segs))
	for i, s := range segs {
		switch s.Kind {
		case activity.KindIdle, activity.KindPaused, activity.KindGap:
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
