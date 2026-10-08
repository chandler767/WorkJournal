package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"workjournal/internal/config"
)

// settingsForm holds the editable fields as text, so a rejected submission
// is shown back exactly as typed.
type settingsForm struct {
	RetentionDays, CloseTime, RollupWeekday, IdleMinutes, PauseAutoResume string
	ProcessInterval                                                       string
	ExcludedApps, ExcludedTitles, Categories, Rules                       string
}

type settingsPage struct {
	Title       string
	Form        settingsForm
	Error       string
	Notice      string
	Weekdays    []string
	Intervals   []string
	Suggestions []Suggestion
	Voices      []string
	ShowVoices  bool
}

var weekdayNames = []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}

var settingsNotices = map[string]string{
	"saved":     "Saved",
	"restart":   "Recorder restarting. It picks up the new exclusions when it comes back.",
	"norecord":  "The recorder isn't running, so there was nothing to restart.",
	"voicegone": "Voice profile deleted.",
}

// intervalChoices are the process_interval_minutes options, as text.
func intervalChoices() []string {
	out := make([]string, 0, len(config.ProcessIntervals))
	for _, v := range config.ProcessIntervals {
		out = append(out, strconv.Itoa(v))
	}
	return out
}

// titleWeekday canonicalises a weekday name ("friday" -> "Friday"), the
// spelling the select offers; unrecognised text is returned trimmed.
func titleWeekday(v string) string {
	v = strings.TrimSpace(v)
	for _, d := range weekdayNames {
		if strings.EqualFold(d, v) {
			return d
		}
	}
	return v
}

func formFromConfig(c config.Config) settingsForm {
	rules := make([]string, 0, len(c.Rules))
	for app, cat := range c.Rules {
		rules = append(rules, app+" = "+cat)
	}
	sort.Strings(rules)
	return settingsForm{
		RetentionDays:   strconv.Itoa(c.RetentionDays),
		CloseTime:       c.CloseTime,
		RollupWeekday:   titleWeekday(c.RollupWeekday),
		IdleMinutes:     strconv.Itoa(c.IdleMinutes),
		PauseAutoResume: strconv.Itoa(c.PauseAutoResumeMinutes),
		ProcessInterval: strconv.Itoa(c.ProcessIntervalMinutes),
		ExcludedApps:    strings.Join(c.ExcludedApps, "\n"),
		ExcludedTitles:  strings.Join(c.ExcludedTitleKeywords, "\n"),
		Categories:      strings.Join(c.Categories, "\n"),
		Rules:           strings.Join(rules, "\n"),
	}
}

