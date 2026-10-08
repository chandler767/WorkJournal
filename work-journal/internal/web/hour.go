package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"workjournal/internal/process"
	"workjournal/internal/skills"
	"workjournal/internal/store"
)

type utterRow struct {
	Offset  int // seconds from the start of the hour
	Clock   string
	Speaker string
	Track   string
	Text    string
}

type audioRow struct {
	Name, Track string
	Start       int // seconds from the start of the hour
}

type screenRow struct {
	File, Clock string
}

type segRow struct {
	Start, Clock, App, Title, Category, Source string
	Dur                                        time.Duration
	Color                                      string
}

type hourPage struct {
	Title, Date, Label, Status string
	Hour                       string
	NeedsReview                bool
	PartialNote                string
	Summary                    skills.HourSummary
	HasSummary                 bool
	Transcript                 []utterRow
	Audio                      []audioRow
	Screens                    []screenRow
	ScreensJSON                string
	Segments                   []segRow
	Categories                 []string
	DocNote                    string
	Rev                        string
}

var audioNameRE = regexp.MustCompile(`^(\d{2})(\d{2})(\d{2})?-(mic|system)\.m4a$`)

func (s *Server) handleHour(w http.ResponseWriter, r *http.Request) {
	day, _, ok := parseDate(r.PathValue("date"))
	hour, ok2 := parseHour(r.PathValue("hh"))
	if !ok || !ok2 {
		http.Error(w, "bad date or hour", http.StatusBadRequest)
		return
	}
	cfg := s.Cfg()
	colors := colorMap(cfg.Categories)
	hh := fmt.Sprintf("%02d", hour)
	p := hourPage{Title: day + " " + hh + ":00", Date: day, Hour: hh, Label: hourLabel(hour), Categories: cfg.Categories}

	rev, err := s.hourRev(day, hour)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	p.Rev = rev

	rec, found, err := s.Store.Hour(day, hour)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if found {
		p.Status = rec.Status
		p.NeedsReview = rec.Status == store.StatusNeedsReview
		if rec.Status == store.StatusPartial {
			p.PartialNote = partialNote(cfg.ProcessIntervalMinutes)
		}
		if rec.SummaryJSON != "" {
			if err := json.Unmarshal([]byte(rec.SummaryJSON), &p.Summary); err == nil {
				p.HasSummary = true
			} else {
				p.DocNote = "Summary unreadable: " + err.Error()
			}
		}
	}

	_, dayStart, _ := parseDate(day)
	hourStart := dayStart.Add(time.Duration(hour) * time.Hour)
	doc, err := process.Load(s.Paths.DerivedHour(day, hour))
	switch {
	case err == nil:
		for _, u := range doc.Transcript {
			off := int(u.Start.Sub(hourStart) / time.Second)
			if off < 0 {
				off = 0
			}
			p.Transcript = append(p.Transcript, utterRow{Offset: off, Clock: u.Start.Local().Format("15:04:05"),
				Speaker: u.Speaker, Track: u.Track, Text: u.Text})
		}
		for _, sc := range doc.Screens {
			p.Screens = append(p.Screens, screenRow{File: filepath.Base(sc.File), Clock: sc.Time.Local().Format("15:04:05")})
		}
	case errors.Is(err, os.ErrNotExist):
		p.DocNote = "No derived data for this hour yet."
	default:
		p.DocNote = "Derived data unreadable: " + err.Error()
	}
	files := make([]string, 0, len(p.Screens))
	for _, sc := range p.Screens {
		files = append(files, sc.File)
	}
	p.ScreensJSON = mustJSON(map[string]any{"day": day, "files": files})
	p.Audio = s.audioFor(day, hh)

	segs, err := s.Store.SegmentsOn(day)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, g := range segs {
		if g.Hour != hour {
			continue
		}
		p.Segments = append(p.Segments, segRow{Start: g.Start.Format(time.RFC3339Nano), Clock: g.Start.Local().Format("15:04"),
			App: g.App, Title: g.Title, Category: g.Category, Source: g.Source, Dur: g.End.Sub(g.Start), Color: colorOf(colors, g.Category)})
	}
	render(w, "hour", p)
}

