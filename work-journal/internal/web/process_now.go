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
	if upto.IsZero() {
		upto = time.Now()
	}
	to := "/day/" + config.DayKey(upto)
	if err != nil {
		log.Printf("web: process now: %v", err)
		to += "?notice=process-failed"
	} else {
		to += "?notice=processed&upto=" + upto.Format("15:04")
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
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
