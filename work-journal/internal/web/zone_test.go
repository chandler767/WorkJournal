package web

import (
	"strings"
	"testing"
	"time"

	"workjournal/internal/activity"
	"workjournal/internal/media"
	"workjournal/internal/process"
	"workjournal/internal/store"
)

// seedTravel adds 2026-10-12 as recorded: the morning in London (+01:00),
// a call at 11:30 New York time, with the Mac now in America/New_York.
func seedTravel(t *testing.T) *env {
	t.Helper()
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	prev := time.Local
	time.Local = ny
	t.Cleanup(func() { time.Local = prev })
	e := newEnv(t)
	bst := time.FixedZone("", 3600)
	lon := func(h, m, s int) time.Time { return time.Date(2026, 10, 12, h, m, s, 0, bst) }
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	const d = "2026-10-12"
	must(e.st.UpsertHour(store.HourRecord{Day: d, Hour: 10, Status: store.StatusTranscribed, UpdatedAt: lon(11, 0, 0)}))
	must(e.st.ReplaceSegments(d, 10, []store.SegmentRecord{
		{Day: d, Hour: 10, Start: lon(10, 5, 0), End: lon(10, 45, 0), App: "Notes", Title: "Planning", Kind: "active", Category: "Meetings", Source: "rule"},
	}))
	must(e.st.UpsertMeeting(store.MeetingRecord{ID: "2026-10-12-1005-in-person", Day: d, Start: lon(10, 5, 0), End: lon(10, 45, 0), App: activity.InPersonApp, Title: "Planning"}))
	must(e.st.UpsertMeeting(store.MeetingRecord{ID: "2026-10-12-1130-zoom-us", Day: d, Start: time.Date(2026, 10, 12, 11, 30, 0, 0, ny), End: time.Date(2026, 10, 12, 12, 0, 0, 0, ny), App: "zoom.us", Title: "Sync"}))
	doc := process.HourDoc{Day: d, Hour: 10, Status: store.StatusTranscribed,
		Transcript: []media.Utterance{{Start: lon(10, 5, 30), End: lon(10, 5, 40), Track: "mic", Speaker: "me", Text: "agenda first"}},
		Screens:    []media.ScreenText{{Time: lon(10, 6, 0), File: "100600.jpg", Text: "agenda"}},
	}
	must(doc.Save(e.p.DerivedHour(d, 10)))
	return e
}

func TestPagesShowRecordedTimesInTheirOwnZone(t *testing.T) {
	e := seedTravel(t)
	body := e.get("/day/2026-10-12").Body.String()
	mustContain(t, body, "10:05–10:45 BST", "11:30–12:00", `&#34;s&#34;:605`)
	if strings.Contains(body, "05:05") || strings.Contains(body, "12:00 EDT") {
		t.Errorf("day page shifts or labels times:\n%s", body)
	}
	body = e.get("/meeting/2026-10-12-1005-in-person").Body.String()
	mustContain(t, body, "10:05–10:45 BST", "10:05:30 BST", "agenda first")
	if strings.Contains(body, "05:05") {
		t.Errorf("meeting page shifts times:\n%s", body)
	}
	body = e.get("/hour/2026-10-12/10").Body.String()
	mustContain(t, body, "10:05:30 BST", "10:06:00 BST", "10:05 BST", `data-t="330"`)
	if strings.Contains(body, "05:05") {
		t.Errorf("hour page shifts times:\n%s", body)
	}
	// A day recorded where the Mac is now carries no zone.
	body = e.get("/day/" + seedDay).Body.String()
	if strings.Contains(body, " EDT") || strings.Contains(body, " BST") {
		t.Errorf("same-zone day labelled with a zone")
	}
}