// audioFor lists the hour's audio chunks (HHMM-<track>.m4a or HHMMSS-…).
func (s *Server) audioFor(day, hh string) []audioRow {
	entries, err := os.ReadDir(s.Paths.Audio(day))
	if err != nil {
		return nil
	}
	var out []audioRow
	for _, e := range entries {
		m := audioNameRE.FindStringSubmatch(e.Name())
		if m == nil || m[1] != hh {
			continue
		}
		mm, _ := strconv.Atoi(m[2])
		ss := 0
		if m[3] != "" {
			ss, _ = strconv.Atoi(m[3])
		}
		out = append(out, audioRow{Name: e.Name(), Track: m[4], Start: mm*60 + ss})
	}
	return out
}

func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request) {
	day, _, ok := parseDate(r.PathValue("date"))
	kind, file := r.PathValue("kind"), r.PathValue("file")
	if !ok || (kind != "audio" && kind != "screens") || !safeName(file) {
		http.Error(w, "bad media path", http.StatusBadRequest)
		return
	}
	dir := s.Paths.Audio(day)
	if kind == "screens" {
		dir = s.Paths.Screens(day)
	}
	path := filepath.Join(dir, file)
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		http.Error(w, "raw media expired", http.StatusGone)
		return
	}
	http.ServeFile(w, r, path)
}

func safeName(f string) bool {
	if f == "" || f == "." {
		return false
	}
	for i := 0; i < len(f); i++ {
		if f[i] == '/' || f[i] == '\\' || f[i] == 0 {
			return false
		}
	}
	for i := 0; i+1 < len(f); i++ {
		if f[i] == '.' && f[i+1] == '.' {
			return false
		}
	}
	return true
}

func (s *Server) handleSegmentCategory(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	day, _, ok := parseDate(r.PostFormValue("date"))
	hour, ok2 := parseHour(r.PostFormValue("hour"))
	start, err := time.Parse(time.RFC3339Nano, r.PostFormValue("start"))
	category := r.PostFormValue("category")
	if !ok || !ok2 || err != nil || !contains(s.Cfg().Categories, category) {
		http.Error(w, "bad date, hour, start or category", http.StatusBadRequest)
		return
	}
	app := s.segmentApp(day, hour, start)
	if err := s.Store.SetSegmentCategory(day, hour, start, category); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if app != "" {
		if err := s.addSuggestion(app, category); err != nil {
			http.Error(w, "save suggestion: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	redirectBack(w, r, fmt.Sprintf("/hour/%s/%02d", day, hour))
}

func (s *Server) segmentApp(day string, hour int, start time.Time) string {
	segs, err := s.Store.SegmentsOn(day)
	if err != nil {
		return ""
	}
	for _, g := range segs {
		if g.Hour == hour && g.Start.Equal(start) {
			return g.App
		}
	}
	return ""
}

// Suggestion is a proposed categorisation rule, kept in KV rule_suggestions.
type Suggestion struct {
	App      string `json:"app"`
	Category string `json:"category"`
}

// addSuggestion records app -> category, replacing any earlier one for app.
func (s *Server) addSuggestion(app, category string) error {
	var list []Suggestion
	if v, ok, err := s.Store.GetKV("rule_suggestions"); err != nil {
		return err
	} else if ok && v != "" {
		if err := json.Unmarshal([]byte(v), &list); err != nil {
			return fmt.Errorf("parse rule_suggestions: %w", err)
		}
	}
	replaced := false
	for i := range list {
		if list[i].App == app {
			list[i].Category, replaced = category, true
		}
	}
	if !replaced {
		list = append(list, Suggestion{App: app, Category: category})
	}
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return s.Store.SetKV("rule_suggestions", string(b))
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
