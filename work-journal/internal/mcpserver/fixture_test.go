package mcpserver

import (
	"fmt"
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

const (
	fxDay     = "2026-10-07"
	fxNextDay = "2026-10-08"
	fxMeeting = "2026-10-07-0910-zoom"
	fxMeet2   = "2026-10-08-1400-zoom"
	fxAddr    = "127.0.0.1:4747"
)

// Strings that exist only outside what the dashboard shows (raw media,
// cache/, voices/, the privacy-decision cache, withheld speech, removed
// speech): no tool output may ever contain them.
var secrets = []string{"CACHESECRET", "PRIVACYSECRET", "VOICESECRET", "WITHHELDSECRET", "REMOVEDSECRET", "bystander", "personal_non_work", "0.12345"}

func fxTime(day string, h, m int) time.Time {
	d, err := time.ParseInLocation("2006-01-02", day, time.Local)
	if err != nil {
		panic(err)
	}
	return d.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
}

func fxWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// seedFixture writes a journal data dir: 2026-10-07 with a done hour 09
// (summary, transcript, screens, a removed span), a needs_review hour 10
// whose speech is withheld, segments, the Standup meeting, a daily and a
// weekly journal; 2026-10-08 with a meeting with Alice. Raw media, cache,
// voices and privacy files hold the secrets.
func seedFixture(t *testing.T, root string) config.Paths {
	t.Helper()
	p := config.Paths{Root: root}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	now := fxTime(fxDay, 12, 0)
	must(st.UpsertHour(store.HourRecord{Day: fxDay, Hour: 9, Status: store.StatusDone, UpdatedAt: now,
		SummaryJSON: `{"narrative":"Rewrote the scheduler retry loop, then joined the Standup where the team agreed to ship on Friday.","timeline":[{"start":"09:00","end":"09:10","app":"Code","activity":"Edited scheduler.go retry loop"},{"start":"09:10","end":"09:30","app":"zoom.us","activity":"Standup meeting about the release"},{"start":"09:30","end":"09:50","app":"Slack","activity":"Answered questions in #general"}],"notes":["Refactored the scheduler retry loop","Reviewed PR 42"],"main_focus":"Scheduler work","action_items":[{"text":"Send release notes","owner":"Priya"}]}`}))
	must(st.UpsertHour(store.HourRecord{Day: fxDay, Hour: 10, Status: store.StatusNeedsReview, UpdatedAt: now}))
	must(st.UpsertHour(store.HourRecord{Day: fxDay, Hour: 11, Status: store.StatusPending, UpdatedAt: now}))
	must(st.ReplaceSegments(fxDay, 9, []store.SegmentRecord{
		{Day: fxDay, Hour: 9, Start: fxTime(fxDay, 9, 0), End: fxTime(fxDay, 9, 10), App: "Code", Title: "scheduler.go", Kind: "active", Category: "Deep work", Source: "skill"},
		{Day: fxDay, Hour: 9, Start: fxTime(fxDay, 9, 10), End: fxTime(fxDay, 9, 30), App: "zoom.us", Title: "Standup", Kind: "active", Category: "Meetings", Source: "rule"},
		{Day: fxDay, Hour: 9, Start: fxTime(fxDay, 9, 30), End: fxTime(fxDay, 9, 50), App: "Slack", Title: "general", Kind: "active", Category: "Communication", Source: "rule"},
	}))
	must(st.ReplaceSegments(fxDay, 10, []store.SegmentRecord{
		{Day: fxDay, Hour: 10, Start: fxTime(fxDay, 10, 0), End: fxTime(fxDay, 10, 30), App: "Code", Title: "retry.go", Kind: "active", Category: "Deep work", Source: "skill"},
	}))
	must(st.ReplaceSegments(fxNextDay, 14, []store.SegmentRecord{
		{Day: fxNextDay, Hour: 14, Start: fxTime(fxNextDay, 14, 0), End: fxTime(fxNextDay, 14, 45), App: "zoom.us", Title: "Planning", Kind: "active", Category: "Meetings", Source: "rule"},
	}))
	must(st.UpsertMeeting(store.MeetingRecord{ID: fxMeeting, Day: fxDay, Start: fxTime(fxDay, 9, 10), End: fxTime(fxDay, 9, 30), App: "zoom.us", Title: "Standup",
		SummaryJSON: `{"participants":[{"speaker":"Speaker 1","name":"Priya","confidence":0.9}],"summary":"The team agreed to ship the release on Friday. Priya owns the notes.","decisions":["Ship the release on Friday"],"action_items":[{"text":"Send release notes","owner":"Priya"}]}`}))
	must(st.SetParticipant(store.ParticipantRecord{MeetingID: fxMeeting, Speaker: "Speaker 1", Name: "Priya", Confidence: 1, Confirmed: true}))
	must(st.SetParticipant(store.ParticipantRecord{MeetingID: fxMeeting, Speaker: "Speaker 2", Name: "Bob", Confidence: 0.3}))
	must(st.UpsertMeeting(store.MeetingRecord{ID: fxMeet2, Day: fxNextDay, Start: fxTime(fxNextDay, 14, 0), End: fxTime(fxNextDay, 14, 45), App: "zoom.us", Title: "Planning",
		SummaryJSON: `{"participants":[],"summary":"Planned the Q4 roadmap with Alice.","decisions":["Drop the legacy importer"],"action_items":[]}`}))
	must(st.SetParticipant(store.ParticipantRecord{MeetingID: fxMeet2, Speaker: "Speaker 1", Name: "Alice", Confidence: 0.95}))

	// The index, as the pipeline writes it.
	ref := func(h, m int) string { return fmt.Sprintf("hour/%s/%02d#%d", fxDay, h, m*60) }
	must(st.ReplaceText(fxDay, 9, []store.TextDoc{
		{Kind: "transcript", TS: fxTime(fxDay, 9, 12), Text: "Speaker 1: Let us ship the release on Friday", Ref: ref(9, 12)},
		{Kind: "ocr", TS: fxTime(fxDay, 9, 5), Text: "func retryLoop() scheduler", Ref: ref(9, 5)},
		{Kind: "note", TS: fxTime(fxDay, 9, 0), Text: "Refactored the scheduler retry loop", Ref: "hour/" + fxDay + "/09"},
		{Kind: "meeting", TS: fxTime(fxDay, 9, 30), Text: "The team agreed to ship the release on Friday.\nShip the release on Friday", Ref: "meeting/" + fxMeeting},
	}))
	// A stale transcript doc of the withheld hour must never be returned.
	must(st.ReplaceText(fxDay, 10, []store.TextDoc{
		{Kind: "transcript", TS: fxTime(fxDay, 10, 5), Text: "Speaker 1: WITHHELDSECRET release plans", Ref: ref(10, 5)},
	}))
	must(st.ReplaceText(fxNextDay, 14, []store.TextDoc{
		{Kind: "meeting", TS: fxTime(fxNextDay, 14, 45), Text: "Planned the Q4 roadmap with Alice. release", Ref: "meeting/" + fxMeet2},
	}))

	doc := process.HourDoc{Day: fxDay, Hour: 9, Status: store.StatusDone,
		Segments: []activity.Segment{{Start: fxTime(fxDay, 9, 0), End: fxTime(fxDay, 9, 50), App: "Code", Kind: "active"}},
		Transcript: []media.Utterance{
			{Start: fxTime(fxDay, 9, 12), End: fxTime(fxDay, 9, 13), Track: "system", Speaker: "Speaker 1", Text: "Let us ship the release on Friday"},
			{Start: fxTime(fxDay, 9, 14), End: fxTime(fxDay, 9, 15), Track: "mic", Speaker: "You", Text: "Sounds good, I will update the scheduler first"},
			{Start: fxTime(fxDay, 9, 16), End: fxTime(fxDay, 9, 17), Track: "system", Speaker: "Speaker 2", Text: "I can review it tomorrow"},
		},
		Screens:  []media.ScreenText{{Time: fxTime(fxDay, 9, 5), File: filepath.Join(p.Screens(fxDay), "090500.jpg"), Text: "func retryLoop() scheduler"}},
		Meetings: []activity.Meeting{{ID: fxMeeting, Start: fxTime(fxDay, 9, 10), End: fxTime(fxDay, 9, 30), App: "zoom.us", Title: "Standup"}},
		Removed:  []process.RemovedSpan{{Start: fxTime(fxDay, 9, 40), End: fxTime(fxDay, 9, 44), Reason: "bystander"}},
	}
	must(doc.Save(p.DerivedHour(fxDay, 9)))
	withheld := process.HourDoc{Day: fxDay, Hour: 10, Status: store.StatusNeedsReview, SpeechWithheld: true,
		Transcript: []media.Utterance{{Start: fxTime(fxDay, 10, 5), End: fxTime(fxDay, 10, 6), Track: "mic", Speaker: "You", Text: "WITHHELDSECRET"}},
	}
	must(withheld.Save(p.DerivedHour(fxDay, 10)))

	fxWrite(t, p.DailyJournal(fxDay), "# Wednesday\n\nShipped the scheduler retry work and agreed the Friday release.\n")
	fxWrite(t, p.WeeklyJournal("2026-W41"), "# Week 41\n\n## Themes\n- Scheduler reliability\n")
	fxWrite(t, filepath.Join(p.Audio(fxDay), "0900-system.m4a"), "audio")
	fxWrite(t, filepath.Join(p.Screens(fxDay), "090500.jpg"), "jpg")
	fxWrite(t, filepath.Join(p.CacheDay(fxDay), "audio", "0900-system.json"), `{"utterances":[{"Text":"CACHESECRET REMOVEDSECRET"}],"embeddings":{"0":[0.12345]}}`)
	fxWrite(t, filepath.Join(p.CacheDay(fxDay), "privacy.json"), `{"decisions":{"x":{"reason":"PRIVACYSECRET"}}}`)
	fxWrite(t, filepath.Join(p.Voices(), "priya.json"), `{"name":"VOICESECRET","embedding":[0.12345]}`)
	return p
}

// newFixture seeds a data dir and opens it the way `journal mcp` does.
func newFixture(t *testing.T) *Journal {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.ListenAddr = fxAddr
	seedFixture(t, cfg.DataDir)
	j, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	j.Now = func() time.Time { return fxTime(fxDay, 15, 0) }
	return j
}
