package web

import (
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"workjournal/internal/config"
)

// handleProcessNow processes up to the current moment (scheduler.Jobs
// ProcessNow) and sends the browser to that day with a notice.
func (s *Server) handleProcessNow(w http.ResponseWriter, r *http.Request) {
	upto, err := s.Jobs.ProcessNow(r.Context())
	http.Redirect(w, r, s.processedTo(upto, err), http.StatusSeeOther)
}

// processedTo is the day page a Process now run returns to, with a notice:
// that Claude is still usage-limited when the run paused Claude work again,
// else whether it worked.
func (s *Server) processedTo(upto time.Time, err error) string {
	if upto.IsZero() {
		upto = time.Now()
	}
	to := "/day/" + config.DayKey(upto)
	switch {
	case s.claudePause() != "":
		if err != nil {
			log.Printf("web: process now: %v", err)
		}
		to += "?notice=claude-still-limited"
	case err != nil:
		log.Printf("web: process now: %v", err)
		to += "?notice=process-failed"
	default:
		to += "?notice=processed&upto=" + upto.Format("15:04")
	}
	return to
}

var clockRE = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// dayNotice maps the ?notice= flag of the day page to fixed text; unknown
// values show nothing and only a valid HH:MM upto is shown.
func dayNotice(q url.Values) string {
	switch q.Get("notice") {
	case "processed":
		if upto := q.Get("upto"); clockRE.MatchString(upto) {
			return "Processed up to " + upto + "."
		}
		return "Processed."
	case "process-failed":
		return "Processing failed — see the log. Anything that finished was kept."
	case "claude-still-limited":
		return "Claude is still at its usage limit, so summaries stay paused. Audio is transcribed and cached locally, so nothing is lost; hours with speech stay pending until Claude resumes."
	case "reprocess-not-ready":
		return "That hour's audio is still being recorded — try again in a few minutes."
	case "reprocess-expired":
		return "Cannot reprocess: the hour's raw recordings were already removed."
	case "reprocess-failed":
		return "Reprocessing failed — see the log."
	}
	return ""
}

// partialNote describes an in-progress hour's summary.
func partialNote(intervalMinutes int) string {
	if intervalMinutes > 0 && intervalMinutes < 60 {
		return "Summary so far — updates every " + strconv.Itoa(intervalMinutes) + " min."
	}
	return "Summary so far — the full summary follows when the hour ends."
}
