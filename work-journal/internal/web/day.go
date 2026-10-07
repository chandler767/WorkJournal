package web

import (
	"encoding/json"
	"html/template"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"

	"workjournal/internal/config"
	"workjournal/internal/scheduler"
	"workjournal/internal/skills"
	"workjournal/internal/store"
)

type totalRow struct {
	Name, Color string
	Dur         time.Duration
	Pct         int
}

type hourRow struct {
	Hour        int
	Label       string
	Status      string
	NeedsReview bool
	Pending     bool
	Summary     skills.HourSummary
}

type meetingRow struct {
	ID, Title, App, Time string
}

type tlSeg struct {
	S int    `json:"s"` // start, minutes since midnight
	E int    `json:"e"`
	C string `json:"c"` // colour
	L string `json:"l"` // tooltip label
	H int    `json:"h"` // hour, for linking
}

type timelineSpec struct {
	From int     `json:"from"` // first hour shown
	To   int     `json:"to"`   // last hour shown (exclusive)
	Day  string  `json:"day"`
	Segs []tlSeg `json:"segs"`
}

type dayPage struct {
	Title, Date, Prev, Next string
	IsToday                 bool
	RecorderUp              bool
	Paused                  bool
	StatusText              string
	DiskWarn                string
	SkillsMissing           bool
	CloseFailed             bool
	Journal                 template.HTML
	Totals                  []totalRow
	Hours                   []hourRow
	Meetings                []meetingRow
	Timeline                string
}

func (s *Server) handleDay(w http.ResponseWriter, r *http.Request) {
	day, t, ok := parseDate(r.PathValue("date"))
	if !ok {
		http.Error(w, "bad date", http.StatusBadRequest)
		return
	}
	cfg := s.Cfg()
	colors := colorMap(cfg.Categories)
	p := dayPage{
		Title: day, Date: day,
		Prev:    config.DayKey(t.AddDate(0, 0, -1)),
		Next:    config.DayKey(t.AddDate(0, 0, 1)),
		IsToday: day == config.DayKey(time.Now()),
	}
	p.RecorderUp, p.Paused, p.StatusText = s.recorderState()
	if v, _, err := s.Store.GetKV("disk_state"); err == nil && v != "" && v != "normal" {
		p.DiskWarn = v
	}
	if b, err := os.ReadFile(s.Paths.DailyJournal(day)); err == nil {
		if p.Journal, err = renderMarkdown(string(b)); err != nil {
			http.Error(w, "render journal: "+err.Error(), http.StatusInternalServerError)
			return
		}
	} else if !os.IsNotExist(err) {
		http.Error(w, "read journal: "+err.Error(), http.StatusInternalServerError)
		return
	} else if v, _, kerr := s.Store.GetKV(scheduler.CloseFailuresKey(day)); kerr == nil {
		n, _ := strconv.Atoi(v)
		p.CloseFailed = n > 0
	}
	p.SkillsMissing = s.SkillsMissing

	hours, err := s.Store.HoursOn(day)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, h := range hours {
		row := hourRow{Hour: h.Hour, Label: hourLabel(h.Hour), Status: h.Status,
			NeedsReview: h.Status == store.StatusNeedsReview,
			Pending:     h.Status == store.StatusPending || h.Status == store.StatusTranscribed}
		if h.SummaryJSON != "" {
			if err := json.Unmarshal([]byte(h.SummaryJSON), &row.Summary); err != nil {
				row.Summary.Notes = []string{"(summary unreadable: " + err.Error() + ")"}
			}
		}
		p.Hours = append(p.Hours, row)
	}

	totals, err := s.Store.CategoryTotals(day, day)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	p.Totals = orderTotals(totals, cfg.Categories, colors)

	meets, err := s.Store.MeetingsOn(day)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, m := range meets {
		p.Meetings = append(p.Meetings, meetingRow{ID: m.ID, Title: m.Title, App: m.App,
			Time: m.Start.Format("15:04") + "–" + m.End.Format("15:04")})
	}

	segs, err := s.Store.SegmentsOn(day)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	p.Timeline = mustJSON(buildTimeline(day, segs, colors))
	render(w, "day", p)
}

// recorderState asks the recorder; failure is shown, not fatal.
func (s *Server) recorderState() (up, paused bool, text string) {
	st, err := s.Control.Status()
	if err != nil {
		return false, false, "Recorder not running"
	}
	return true, st.State == "paused", st.State
}

func hourLabel(h int) string {
	return time.Date(2000, 1, 1, h, 0, 0, 0, time.UTC).Format("15:00")
}

func orderTotals(totals map[string]time.Duration, order []string, colors map[string]string) []totalRow {
	var names []string
	seen := map[string]bool{}
	for _, c := range order {
		if totals[c] > 0 {
			names = append(names, c)
			seen[c] = true
		}
	}
	var extra []string
	for c, d := range totals {
		if !seen[c] && d > 0 {
			extra = append(extra, c)
		}
	}
	sort.Strings(extra)
	names = append(names, extra...)
	var max time.Duration
	for _, n := range names {
		if totals[n] > max {
			max = totals[n]
		}
	}
	rows := make([]totalRow, 0, len(names))
	for _, n := range names {
		pct := 0
		if max > 0 {
			pct = int(totals[n] * 100 / max)
		}
		rows = append(rows, totalRow{Name: n, Color: colorOf(colors, n), Dur: totals[n], Pct: pct})
	}
	return rows
}

// buildTimeline spans 06:00–22:00, widened to the hours that hold data.
func buildTimeline(day string, segs []store.SegmentRecord, colors map[string]string) timelineSpec {
	spec := timelineSpec{From: 6, To: 22, Day: day, Segs: []tlSeg{}}
	for _, g := range segs {
		s := g.Start.Local().Hour()*60 + g.Start.Local().Minute()
		e := s + int(g.End.Sub(g.Start).Round(time.Minute)/time.Minute)
		if e <= s {
			e = s + 1
		}
		if e > 24*60 {
			e = 24 * 60
		}
		if s/60 < spec.From {
			spec.From = s / 60
		}
		if (e+59)/60 > spec.To {
			spec.To = (e + 59) / 60
		}
		label := g.Category + " · " + g.App
		if g.Title != "" {
			label += " · " + g.Title
		}
		spec.Segs = append(spec.Segs, tlSeg{S: s, E: e, C: colorOf(colors, g.Category), L: label, H: g.Hour})
	}
	return spec
}
