package web

import (
	"encoding/json"
	"html/template"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"workjournal/internal/config"
	"workjournal/internal/process"
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
	Partial     bool
	// Empty is a finished hour with nothing in its summary: one muted line.
	Empty bool
	// Reprocess is offered only where it helps: needs_review, partial and
	// transcribed (stuck before summarizing) hours.
	Reprocess bool
	// Removed: the privacy filter removed some of the hour's speech.
	Removed bool
	Summary skills.HourSummary
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
	PrevShort, NextShort    string // "Oct 7", for narrow screens
	IsToday                 bool
	RecorderUp              bool
	Paused                  bool
	StatusText              string
	AudioText               string        // "Audio: Auto · listening"; "" from an older recorder
	AudioModes              []audioChoice // the On / Off / Auto control; nil hides it
	DiskWarn                string
	SkillsMissing           bool
	CloseFailed             bool
	Notice                  string
	Journal                 template.HTML
	Totals                  []totalRow
	Hours                   []hourRow
	Meetings                []meetingRow
	Timeline                string
	Rev                     string
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
		Prev:      config.DayKey(t.AddDate(0, 0, -1)),
		Next:      config.DayKey(t.AddDate(0, 0, 1)),
		PrevShort: t.AddDate(0, 0, -1).Format("Jan 2"),
		NextShort: t.AddDate(0, 0, 1).Format("Jan 2"),
		IsToday:   day == config.DayKey(s.now()),
	}
	rev, err := s.dayRev(day) // before reading anything: a later change then just triggers one more refresh
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	p.Rev = rev
	p.Notice = dayNotice(r.URL.Query())
	rv := s.recorder()
	p.RecorderUp, p.Paused, p.StatusText, p.AudioText = rv.Up, rv.Paused, rv.Text, rv.AudioText
	if rv.Up && rv.AudioMode != "" {
		p.AudioModes = audioChoices(rv.AudioMode)
	}
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
			Pending:     h.Status == store.StatusPending || h.Status == store.StatusTranscribed,
			Partial:     h.Status == store.StatusPartial}
		row.Reprocess = row.NeedsReview || row.Partial || h.Status == store.StatusTranscribed
		if h.SummaryJSON != "" {
			if err := json.Unmarshal([]byte(h.SummaryJSON), &row.Summary); err != nil {
				row.Summary.Notes = []string{"(summary unreadable: " + err.Error() + ")"}
			}
		}
		row.Empty = !row.NeedsReview && !row.Partial && !row.Pending && summaryEmpty(row.Summary)
		if !row.Empty {
			if doc, err := process.Load(s.Paths.DerivedHour(day, h.Hour)); err == nil {
				row.Removed = len(doc.Removed) > 0
			}
		}
		p.Hours = append(p.Hours, row)
	}
	// Newest first: the current hour is on top.
	for i, j := 0, len(p.Hours)-1; i < j; i, j = i+1, j-1 {
		p.Hours[i], p.Hours[j] = p.Hours[j], p.Hours[i]
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
	for i, j := 0, len(p.Meetings)-1; i < j; i, j = i+1, j-1 {
		p.Meetings[i], p.Meetings[j] = p.Meetings[j], p.Meetings[i]
	}

	segs, err := s.Store.SegmentsOn(day)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	p.Timeline = mustJSON(buildTimeline(day, segs, colors))
	render(w, "day", p)
}

// recorderView is what the day page shows about the recorder.
type recorderView struct {
	Up, Paused bool
	Text       string // recorder state, or "Recorder not running"
	AudioMode  string // "on", "off", "auto"; "" from an older recorder
	AudioText  string
	// rev fingerprints everything above plus whether audio is active.
	rev string
}

// recorder asks the recorder for its state; failure is shown, not fatal.
func (s *Server) recorder() recorderView {
	st, err := s.Control.Status()
	if err != nil {
		return recorderView{Text: "Recorder not running", rev: "down"}
	}
	return recorderView{Up: true, Paused: st.State == "paused", Text: st.State, AudioMode: st.AudioMode,
		AudioText: audioText(st.AudioMode, st.AudioActive, st.AudioReason),
		rev:       strings.Join([]string{st.State, st.AudioMode, boolStr(st.AudioActive), st.AudioReason}, "/")}
}

// audioText is the status line for the recorder's audio mode: "Audio: On",
// "Audio: Off", "Audio: Auto · listening" or "Audio: Auto · recording (call)".
func audioText(mode string, active bool, reason string) string {
	switch mode {
	case "on":
		return "Audio: On"
	case "off":
		return "Audio: Off"
	case "auto":
		if !active {
			return "Audio: Auto · listening"
		}
		switch reason {
		case "call":
			return "Audio: Auto · recording (call)"
		case "in_person":
			return "Audio: Auto · recording (in person)"
		}
		return "Audio: Auto · recording"
	}
	return ""
}

// audioChoice is one button of the On / Off / Auto control.
type audioChoice struct {
	Value, Label string
	Current      bool
}

func audioChoices(current string) []audioChoice {
	out := make([]audioChoice, 0, len(config.AudioModes))
	for _, m := range config.AudioModes {
		out = append(out, audioChoice{Value: m, Label: strings.ToUpper(m[:1]) + m[1:], Current: m == current})
	}
	return out
}

func summaryEmpty(h skills.HourSummary) bool {
	return h.Narrative == "" && h.MainFocus == "" && len(h.Notes) == 0 && len(h.ActionItems) == 0 && len(h.Timeline) == 0
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
