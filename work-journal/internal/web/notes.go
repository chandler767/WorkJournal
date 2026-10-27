package web

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"workjournal/internal/notes"
	"workjournal/internal/store"
)

// notesBox is the "Tell the journal" box of an hour or day page: the notes
// shown there, and the form's defaults.
type notesBox struct {
	Date       string
	Hour       string // "HH" on an hour page, "" on a day page
	From, To   string // the new note's default range
	Notes      []noteRow
	RawGone    bool   // retention removed the day's raw recordings
	Notice     string // the result of the last save
	Error      string
	ReturnPath string
}

type noteRow struct {
	ID                 int64
	Kind, Range, Text  string
	From, To, Source   string
	Correction, Edited bool
}

// notesBoxFor lists day's notes for an hour page (hour >= 0: the notes
// covering it, whole-day notes included) or a day page (hour < 0: all).
func (s *Server) notesBoxFor(day string, hour int, q url.Values) (notesBox, error) {
	b := notesBox{Date: day, RawGone: notes.RawGone(s.Paths, day), ReturnPath: "/day/" + day}
	if hour >= 0 {
		b.Hour = fmt.Sprintf("%02d", hour)
		b.From, b.To = b.Hour+":00", fmt.Sprintf("%02d:00", hour+1)
		b.ReturnPath = "/hour/" + day + "/" + b.Hour
	}
	ns, err := s.Store.NotesOn(day)
	if err != nil {
		return b, err
	}
	for _, n := range ns {
		if hour >= 0 && n.Start != "" && !slicesContains(notes.Hours(n), hour) {
			continue
		}
		b.Notes = append(b.Notes, noteRow{ID: n.ID, Kind: n.Kind, Range: notes.Range(n), Text: n.Text, From: n.Start, To: n.End,
			Source: n.Source, Correction: n.Kind == store.NoteCorrection, Edited: !n.UpdatedAt.Equal(n.CreatedAt)})
	}
	b.Notice, b.Error = noteNotice(q.Get("note"), b.RawGone), q.Get("note_error")
	if len(b.Error) > 300 {
		b.Error = b.Error[:300]
	}
	return b, nil
}

func slicesContains(hs []int, h int) bool {
	for _, x := range hs {
		if x == h {
			return true
		}
	}
	return false
}

// noteNotice maps the ?note= flag to fixed text.
func noteNotice(v string, rawGone bool) string {
	switch v {
	case "saved":
		return "Note saved. The journal uses it from now on; hours it covers are summarized again shortly."
	case "correction":
		if rawGone {
			return "Correction saved. This day's raw recordings were already removed, so it can change the summaries but not the transcript."
		}
		return "Correction saved. The hours it covers are reprocessed shortly."
	case "deleted":
		return "Note deleted. The hours it covered are processed again shortly."
	}
	return ""
}

// noteReturn is the page a note form came from (its return field, when a
// dashboard page), else the note's day page.
func noteReturn(r *http.Request, day string) string {
	if p := r.PostFormValue("return"); safeReturn(p) {
		return p
	}
	return "/day/" + day
}

func withQuery(path, key, val string) string {
	return path + "?" + url.Values{key: {val}}.Encode()
}

func (s *Server) handleNoteAdd(w http.ResponseWriter, r *http.Request) {
	d := noteDraft(r)
	back := noteReturn(r, d.Day)
	if _, _, ok := parseDate(d.Day); !ok {
		http.Error(w, "bad date", http.StatusBadRequest)
		return
	}
	n, err := notes.Add(s.Store, d, store.SourceDashboard, time.Now())
	if err != nil {
		s.noteFailed(w, r, back, err)
		return
	}
	http.Redirect(w, r, withQuery(back, "note", savedFlag(n)), http.StatusSeeOther)
}

func (s *Server) handleNoteUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad note id", http.StatusBadRequest)
		return
	}
	d := noteDraft(r)
	old, ok, err := s.Store.Note(id)
	if err != nil || !ok {
		http.Error(w, "no such note", http.StatusNotFound)
		return
	}
	back := noteReturn(r, old.Day)
	n, err := notes.Update(s.Store, id, d, time.Now())
	if err != nil {
		s.noteFailed(w, r, back, err)
		return
	}
	flag := savedFlag(n)
	if old.Kind == store.NoteCorrection {
		flag = "correction"
	}
	http.Redirect(w, r, withQuery(back, "note", flag), http.StatusSeeOther)
}

func (s *Server) handleNoteDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad note id", http.StatusBadRequest)
		return
	}
	old, err := notes.Delete(s.Store, id)
	if errors.Is(err, notes.ErrNotFound) {
		http.Error(w, "no such note", http.StatusNotFound)
		return
	}
	if err != nil {
		s.noteFailed(w, r, noteReturn(r, old.Day), err)
		return
	}
	http.Redirect(w, r, withQuery(noteReturn(r, old.Day), "note", "deleted"), http.StatusSeeOther)
}

func noteDraft(r *http.Request) notes.Draft {
	return notes.Draft{Kind: r.PostFormValue("kind"), Day: r.PostFormValue("date"),
		From: r.PostFormValue("from"), To: r.PostFormValue("to"), Text: r.PostFormValue("text")}
}

func savedFlag(n store.NoteRecord) string {
	if n.Kind == store.NoteCorrection {
		return "correction"
	}
	return "saved"
}

// noteFailed sends the browser back with the validation error shown in the
// box; anything else is logged.
func (s *Server) noteFailed(w http.ResponseWriter, r *http.Request, back string, err error) {
	log.Printf("web: note: %v", err)
	http.Redirect(w, r, withQuery(back, "note_error", err.Error()), http.StatusSeeOther)
}
