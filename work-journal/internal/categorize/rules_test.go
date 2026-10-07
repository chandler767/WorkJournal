package categorize

import (
	"reflect"
	"testing"
	"time"

	"workjournal/internal/activity"
)

func TestApplyRules(t *testing.T) {
	t0 := time.Date(2026, 10, 7, 9, 0, 0, 0, time.Local)
	seg := func(kind, app, title string) activity.Segment {
		return activity.Segment{Start: t0, End: t0.Add(time.Minute), Kind: kind, App: app, Title: title}
	}
	segs := []activity.Segment{
		seg(activity.KindActive, "Figma", "Checkout flow"),           // 0: app rule
		seg(activity.KindIdle, "Figma", "Checkout flow"),             // 1: idle
		seg(activity.KindActive, "Code", "parser.go"),                // 2: unmatched
		seg(activity.KindPaused, "", ""),                             // 3: paused, not counted
		seg(activity.KindGap, "", ""),                                // 4: gap (asleep/off), not counted
		seg(activity.KindExcluded, "", ""),                           // 5: excluded, not counted
		seg(activity.KindActive, "Google Chrome", "JIRA-12 · Jira"),  // 6: title rule, case-insensitive
		seg(activity.KindActive, "Slack", "Huddle with design team"), // 7: longest key wins
	}
	rules := map[string]string{
		"figma":       "Design",
		"jira":        "Admin",
		"slack":       "Communication",
		"huddle with": "Meetings",
		"":            "Ignored",
	}
	cats, unmatched := Apply(segs, rules)
	want := []string{"Design", "Break/Idle", "", "", "", "", "Admin", "Meetings"}
	if !reflect.DeepEqual(cats, want) {
		t.Fatalf("cats = %q\nwant  %q", cats, want)
	}
	if !reflect.DeepEqual(unmatched, []int{2}) {
		t.Fatalf("unmatched = %v, want [2]", unmatched)
	}
}

func TestApplyNoRules(t *testing.T) {
	t0 := time.Date(2026, 10, 7, 9, 0, 0, 0, time.Local)
	segs := []activity.Segment{{Start: t0, End: t0.Add(time.Minute), Kind: activity.KindActive, App: "Figma"}}
	cats, unmatched := Apply(segs, nil)
	if !reflect.DeepEqual(cats, []string{""}) || !reflect.DeepEqual(unmatched, []int{0}) {
		t.Fatalf("cats=%q unmatched=%v", cats, unmatched)
	}
}

func TestApplyTieBreakIsDeterministic(t *testing.T) {
	t0 := time.Date(2026, 10, 7, 9, 0, 0, 0, time.Local)
	segs := []activity.Segment{{Start: t0, End: t0.Add(time.Minute), Kind: activity.KindActive, App: "abc", Title: "xyz"}}
	rules := map[string]string{"abc": "A", "xyz": "X"}
	for i := 0; i < 20; i++ {
		if cats, _ := Apply(segs, rules); cats[0] != "A" {
			t.Fatalf("tie resolved to %q, want the lexically first key's category", cats[0])
		}
	}
}
