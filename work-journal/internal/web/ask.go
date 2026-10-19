package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"workjournal/internal/skills"
)

// Asker answers a question about the journal (ask.Runner): Claude with only
// the journal MCP tools. bypassPause is "Ask anyway" during a usage-limit
// pause, for that one question.
type Asker interface {
	Ask(ctx context.Context, question string, bypassPause bool) (string, error)
}

// Ask settings: the KV key of the history, how many Q&As it keeps, the
// longest question and how long one ask may take in the background.
const (
	kvAskHistory   = "ask_history"
	askHistoryMax  = 20
	askMaxQuestion = 1000
	askBudget      = 3*time.Minute + 30*time.Second
	askPollMS      = 3000
)

var askIDRE = regexp.MustCompile(`^[0-9a-f]{16}$`)

// askEntry is one question and its answer (or error) in ask_history.
type askEntry struct {
	ID       string    `json:"id"`
	Question string    `json:"question"`
	Answer   string    `json:"answer,omitempty"` // Markdown
	Error    string    `json:"error,omitempty"`
	Asked    time.Time `json:"asked"`
	Answered time.Time `json:"answered"`
}

type askRow struct {
	ID, Question, When, Error string
	Answer                    template.HTML
	Highlight                 bool
}

type askPage struct {
	Title       string
	Available   bool
	ClaudePause string
	Notice      string
	Question    string // prefilled: a question refused while paused
	Pending     *askRow
	History     []askRow
	Rev         string
	PollMS      int
}

func (s *Server) askHistory() ([]askEntry, error) {
	v, ok, err := s.Store.GetKV(kvAskHistory)
	if err != nil || !ok || v == "" {
		return nil, err
	}
	var h []askEntry
	if err := json.Unmarshal([]byte(v), &h); err != nil {
		return nil, err
	}
	return h, nil
}

// addAsk prepends e to the history, keeping the newest askHistoryMax.
func (s *Server) addAsk(e askEntry) error {
	h, err := s.askHistory()
	if err != nil {
		log.Printf("web: ask_history unreadable (%v); starting it again", err)
		h = nil
	}
	h = append([]askEntry{e}, h...)
	if len(h) > askHistoryMax {
		h = h[:askHistoryMax]
	}
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return s.Store.SetKV(kvAskHistory, string(b))
}

// running returns the ask in progress, nil when there is none.
func (s *Server) running() *askEntry {
	s.askMu.Lock()
	defer s.askMu.Unlock()
	if s.askRunning == nil {
		return nil
	}
	e := *s.askRunning
	return &e
}

// askWait waits for the background ask (tests).
func (s *Server) askWait() { s.askWG.Wait() }

func (s *Server) askRev() string {
	h, _, _ := s.Store.GetKV(kvAskHistory)
	run := ""
	if e := s.running(); e != nil {
		run = e.ID
	}
	return shortHash(h, run, s.pauseRev(), boolStr(s.Ask != nil))
}

func (s *Server) handleRevAsk(w http.ResponseWriter, r *http.Request) { writeRev(w, s.askRev()) }

func (s *Server) handleAskPage(w http.ResponseWriter, r *http.Request) {
	s.renderAsk(w, r, "", askNotices[r.URL.Query().Get("notice")])
}

var askNotices = map[string]string{
	"busy": "Another question is already being answered; ask again when it is done.",
}

func (s *Server) renderAsk(w http.ResponseWriter, r *http.Request, question, notice string) {
	p := askPage{Title: "Ask", Available: s.Ask != nil, Notice: notice, Question: question, PollMS: askPollMS}
	p.Rev = s.askRev() // before reading: a later change just means one more refresh
	p.ClaudePause = s.claudePause()
	pending := r.URL.Query().Get("pending")
	if !askIDRE.MatchString(pending) {
		pending = ""
	}
	if e := s.running(); e != nil {
		p.Pending = &askRow{ID: e.ID, Question: e.Question, When: e.Asked.Local().Format("Jan 2 15:04")}
	}
	h, err := s.askHistory()
	if err != nil {
		p.Notice = "Earlier questions could not be read: " + err.Error()
	}
	for _, e := range h {
		row := askRow{ID: e.ID, Question: e.Question, Error: e.Error, When: e.Asked.Local().Format("Jan 2 15:04"), Highlight: e.ID == pending}
		if e.Answer != "" {
			if row.Answer, err = renderMarkdown(e.Answer, s.Cfg().ListenAddr); err != nil {
				row.Error = "answer could not be rendered: " + err.Error()
			}
		}
		p.History = append(p.History, row)
	}
	render(w, "ask", p)
}

// handleAsk starts answering a question in the background and sends the
// browser to /ask?pending=<id>, which refreshes until the answer lands.
// One ask runs at a time. While Claude is paused for a usage limit the
// question is not run unless force ("Ask anyway").
func (s *Server) handleAsk(w http.ResponseWriter, r *http.Request) {
	if s.Ask == nil {
		http.Error(w, "Ask is not available", http.StatusServiceUnavailable)
		return
	}
	q := strings.TrimSpace(r.PostFormValue("q"))
	if q == "" || utf8.RuneCountInString(q) > askMaxQuestion {
		http.Error(w, "a question of 1 to 1000 characters is required", http.StatusBadRequest)
		return
	}
	force := r.PostFormValue("force") == "1"
	if !force && s.claudePause() != "" {
		s.renderAsk(w, r, q, "")
		return
	}
	id, err := newAskID()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	e := askEntry{ID: id, Question: q, Asked: s.now()}
	s.askMu.Lock()
	if cur := s.askRunning; cur != nil {
		s.askMu.Unlock()
		http.Redirect(w, r, "/ask?pending="+cur.ID+"&notice=busy", http.StatusSeeOther)
		return
	}
	if s.askBase == nil {
		s.askBase, s.askStop = context.WithCancel(context.Background())
	}
	base := s.askBase
	s.askRunning = &e
	s.askWG.Add(1)
	s.askMu.Unlock()
	go s.runAsk(base, e, force)
	http.Redirect(w, r, "/ask?pending="+id, http.StatusSeeOther)
}

// runAsk answers e with its own context (the request has ended by then;
// base ends at shutdown), records it in the history and clears the running
// ask.
func (s *Server) runAsk(base context.Context, e askEntry, force bool) {
	defer s.askWG.Done()
	ctx, cancel := context.WithTimeout(base, askBudget)
	defer cancel()
	ans, err := s.Ask.Ask(ctx, e.Question, force)
	e.Answered = s.now()
	var ul skills.ErrUsageLimited
	switch {
	case errors.As(err, &ul):
		e.Error = "Claude is at its usage limit (" + ul.Error() + "). Try again later, or use Ask anyway."
	case err != nil:
		log.Printf("web: ask: %v", err)
		e.Error = err.Error()
	default:
		e.Answer = ans
	}
	if err := s.addAsk(e); err != nil {
		log.Printf("web: save ask: %v", err)
	}
	s.askMu.Lock()
	s.askRunning = nil
	s.askMu.Unlock()
}

// CloseAsks cancels a running ask (shutdown) and waits up to d for it to
// record its outcome.
func (s *Server) CloseAsks(d time.Duration) {
	s.askMu.Lock()
	if s.askStop != nil {
		s.askStop()
	}
	s.askMu.Unlock()
	done := make(chan struct{})
	go func() { s.askWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		log.Printf("web: an ask did not stop within %v", d)
	}
}

func newAskID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
