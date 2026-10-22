package web

import (
	"testing"

	"workjournal/internal/activity"
	"workjournal/internal/media"
	"workjournal/internal/process"
	"workjournal/internal/store"
)

const importID = "2026-10-07-1830-imported"

// seedImport adds an imported recording's meeting (18:30-18:36) with its
// import state and derived doc.
func seedImport(t *testing.T, e *env, status, errMsg string) {
	t.Helper()
	if err := e.st.UpsertMeeting(store.MeetingRecord{ID: importID, Day: seedDay, Start: seedTime(18, 30), End: seedTime(18, 36),
		App: activity.InPersonApp, Title: "AI Tinkerers meetup"}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpsertImport(store.ImportRecord{ID: importID, Status: status, Error: errMsg, UpdatedAt: seedTime(19, 0)}); err != nil {
		t.Fatal(err)
	}
	doc := process.HourDoc{Day: seedDay, Hour: 18, Transcript: []media.Utterance{
		{Start: seedTime(18, 31), End: seedTime(18, 32), Track: "mic", Speaker: "Speaker 1", Text: "the demo is local transcription"},
	}}
	if err := doc.Save(e.p.ImportDoc(importID)); err != nil {
		t.Fatal(err)
	}
}

func TestImportedMeetingOnDayAndMeetingPages(t *testing.T) {
	e := newEnv(t)
	seedImport(t, e, store.StatusDone, "")
	body := e.get("/day/" + seedDay).Body.String()
	mustContain(t, body, `href="/meeting/`+importID+`"`, "AI Tinkerers meetup", `<span class="badge">imported</span>`)
	rec := e.get("/meeting/" + importID)
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	mustContain(t, rec.Body.String(), "AI Tinkerers meetup", "imported", "the demo is local transcription")
}

func TestImportedMeetingShowsProcessingError(t *testing.T) {
	e := newEnv(t)
	seedImport(t, e, store.StatusPending, "transcribe: whisper crashed")
	mustContain(t, e.get("/day/"+seedDay).Body.String(), "processing</span>", "Processing failed: transcribe: whisper crashed")
	mustContain(t, e.get("/meeting/"+importID).Body.String(), "Processing failed: transcribe: whisper crashed", "The recording is kept.")
}
