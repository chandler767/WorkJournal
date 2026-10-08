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
	cats, unmatched := Apply(segs, nil, rules)
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
	cats, unmatched := Apply(segs, nil, nil)
	if !reflect.DeepEqual(cats, []string{""}) || !reflect.DeepEqual(unmatched, []int{0}) {
		t.Fatalf("cats=%q unmatched=%v", cats, unmatched)
	}
}

func TestApplyTieBreakIsDeterministic(t *testing.T) {
	t0 := time.Date(2026, 10, 7, 9, 0, 0, 0, time.Local)
	segs := []activity.Segment{{Start: t0, End: t0.Add(time.Minute), Kind: activity.KindActive, App: "abc", Title: "xyz"}}
	rules := map[string]string{"abc": "A", "xyz": "X"}
	for i := 0; i < 20; i++ {
		if cats, _ := Apply(segs, nil, rules); cats[0] != "A" {
			t.Fatalf("tie resolved to %q, want the lexically first key's category", cats[0])
		}
	}
}

func TestApplyMeetingTimeIsMeetings(t *testing.T) {
	t0 := time.Date(2026, 10, 7, 14, 0, 0, 0, time.Local)
	seg := func(m1, m2 int, kind, app string) activity.Segment {
		return activity.Segment{Start: t0.Add(time.Duration(m1) * time.Minute), End: t0.Add(time.Duration(m2) * time.Minute), Kind: kind, App: app}
	}
	segs := []activity.Segment{
		seg(0, 10, activity.KindIdle, "loginwindow"),    // 0: before the meeting: Break/Idle
		seg(10, 20, activity.KindIdle, "loginwindow"),   // 1: idle in the meeting
		seg(20, 30, activity.KindActive, "loginwindow"), // 2: locked screen in the meeting
		seg(30, 35, activity.KindActive, "Figma"),       // 3: rule-matched app in the meeting
		seg(35, 40, activity.KindPaused, ""),            // 4: paused stays uncounted
		seg(40, 45, activity.KindExcluded, ""),          // 5: excluded stays uncounted
		seg(45, 50, activity.KindGap, ""),               // 6: gap stays uncounted
		seg(50, 60, activity.KindActive, "loginwindow"), // 7: after the meeting: unmatched
	}
	ms := []activity.Meeting{{Start: t0.Add(10 * time.Minute), End: t0.Add(50 * time.Minute), App: activity.InPersonApp}}
	cats, unmatched := Apply(segs, ms, map[string]string{"figma": "Design"})
	want := []string{"Break/Idle", "Meetings", "Meetings", "Meetings", "", "", "", ""}
	if !reflect.DeepEqual(cats, want) {
		t.Fatalf("cats = %q\nwant  %q", cats, want)
	}
	if !reflect.DeepEqual(unmatched, []int{7}) {
		t.Fatalf("unmatched = %v, want [7]", unmatched)
	}
	for i, s := range segs {
		if got, want := InMeeting(s, ms), i >= 1 && i <= 6; got != want {
			t.Errorf("InMeeting(seg %d) = %v", i, got)
		}
	}
}