// splitLines returns the trimmed, non-blank lines of v.
func splitLines(v string) []string {
	out := []string{}
	for _, l := range strings.Split(strings.ReplaceAll(v, "\r\n", "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// parseRules reads "app = Category" lines; the error names the bad line.
func parseRules(v string) (map[string]string, error) {
	rules := map[string]string{}
	for i, l := range strings.Split(strings.ReplaceAll(v, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		app, cat, ok := strings.Cut(l, "=")
		app, cat = strings.TrimSpace(app), strings.TrimSpace(cat)
		if !ok || app == "" || cat == "" {
			return nil, fmt.Errorf("rules line %d: expected \"app = Category\", got %q", i+1, strings.TrimSpace(l))
		}
		rules[app] = cat
	}
	return rules, nil
}

// applyForm builds a candidate config from base plus the submitted form.
func applyForm(base config.Config, f settingsForm) (config.Config, error) {
	c := base
	var err error
	if c.RetentionDays, err = strconv.Atoi(strings.TrimSpace(f.RetentionDays)); err != nil {
		return c, fmt.Errorf("retention_days must be a whole number, got %q", f.RetentionDays)
	}
	if c.IdleMinutes, err = strconv.Atoi(strings.TrimSpace(f.IdleMinutes)); err != nil {
		return c, fmt.Errorf("idle_minutes must be a whole number, got %q", f.IdleMinutes)
	}
	if c.PauseAutoResumeMinutes, err = strconv.Atoi(strings.TrimSpace(f.PauseAutoResume)); err != nil {
		return c, fmt.Errorf("pause_auto_resume_minutes must be a whole number, got %q", f.PauseAutoResume)
	}
	if v := strings.TrimSpace(f.ProcessInterval); v != "" { // absent from older forms: keep
		if c.ProcessIntervalMinutes, err = strconv.Atoi(v); err != nil {
			return c, fmt.Errorf("process_interval_minutes must be a whole number, got %q", f.ProcessInterval)
		}
	}
	c.CloseTime = strings.TrimSpace(f.CloseTime)
	c.RollupWeekday = titleWeekday(f.RollupWeekday)
	c.ExcludedApps = splitLines(f.ExcludedApps)
	c.ExcludedTitleKeywords = splitLines(f.ExcludedTitles)
	c.Categories = splitLines(f.Categories)
	if c.Rules, err = parseRules(f.Rules); err != nil {
		return c, err
	}
	for app, cat := range c.Rules {
		if !slices.Contains(c.Categories, cat) {
			return c, fmt.Errorf("rule %q uses category %q, which is not in the categories list", app, cat)
		}
	}
	return c, nil
}

func (s *Server) loadSuggestions() ([]Suggestion, error) {
	v, ok, err := s.Store.GetKV("rule_suggestions")
	if err != nil || !ok || v == "" {
		return nil, err
	}
	var list []Suggestion
	if err := json.Unmarshal([]byte(v), &list); err != nil {
		return nil, fmt.Errorf("parse rule_suggestions: %w", err)
	}
	return list, nil
}

func (s *Server) saveSuggestions(list []Suggestion) error {
	if list == nil {
		list = []Suggestion{}
	}
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return s.Store.SetKV("rule_suggestions", string(b))
}

func (s *Server) renderSettings(w http.ResponseWriter, form settingsForm, errMsg, notice string, code int) {
	cfg := s.Cfg()
	p := settingsPage{Title: "Settings", Form: form, Error: errMsg, Notice: notice,
		Weekdays: weekdayNames, Intervals: intervalChoices(), ShowVoices: cfg.Diarization != "off" && s.Voices != nil}
	var err error
	if p.Suggestions, err = s.loadSuggestions(); err != nil {
		log.Printf("web: %v", err)
	}
	if p.ShowVoices {
		if p.Voices, err = s.Voices.List(); err != nil {
			log.Printf("web: list voices: %v", err)
		}
	}
	if code != http.StatusOK {
		w.WriteHeader(code)
	}
	render(w, "settings", p)
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	notice := settingsNotices[q.Get("notice")]
	if q.Get("saved") != "" {
		notice = settingsNotices["saved"]
	}
	s.renderSettings(w, formFromConfig(s.Cfg()), "", notice, http.StatusOK)
}

func (s *Server) handleSettingsSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	f := settingsForm{
		RetentionDays: r.PostFormValue("retention_days"), CloseTime: r.PostFormValue("close_time"),
		RollupWeekday: r.PostFormValue("rollup_weekday"), IdleMinutes: r.PostFormValue("idle_minutes"),
		PauseAutoResume: r.PostFormValue("pause_auto_resume"), ExcludedApps: r.PostFormValue("excluded_apps"),
		ExcludedTitles: r.PostFormValue("excluded_titles"), Categories: r.PostFormValue("categories"),
		Rules: r.PostFormValue("rules"), ProcessInterval: r.PostFormValue("process_interval_minutes"),
	}
	c, err := applyForm(s.Cfg(), f)
	if err == nil {
		err = c.Validate()
	}
	if err != nil {
		s.renderSettings(w, f, err.Error(), "", http.StatusBadRequest)
		return
	}
	if err := s.SaveCfg(c); err != nil {
		s.renderSettings(w, f, err.Error(), "", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}

func (s *Server) handleRuleSuggestion(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	if action != "accept" && action != "dismiss" {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	app := strings.TrimSpace(r.PostFormValue("app"))
	list, err := s.loadSuggestions()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	i := slices.IndexFunc(list, func(g Suggestion) bool { return g.App == app })
	if i < 0 {
		http.Error(w, "no such suggestion", http.StatusNotFound)
		return
	}
	if action == "accept" {
		c := s.Cfg()
		rules := make(map[string]string, len(c.Rules)+1)
		for k, v := range c.Rules {
			rules[k] = v
		}
		if !slices.Contains(c.Categories, list[i].Category) {
			http.Error(w, fmt.Sprintf("category %q is no longer configured", list[i].Category), http.StatusBadRequest)
			return
		}
		rules[app] = list[i].Category
		c.Rules = rules
		if err := s.SaveCfg(c); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if err := s.saveSuggestions(slices.Delete(list, i, i+1)); err != nil {
		http.Error(w, "update suggestions: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (s *Server) handleVoiceDelete(w http.ResponseWriter, r *http.Request) {
	if s.Voices == nil || s.Cfg().Diarization == "off" {
		http.Error(w, "voice profiles are disabled (diarization is off)", http.StatusConflict)
		return
	}
	name := r.PathValue("name")
	names, err := s.Voices.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !slices.Contains(names, name) {
		http.NotFound(w, r)
		return
	}
	if err := s.Voices.Delete(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		http.Error(w, "delete voice: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings?notice=voicegone", http.StatusSeeOther)
}

func (s *Server) handleRestartRecorder(w http.ResponseWriter, r *http.Request) {
	to := "/settings?notice=restart"
	if err := s.Control.Send("quit"); err != nil {
		log.Printf("web: restart recorder: %v", err)
		to = "/settings?notice=norecord"
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}
