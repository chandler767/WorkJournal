package mcpserver

import (
	"testing"
	"time"

	"workjournal/internal/activity"
	"workjournal/internal/config"
	"workjournal/internal/media"
	"workjournal/internal/process"
	"workjournal/internal/store"
)

const (
	lonDay     = "2026-10-12"
	lonMeeting = "2026-10-12-1005-in-person"
	nyMeeting  = "2026-10-12-1130-zoom-us"
)

// newTravelFixture is the fixture plus 2026-10-12 as the user recorded it:
// the morning in London (+01:00), then a call at 11:30 New York time
// (-04:00). The Mac is now in America/New_York.
func newTravelFixture(t *testing.T) *Journal {
	t.Helper()
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	prev := time.Local
	time.Local = ny
	t.Cleanup(func() { time.Local = prev })
	j := newFixture(t)
	bst := time.FixedZone("", 3600)
	lon := func(h, m, s int) time.Time { return time.Date(2026, 10, 12, h, m, s, 0, bst) }
	nyt := func(h, m int) time.Time { return time.Date(2026, 10, 12, h, m, 0, 0, ny) }
	p := config.Paths{Root: j.Cfg.DataDir}
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
	must(st.UpsertHour(store.HourRecord{Day: lonDay, Hour: 10, Status: store.StatusTranscribed, UpdatedAt: lon(11, 0, 0)}))
	must(st.ReplaceSegments(lonDay, 10, []store.SegmentRecord{
		{Day: lonDay, Hour: 10, Start: lon(10, 5, 0), End: lon(10, 45, 0), App: "Notes", Title: "Planning", Kind: "active", Category: "Meetings", Source: "rule"},
	}))
	must(st.UpsertMeeting(store.MeetingRecord{ID: lonMeeting, Day: lonDay, Start: lon(10, 5, 0), End: lon(10, 45, 0), App: activity.InPersonApp, Title: "Planning"}))
	must(st.UpsertMeeting(store.MeetingRecord{ID: nyMeeting, Day: lonDay, Start: nyt(11, 30), End: nyt(12, 0), App: "zoom.us", Title: "Sync"}))
	doc := &process.HourDoc{Day: lonDay, Hour: 10, Status: store.StatusTranscribed,
		Segments:   []activity.Segment{{Start: lon(10, 5, 0), End: lon(10, 45, 0), App: "Notes", Kind: activity.KindActive}},
		Transcript: []media.Utterance{{Start: lon(10, 5, 30), End: lon(10, 5, 40), Track: "mic", Speaker: "me", Text: "agenda first"}},
		Meetings:   []activity.Meeting{{ID: lonMeeting, Start: lon(10, 5, 0), End: lon(10, 45, 0), App: activity.InPersonApp, Title: "Planning"}},
	}
	must(doc.Save(p.DerivedHour(lonDay, 10)))
	return j
}

func TestRecordedTimesShowInTheirOwnZone(t *testing.T) {
	j := newTravelFixture(t)
	out, err := j.GetDay(DayArgs{Date: lonDay})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "10:05–10:45 BST Planning", "11:30–12:00 Sync")
	lacks(t, out, "05:05", "11:30–12:00 EDT")

	out, err = j.ListMeetings(ListMeetingsArgs{From: lonDay, To: lonDay})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, lonDay+" 10:05–10:45 BST · Planning", lonDay+" 11:30–12:00 · Sync")
	lacks(t, out, "05:05")

	out, err = j.GetMeeting(MeetingArgs{ID: lonMeeting, IncludeTranscript: true})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "When: "+lonDay+" 10:05–10:45 BST", "10:05:30 BST me: agenda first")
	lacks(t, out, "05:05")

	out, err = j.GetHour(HourArgs{Date: lonDay, Hour: 10, IncludeTranscript: true})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "- 10:05 BST Notes", "Planning (10:05–10:45 BST, id "+lonMeeting+")", "10:05:30 BST")
	lacks(t, out, "05:05")
}
