package mcpserver

import (
	"testing"

	"workjournal/internal/activity"
	"workjournal/internal/config"
	"workjournal/internal/media"
	"workjournal/internal/process"
	"workjournal/internal/store"
)

const fxImport = "2026-10-07-1830-imported"

// newImportFixture is the fixture plus an imported recording, processed and
// summarized, with its transcript indexed under its meeting.
func newImportFixture(t *testing.T) *Journal {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.ListenAddr = fxAddr
	p := seedFixture(t, cfg.DataDir)
	st, err := store.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	start, end := fxTime(fxDay, 18, 30), fxTime(fxDay, 18, 36)
	for _, err := range []error{
		st.UpsertMeeting(store.MeetingRecord{ID: fxImport, Day: fxDay, Start: start, End: end, App: activity.InPersonApp,
			Title: "AI Tinkerers meetup", SummaryJSON: `{"participants":[],"summary":"Maya demoed speaker clustering.","decisions":[],"action_items":[]}`}),
		st.UpsertImport(store.ImportRecord{ID: fxImport, Status: store.StatusDone, UpdatedAt: end}),
		st.ReplaceImportText(fxImport, []store.TextDoc{{Kind: "transcript", Day: fxDay, Hour: 18, TS: fxTime(fxDay, 18, 31),
			Text: "Speaker 1: the clustering runs on device", Ref: "meeting/" + fxImport}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	doc := process.HourDoc{Day: fxDay, Hour: 18, Transcript: []media.Utterance{
		{Start: fxTime(fxDay, 18, 31), End: fxTime(fxDay, 18, 32), Track: "mic", Speaker: "Speaker 1", Text: "the clustering runs on device"},
	}}
	if err := doc.Save(p.ImportDoc(fxImport)); err != nil {
		t.Fatal(err)
	}
	j, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	return j
}

func TestImportedMeetingOverMCP(t *testing.T) {
	j := newImportFixture(t)
	out, err := j.ListMeetings(ListMeetingsArgs{From: fxDay, To: fxDay})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "AI Tinkerers meetup (In person, imported) · id "+fxImport, "Maya demoed speaker clustering.")

	out, err = j.GetMeeting(MeetingArgs{ID: fxImport, IncludeTranscript: true})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "# AI Tinkerers meetup", "18:30–18:36 (In person, imported)", "Imported: recorded on the user's phone",
		"Summary: Maya demoed speaker clustering.", "18:31:00 Speaker 1: the clustering runs on device")

	out, err = j.Search(SearchArgs{Query: "clustering"})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "[transcript] 2026-10-07 18:31", "/meeting/"+fxImport)

	out, err = j.GetDay(DayArgs{Date: fxDay})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "AI Tinkerers meetup (In person, imported) — id "+fxImport)
}
