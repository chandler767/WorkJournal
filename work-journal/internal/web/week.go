package web

import (
	"html/template"
	"net/http"
	"os"
	"regexp"
	"time"

	"workjournal/internal/config"
	"workjournal/internal/journal"
)

var weekRE = regexp.MustCompile(`^\d{4}-W\d{2}$`)

type weekDay struct {
	Day, Weekday string
	Totals       []totalRow
}

type weekPage struct {
	Title, Week, Prev, Next string
	Journal                 template.HTML
	Days                    []weekDay
}

// parseWeek validates an ISO week key and returns its Monday-Sunday days.
func parseWeek(v string) ([]string, bool) {
	if !weekRE.MatchString(v) {
		return nil, false
	}
	days, err := journal.WeekDays(v)
	return days, err == nil
}

func (s *Server) handleWeek(w http.ResponseWriter, r *http.Request) {
	week := r.PathValue("week")
	days, ok := parseWeek(week)
	if !ok {
		http.Error(w, "bad week", http.StatusBadRequest)
		return
	}
	monday, _ := time.Parse("2006-01-02", days[0])
	p := weekPage{Title: week, Week: week,
		Prev: config.WeekKey(monday.AddDate(0, 0, -7)),
		Next: config.WeekKey(monday.AddDate(0, 0, 7))}
	if b, err := os.ReadFile(s.Paths.WeeklyJournal(week)); err == nil {
		if p.Journal, err = renderMarkdown(string(b), s.Cfg().ListenAddr); err != nil {
			http.Error(w, "render journal: "+err.Error(), http.StatusInternalServerError)
			return
		}
	} else if !os.IsNotExist(err) {
		http.Error(w, "read journal: "+err.Error(), http.StatusInternalServerError)
		return
	}
	cfg := s.Cfg()
	colors := colorMap(cfg.Categories)
	for _, d := range days {
		totals, err := s.Store.CategoryTotals(d, d)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		t, _ := time.Parse("2006-01-02", d)
		p.Days = append(p.Days, weekDay{Day: d, Weekday: t.Format("Mon"), Totals: orderTotals(totals, cfg.Categories, colors)})
	}
	render(w, "week", p)
}

func (s *Server) handleWeekRollup(w http.ResponseWriter, r *http.Request) {
	week := r.PathValue("week")
	if _, ok := parseWeek(week); !ok {
		http.Error(w, "bad week", http.StatusBadRequest)
		return
	}
	if err := s.Jobs.Rollup(r.Context(), week); err != nil {
		http.Error(w, "rollup: "+err.Error(), http.StatusInternalServerError)
		return
	}
	redirectBack(w, r, "/week/"+week)
}
