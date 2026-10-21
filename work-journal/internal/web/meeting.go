package web

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"workjournal/internal/process"
	"workjournal/internal/scheduler"
	"workjournal/internal/skills"
	"workjournal/internal/store"
	"workjournal/internal/wallclock"
)

var meetingIDRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-\d{4}-[a-z0-9-]+$`)

const maxNameLen = 80

type meetingUtter struct {
	Clock, Speaker, Track, Text string
}

type speakerRow struct {
	Speaker, Display string
	Confirmed        bool
}

type meetingPage struct {
	Title, ID, Day, App, Time string
	Summary                   skills.MeetingSummary
	HasSummary                bool
	Speakers                  []speakerRow
	Transcript                []meetingUtter
	Notice                    string
	CanName                   bool
	DocNote                   string
	// Imported marks a meeting made from an imported recording; ImportError
	// is its last processing failure and Processing that it is not
	// processed yet.
	Imported, Processing bool
	ImportError          string
}

// notices maps the ?notice= flag to fixed text; unknown values show nothing.
var notices = map[string]string{
	"expired":       "raw media expired — name saved, voice not learned",
	"enroll-failed": "enrolment failed — name saved, voice not learned (see the log)",
}

func (s *Server) loadMeeting(w http.ResponseWriter, id string) (store.MeetingRecord, bool) {
	if !meetingIDRE.MatchString(id) {
		http.Error(w, "bad meeting id", http.StatusBadRequest)
		return store.MeetingRecord{}, false
	}
	m, found, err := s.Store.Meeting(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return m, false
	}
	if !found {
		http.NotFound(w, nil)
		return m, false
	}
	return m, true
}

func (s *Server) handleMeeting(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadMeeting(w, r.PathValue("id"))
	if !ok {
		return
	}
	cfg := s.Cfg()
	p := meetingPage{Title: m.Title, ID: m.ID, Day: m.Day, App: m.App,
		Time:     wallclock.Span(m.Start, m.End),
		Notice:   notices[r.URL.Query().Get("notice")],
		CanName:  cfg.Diarization != "off",
		Imported: m.Imported, Processing: m.ImportStatus == store.StatusPending, ImportError: m.ImportError}
	if m.SummaryJSON != "" {
		if err := json.Unmarshal([]byte(m.SummaryJSON), &p.Summary); err == nil {
			p.HasSummary = true
		} else {
			p.DocNote = "Summary unreadable: " + err.Error()
		}
	}

	parts, err := s.Store.Participants(m.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	byName := make(map[string]*store.ParticipantRecord, len(parts))
	for i := range parts {
		byName[parts[i].Speaker] = &parts[i]
		p.Speakers = append(p.Speakers, speakerRow{Speaker: parts[i].Speaker, Confirmed: parts[i].Confirmed,
			Display: process.SpeakerDisplay(parts[i].Speaker, &parts[i], cfg.NameConfidenceThreshold)})
	}

	// Every derived doc holding the meeting's speech, keeping utterances in
	// [Start, End]; an hour not processed yet has none.
	for _, doc := range process.MeetingDocs(s.Paths, m) {
		for _, u := range doc.Transcript {
			if u.Start.Before(m.Start) || u.Start.After(m.End) {
				continue
			}
			p.Transcript = append(p.Transcript, meetingUtter{Clock: wallclock.Format(u.Start, "15:04:05"),
				Speaker: process.SpeakerDisplay(u.Speaker, byName[u.Speaker], cfg.NameConfidenceThreshold), Track: u.Track, Text: u.Text})
		}
	}
	render(w, "meeting", p)
}

func (s *Server) handleSpeaker(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadMeeting(w, r.PathValue("id"))
	if !ok {
		return
	}
	if s.Cfg().Diarization == "off" {
		http.Error(w, "speaker naming is disabled (diarization is off)", http.StatusConflict)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	speaker := strings.TrimSpace(r.PostFormValue("speaker"))
	name := strings.TrimSpace(r.PostFormValue("name"))
	if speaker == "" || utf8.RuneCountInString(speaker) > maxNameLen || name == "" || utf8.RuneCountInString(name) > maxNameLen {
		http.Error(w, "speaker and name are required (name up to 80 characters)", http.StatusBadRequest)
		return
	}
	parts, err := s.Store.Participants(m.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !slices.ContainsFunc(parts, func(p store.ParticipantRecord) bool { return p.Speaker == speaker }) {
		http.Error(w, "unknown speaker for this meeting", http.StatusBadRequest)
		return
	}
	// Save the name first: a failed enrolment must never lose it.
	if err := s.Store.SetParticipant(store.ParticipantRecord{MeetingID: m.ID, Speaker: speaker, Name: name, Confidence: 1, Confirmed: true}); err != nil {
		http.Error(w, "save name: "+err.Error(), http.StatusInternalServerError)
		return
	}
	to := "/meeting/" + m.ID
	switch err := s.Jobs.EnrollSpeaker(r.Context(), m.ID, speaker, name); {
	case err == nil:
	case errors.Is(err, scheduler.ErrMediaExpired):
		to += "?notice=expired"
	default:
		log.Printf("web: enroll speaker %q in %s: %v", speaker, m.ID, err)
		to += "?notice=enroll-failed"
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}
