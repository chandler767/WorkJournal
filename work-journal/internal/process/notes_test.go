package process

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"workjournal/internal/activity"
	"workjournal/internal/media"
	"workjournal/internal/notes"
	"workjournal/internal/skills"
	"workjournal/internal/store"
)

// addNote saves a note on the fixture day.
func addNote(t *testing.T, st *store.Store, kind, from, to, text string) {
	t.Helper()
	n, err := notes.Validate(notes.Draft{Kind: kind, Day: day, From: from, To: to, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	n.Source, n.CreatedAt, n.UpdatedAt = store.SourceCLI, time.Now(), time.Now()
	if _, err := st.AddNote(n, nil); err != nil {
		t.Fatal(err)
	}
}

// A "disregard" correction reaches privacy-filter for the hour it covers,
// and the speech the skill drops for it is removed even below
// privacy_drop_confidence; the hour page reads it as removed by the note.
func TestPrivacyCorrectionDisregardsSpeech(t *testing.T) {
	e := newEnv(t)
	r := privacyFixture(e)
	addNote(t, e.st, store.NoteCorrection, "13:01", "13:02", "people next to me, not me; disregard it")
	addNote(t, e.st, store.NoteCorrection, "15:00", "15:10", "another hour")
	addNote(t, e.st, store.NoteContext, "13:00", "14:00", "context is not for the privacy filter")
	r.respond = func(in privacyInput) string {
		out := skills.PrivacyResult{Drop: []skills.DropRange{}, Redact: []skills.Redaction{}}
		for _, l := range in.Utterances {
			if strings.HasPrefix(l.Time, "13:01") {
				// Below the default 0.7: a correction's drop applies anyway.
				out.Drop = append(out.Drop, skills.DropRange{From: l.Index, To: l.Index, Reason: ReasonUserNote, Confidence: 0.5})
			}
		}
		b, _ := json.Marshal(out)
		return string(b)
	}
	if err := e.p.ProcessPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.inputs) != 1 {
		t.Fatalf("privacy-filter ran %d times", len(r.inputs))
	}
	want := []skills.UserNote{{Kind: store.NoteCorrection, From: "13:01", To: "13:02", Context: []string{"people next to me, not me; disregard it"}}}
	if got := r.inputs[0].UserNotes; !reflect.DeepEqual(got, want) {
		t.Fatalf("user_notes = %+v, want %+v", got, want)
	}
	doc, err := Load(e.paths.DerivedHour(day, 13))
	if err != nil {
		t.Fatal(err)
	}
	if got := transcriptTexts(doc); !reflect.DeepEqual(got, []string{"the parser needs a retry", "the account number is 4417 1234"}) {
		t.Fatalf("transcript = %q", got)
	}
	if len(doc.Removed) != 1 || doc.Removed[0].Reason != ReasonUserNote {
		t.Fatalf("removed = %+v", doc.Removed)
	}
	assertDerivedLacks(t, e, 13, "parser", "love you mom", "dinner")
	if hits, _ := e.st.Search("dinner", 10); len(hits) != 0 {
		t.Errorf("search dinner found %+v", hits)
	}
	if got := RemovedNotices(doc.Removed); len(got) != 1 || !strings.Contains(got[0], "removed by your note") {
		t.Errorf("notices = %q", got)
	}
}

// A drop for the user's correction applies whatever its confidence; the
// filter's own drops are held to privacy_drop_confidence.
func TestPrivacyUserNoteDropIgnoresThreshold(t *testing.T) {
	e := newEnv(t)
	p := e.p
	low := privacyDecision{Drop: true, Reason: "bystander", Confidence: 0.5}
	note := privacyDecision{Drop: true, Reason: ReasonUserNote, Confidence: 0.5}
	if p.dropped(low) || !p.dropped(note) {
		t.Fatalf("dropped(bystander 0.5)=%v dropped(user_note 0.5)=%v", p.dropped(low), p.dropped(note))
	}
}

// Notes covering the hour reach summarize-hour; a whole-day context note
// does not (close-day gets it), a whole-day correction does.
func TestSummarizerHourInputHasNotes(t *testing.T) {
	e := newSumEnv(t)
	doc := &HourDoc{Day: day, Hour: 10, Segments: []activity.Segment{sg(activity.KindActive, "Code", "main.go", 10, 0, 30)}}
	e.transcribed(doc)
	addNote(t, e.st, store.NoteContext, "10:15", "11:30", "This was the Cloudflare planning call.")
	addNote(t, e.st, store.NoteContext, "", "", "Whole-day context.")
	addNote(t, e.st, store.NoteCorrection, "", "", "Speaker 2 all day is Maya.")
	addNote(t, e.st, store.NoteContext, "11:00", "12:00", "Another hour.")
	e.fk.out["summarize-hour"] = hourOut
	e.fk.out["categorize-time"] = `{"segments":[{"index":0,"category":"Deep work","confidence":0.9}]}`
	if err := e.s.SummarizeHour(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(e.fk.calls[0].input["user_notes"])
	want := `[{"context_lines":["Speaker 2 all day is Maya."],"kind":"correction","title":""},` +
		`{"context_lines":["This was the Cloudflare planning call."],"from":"10:15","kind":"context","title":"","to":"11:30"}]`
	if string(b) != want {
		t.Fatalf("user_notes\n got %s\nwant %s", b, want)
	}
}

// A note in a meeting's span reaches summarize-meeting as user_notes.
func TestSummarizerMeetingInputHasNotes(t *testing.T) {
	e := newSumEnv(t)
	m := activity.Meeting{ID: "2026-10-07-1010-zoom-us", Start: at(10, 10, 0), End: at(10, 40, 0), App: "zoom.us", Title: "Zoom"}
	doc := &HourDoc{Day: day, Hour: 10, Meetings: []activity.Meeting{m}, EndedMeetings: []string{m.ID},
		Transcript: []media.Utterance{{Start: at(10, 11, 0), End: at(10, 11, 3), Track: "system", Speaker: "Speaker 1", Text: "hi"}}}
	e.transcribed(doc)
	addNote(t, e.st, store.NoteContext, "10:30", "10:45", "Cloudflare planning call.")
	addNote(t, e.st, store.NoteContext, "10:40", "10:50", "After the meeting.")
	e.fk.out["summarize-hour"] = hourOut
	e.fk.out["summarize-meeting"] = `{"participants":[],"summary":"s","decisions":[],"action_items":[]}`
	if err := e.s.SummarizeHour(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	var in map[string]any
	for _, c := range e.fk.calls {
		if c.skill == "summarize-meeting" {
			in = c.input
		}
	}
	got, _ := json.Marshal(in["user_notes"])
	if want := `[{"context_lines":["Cloudflare planning call."],"from":"10:30","kind":"context","title":"","to":"10:45"}]`; string(got) != want {
		t.Fatalf("user_notes\n got %s\nwant %s", got, want)
	}
	if _, ok := in["user_note"]; ok {
		t.Errorf("a recorded meeting got an import's user_note: %v", in["user_note"])
	}
}

func TestRemovedNoticesKeepNoteRemovalsApart(t *testing.T) {
	got := RemovedNotices([]RemovedSpan{
		{Start: at(13, 0, 0), End: at(13, 2, 0), Reason: "bystander"},
		{Start: at(13, 2, 10), End: at(13, 5, 0), Reason: ReasonUserNote},
		{Start: at(13, 5, 20), End: at(13, 9, 0), Reason: ReasonUserNote},
	})
	want := []string{"2 min of non-work audio removed (13:00–13:02)", "7 min of speech removed by your note (13:02–13:09)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("notices = %q, want %q", got, want)
	}
}
