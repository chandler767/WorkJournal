package web

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"workjournal/internal/activity"
	"workjournal/internal/config"
	"workjournal/internal/media"
	"workjournal/internal/process"
	"workjournal/internal/store"
)

const seedDay = "2026-10-07"

func seedTime(h, m int) time.Time {
	return time.Date(2026, 10, 7, h, m, 0, 0, time.Local)
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Seed fills st and p with one day of data: hour 09 done (with transcript,
// screenshots and audio), hour 10 needs_review, three segments, one
// meeting and a journal file. Shared by the web tests.
func Seed(t *testing.T, st *store.Store, p config.Paths) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	now := seedTime(11, 0)
	must(st.UpsertHour(store.HourRecord{Day: seedDay, Hour: 9, Status: store.StatusDone, UpdatedAt: now,
		SummaryJSON: `{"narrative":"Rewrote the scheduler retry loop and reviewed PR 42 before standup.","timeline":[{"start":"09:00","end":"09:40","app":"Code","activity":"Edited scheduler.go retry loop"},{"start":"09:40","end":"09:50","app":"Slack","activity":"Answered questions in #general"}],"notes":["Refactored the scheduler","Reviewed PR 42"],"main_focus":"Scheduler work","action_items":[{"text":"Send release notes","owner":"Priya"}]}`}))
	must(st.UpsertHour(store.HourRecord{Day: seedDay, Hour: 10, Status: store.StatusNeedsReview, UpdatedAt: now}))
	must(st.ReplaceSegments(seedDay, 9, []store.SegmentRecord{
		{Day: seedDay, Hour: 9, Start: seedTime(9, 0), End: seedTime(9, 40), App: "Code", Title: "scheduler.go", Kind: "app", Category: "Deep work", Confidence: 0.9, Source: "skill"},
		{Day: seedDay, Hour: 9, Start: seedTime(9, 40), End: seedTime(9, 50), App: "Slack", Title: "general", Kind: "app", Category: "Communication", Confidence: 0.8, Source: "rule"},
	}))
	must(st.ReplaceSegments(seedDay, 10, []store.SegmentRecord{
		{Day: seedDay, Hour: 10, Start: seedTime(10, 0), End: seedTime(10, 30), App: "Safari", Title: "docs", Kind: "app", Category: "Browsing/Research", Confidence: 0.6, Source: "skill"},
	}))
	must(st.UpsertMeeting(store.MeetingRecord{ID: "2026-10-07-0910-zoom", Day: seedDay, Start: seedTime(9, 10), End: seedTime(9, 30), App: "zoom.us", Title: "Standup"}))

	doc := process.HourDoc{Day: seedDay, Hour: 9, Status: store.StatusDone,
		Segments: []activity.Segment{},
		Transcript: []media.Utterance{
			{Start: seedTime(9, 10), End: seedTime(9, 11), Track: "system", Speaker: "Priya", Text: "Let us ship on Friday"},
			{Start: seedTime(9, 12), End: seedTime(9, 13), Track: "mic", Speaker: "Me", Text: "Sounds good to me"},
		},
		Screens: []media.ScreenText{{Time: seedTime(9, 5), File: "090500.jpg", Text: "code"}, {Time: seedTime(9, 20), File: "092000.jpg", Text: "slack"}},
	}
	must(doc.Save(p.DerivedHour(seedDay, 9)))

	writeFile(t, p.DailyJournal(seedDay), "# Today\nShipped <b>scheduler</b> work.\n")
	writeFile(t, filepath.Join(p.Audio(seedDay), "0900-system.m4a"), "audio")
	writeFile(t, filepath.Join(p.Audio(seedDay), "0900-mic.m4a"), "audio")
	writeFile(t, filepath.Join(p.Screens(seedDay), "090500.jpg"), "jpg")
	writeFile(t, filepath.Join(p.Screens(seedDay), "092000.jpg"), "jpg")
}
