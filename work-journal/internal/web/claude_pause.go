package web

import (
	"log"
	"net/http"
	"time"

	"workjournal/internal/config"
	"workjournal/internal/skills"
)

// claudePause is the banner text while Claude work is paused for a usage
// limit (skills.PausingRunner), "" when it is not or pausing is off.
func (s *Server) claudePause() string {
	if !s.Cfg().PauseOnUsageLimit {
		return ""
	}
	now := s.now()
	st, err := skills.ReadPause(s.Store, now)
	if err != nil {
		log.Printf("web: claude pause: %v", err)
		return ""
	}
	if !st.Active {
		return ""
	}
	if !st.ResetKnown {
		return "Claude usage limit reached — summaries paused for about an hour"
	}
	until := st.Until.Local()
	text := "Claude usage limit reached — summaries paused until " + until.Format("3:04 PM")
	if config.DayKey(until) != config.DayKey(now) {
		text += " tomorrow"
	}
	return text
}

// pauseRev is the pause's part of the page revs, so pages refresh when it
// starts or ends.
func (s *Server) pauseRev() string {
	v, _, _ := s.Store.GetKV(skills.KVPausedUntil)
	return v + "|" + boolStr(s.Cfg().PauseOnUsageLimit) + "|" + boolStr(s.claudePause() != "")
}

// handleClaudeResume is "Run now anyway": it lifts the pause and processes
// up to now (Process now). If Claude is still limited, the run pauses
// Claude work again and the notice says so.
func (s *Server) handleClaudeResume(w http.ResponseWriter, r *http.Request) {
	if err := skills.ClearPause(s.Store); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("web: Claude usage-limit pause lifted by hand; processing now")
	upto, err := s.Jobs.ProcessNow(r.Context())
	if upto.IsZero() {
		upto = time.Now()
	}
	http.Redirect(w, r, s.processedTo(upto, err), http.StatusSeeOther)
}
