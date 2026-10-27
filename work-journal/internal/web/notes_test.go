package web

import (
	"net/url"
	"strings"
	"testing"

	"workjournal/internal/process"
	"workjournal/internal/store"
)

// The hour page's box adds a correction (queued for reprocessing), lists
// it with edit and delete, and edits and deletes it.
func TestHourPageNotes(t *testing.T) {
	e := newEnv(t)
	b := e.get("/hour/2026-10-07/09").Body.String()
	mustContain(t, b, "Tell the journal", `action="/notes"`, `name="from" value="09:00"`, `name="to" value="10:00"`)

	rec := e.post("/notes", url.Values{"date": {seedDay}, "kind": {"correction"}, "from": {"09:10"}, "to": {"09:20"},
		"text": {"people next to me, not me; disregard it"}, "return": {"/hour/2026-10-07/09"}}, nil)
	if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), "/hour/2026-10-07/09?note=correction") {
		t.Fatalf("add: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	ns, _ := e.st.NotesOn(seedDay)
	if len(ns) != 1 || ns[0].Source != store.SourceDashboard || ns[0].Start != "09:10" {
		t.Fatalf("notes %+v", ns)
	}
	if q, _ := e.st.NoteQueue(); len(q) != 2 || q[0].Hour != 9 || q[0].Redo != store.RedoPrivacy {
		t.Fatalf("queue %+v", q)
	}
	b = e.get(rec.Header().Get("Location")).Body.String()
	mustContain(t, b, "Correction saved", "people next to me, not me; disregard it", "09:10–09:20", `action="/notes/1/delete"`)
	if b := e.get("/hour/2026-10-07/11").Body.String(); strings.Contains(b, "disregard it") {
		t.Error("hour 11 lists a note on 09:10–09:20")
	}
	if b := e.get("/day/2026-10-07").Body.String(); !strings.Contains(b, "disregard it") {
		t.Error("day page does not list the note")
	}

	rec = e.post("/notes/1", url.Values{"kind": {"context"}, "from": {"09:00"}, "to": {"09:30"}, "text": {"Cloudflare planning call"},
		"return": {"/hour/2026-10-07/09"}}, nil)
	if rec.Code != 303 {
		t.Fatalf("edit: %d", rec.Code)
	}
	if n, _, _ := e.st.Note(1); n.Kind != store.NoteContext || n.Text != "Cloudflare planning call" || n.Day != seedDay {
		t.Fatalf("edited %+v", n)
	}
	rec = e.post("/notes/1/delete", url.Values{"return": {"/hour/2026-10-07/09"}}, nil)
	if rec.Code != 303 || !strings.Contains(rec.Header().Get("Location"), "note=deleted") {
		t.Fatalf("delete: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if ns, _ := e.st.NotesOn(seedDay); len(ns) != 0 {
		t.Fatalf("left %+v", ns)
	}
}

func TestNoteFormRejectsBadInput(t *testing.T) {
	e := newEnv(t)
	rec := e.post("/notes", url.Values{"date": {seedDay}, "kind": {"context"}, "from": {"10:00"}, "to": {"09:00"}, "text": {"x"},
		"return": {"/day/2026-10-07"}}, nil)
	loc := rec.Header().Get("Location")
	if rec.Code != 303 || !strings.Contains(loc, "note_error=") {
		t.Fatalf("bad range: %d %s", rec.Code, loc)
	}
	mustContain(t, e.get(loc).Body.String(), "Note not saved: to 09:00 must be after from 10:00")
	if rec := e.post("/notes", url.Values{"date": {seedDay}, "kind": {"context"}, "text": {"x"}}, map[string]string{"Origin": "http://evil.example"}); rec.Code != 403 {
		t.Errorf("cross-origin add: %d", rec.Code)
	}
	if rec := e.post("/notes/99/delete", nil, nil); rec.Code != 404 {
		t.Errorf("delete missing: %d", rec.Code)
	}
	if ns, _ := e.st.NotesOn(seedDay); len(ns) != 0 {
		t.Fatalf("saved %+v", ns)
	}
}

// Speech a correction removed reads as removed by the note on the hour and
// day pages; a context note is found by search and links to its hour.
func TestNoteRemovalAndSearch(t *testing.T) {
	e := newEnv(t)
	doc, err := process.Load(e.p.DerivedHour(seedDay, 9))
	if err != nil {
		t.Fatal(err)
	}
	doc.Removed = []process.RemovedSpan{{Start: seedTime(9, 10), End: seedTime(9, 20), Reason: process.ReasonUserNote}}
	if err := doc.Save(e.p.DerivedHour(seedDay, 9)); err != nil {
		t.Fatal(err)
	}
	mustContain(t, e.get("/hour/2026-10-07/09").Body.String(), "10 min of speech removed by your note (09:10–09:20)")
	mustContain(t, e.get("/day/2026-10-07").Body.String(), "Some speech removed by your note")

	e.post("/notes", url.Values{"date": {seedDay}, "kind": {"context"}, "from": {"09:00"}, "to": {"09:30"}, "text": {"Zanzibar planning call"}}, nil)
	mustContain(t, e.get("/search?q=zanzibar").Body.String(), "user_note", `href="/hour/2026-10-07/09#note-1"`)
}
