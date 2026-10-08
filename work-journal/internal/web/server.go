// Package web is the local dashboard: a small net/http server with
// html/template pages and embedded static assets (no CDN, no JS framework).
package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"workjournal/internal/capture"
	"workjournal/internal/config"
	"workjournal/internal/diarize"
	"workjournal/internal/process"
	"workjournal/internal/scheduler"
	"workjournal/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Server serves the dashboard. Binding to loopback is the caller's job.
type Server struct {
	Store   *store.Store
	Paths   config.Paths
	Cfg     func() config.Config
	SaveCfg func(config.Config) error
	Control capture.Control
	Jobs    scheduler.Jobs
	Voices  *diarize.Voices
	// SkillsMissing is set when no installed Claude Code skills were found:
	// hours then cannot be summarized and the day page says so.
	SkillsMissing bool
	nowFn         func() time.Time // test hook; time.Now when nil
}

func (s *Server) now() time.Time {
	if s.nowFn != nil {
		return s.nowFn()
	}
	return time.Now()
}

// Handler returns the dashboard's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // embedded tree is fixed at build time
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /{$}", s.handleRoot)
	mux.HandleFunc("GET /day/{date}", s.handleDay)
	mux.HandleFunc("GET /hour/{date}/{hh}", s.handleHour)
	mux.HandleFunc("GET /api/rev/day/{date}", s.handleRevDay)
	mux.HandleFunc("GET /api/rev/hour/{date}/{hh}", s.handleRevHour)
	mux.HandleFunc("GET /media/{date}/{kind}/{file}", s.handleMedia)
	mux.HandleFunc("GET /meeting/{id}", s.handleMeeting)
	mux.HandleFunc("GET /week/{week}", s.handleWeek)
	mux.HandleFunc("POST /week/{week}/rollup", s.sameOrigin(s.handleWeekRollup))
	mux.HandleFunc("GET /search", s.handleSearch)
	mux.HandleFunc("GET /settings", s.handleSettings)
	mux.HandleFunc("POST /settings", s.sameOrigin(s.handleSettingsSave))
	mux.HandleFunc("POST /settings/restart-recorder", s.sameOrigin(s.handleRestartRecorder))
	mux.HandleFunc("POST /settings/rule-suggestion/{action}", s.sameOrigin(s.handleRuleSuggestion))
	mux.HandleFunc("POST /settings/voice/{name}/delete", s.sameOrigin(s.handleVoiceDelete))
	mux.HandleFunc("POST /pause", s.sameOrigin(s.handlePause))
	mux.HandleFunc("POST /resume", s.sameOrigin(s.handleResume))
	mux.HandleFunc("POST /close-day/{date}", s.sameOrigin(s.handleCloseDay))
	mux.HandleFunc("POST /process-now", s.sameOrigin(s.handleProcessNow))
	mux.HandleFunc("POST /hour/{date}/{hh}/reprocess", s.sameOrigin(s.handleReprocess))
	mux.HandleFunc("POST /segment/category", s.sameOrigin(s.handleSegmentCategory))
	mux.HandleFunc("POST /meeting/{id}/speaker", s.sameOrigin(s.handleSpeaker))
	return localHostOnly(mux)
}

var dateRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// parseDate validates a YYYY-MM-DD path value and returns it with its time.
func parseDate(v string) (string, time.Time, bool) {
	if !dateRE.MatchString(v) {
		return "", time.Time{}, false
	}
	t, err := time.ParseInLocation("2006-01-02", v, time.Local)
	return v, t, err == nil
}

// parseHour validates a two-digit hour 00-23.
func parseHour(v string) (int, bool) {
	if len(v) != 2 {
		return 0, false
	}
	h, err := strconv.Atoi(v)
	return h, err == nil && h >= 0 && h <= 23
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/day/"+config.DayKey(time.Now()), http.StatusSeeOther)
}

// sameOrigin is the CSRF guard for POSTs: a present Origin must be this host.
func (s *Server) sameOrigin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" {
			u, err := url.Parse(o)
			if err != nil || u.Host != r.Host {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
		}
		next(w, r)
	}
}

// allowedReturn lists the page prefixes a Referer may send us back to.
var allowedReturn = []string{"/day/", "/hour/", "/meeting/", "/week/", "/settings", "/search"}

// safeReturn reports whether p is a same-site dashboard path: one leading
// slash, no "//" or backslash, and one of the known pages.
func safeReturn(p string) bool {
	if p == "" || p[0] != '/' || strings.Contains(p, "//") || strings.Contains(p, "\\") {
		return false
	}
	for _, pre := range allowedReturn {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

// redirectBack sends the browser back to the referring dashboard page, or
// to fallback when the Referer is absent or not a known page path.
func redirectBack(w http.ResponseWriter, r *http.Request, fallback string) {
	to := fallback
	if ref := r.Header.Get("Referer"); ref != "" {
		if u, err := url.Parse(ref); err == nil && safeReturn(u.Path) {
			to = u.Path
		}
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// localHostOnly is the DNS-rebinding guard: only loopback Host names pass.
func localHostOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		switch strings.Trim(host, "[]") {
		case "127.0.0.1", "localhost", "::1":
			next.ServeHTTP(w, r)
		default:
			http.Error(w, "forbidden host", http.StatusForbidden)
		}
	})
}

func (s *Server) handlePause(w http.ResponseWriter, r *http.Request) {
	if err := s.Control.Pause(); err != nil {
		http.Error(w, "pause: "+err.Error(), http.StatusBadGateway)
		return
	}
	redirectBack(w, r, "/day/"+config.DayKey(time.Now()))
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	if err := s.Control.Resume(); err != nil {
		http.Error(w, "resume: "+err.Error(), http.StatusBadGateway)
		return
	}
	redirectBack(w, r, "/day/"+config.DayKey(time.Now()))
}

func (s *Server) handleCloseDay(w http.ResponseWriter, r *http.Request) {
	day, _, ok := parseDate(r.PathValue("date"))
	if !ok {
		http.Error(w, "bad date", http.StatusBadRequest)
		return
	}
	if err := s.Jobs.CloseDay(r.Context(), day); err != nil {
		http.Error(w, "close day: "+err.Error(), http.StatusInternalServerError)
		return
	}
	redirectBack(w, r, "/day/"+day)
}

func (s *Server) handleReprocess(w http.ResponseWriter, r *http.Request) {
	day, _, ok := parseDate(r.PathValue("date"))
	hour, ok2 := parseHour(r.PathValue("hh"))
	if !ok || !ok2 {
		http.Error(w, "bad date or hour", http.StatusBadRequest)
		return
	}
	if err := s.Jobs.ReprocessHour(r.Context(), day, hour); err != nil {
		log.Printf("web: reprocess %s %02d: %v", day, hour, err)
		notice := "reprocess-failed"
		switch {
		case errors.Is(err, process.ErrChunkNotReady):
			notice = "reprocess-not-ready"
		case errors.Is(err, scheduler.ErrMediaExpired):
			notice = "reprocess-expired"
		}
		http.Redirect(w, r, "/day/"+day+"?notice="+notice, http.StatusSeeOther)
		return
	}
	redirectBack(w, r, "/day/"+day)
}

// ---- rendering helpers ----

var funcs = template.FuncMap{
	"dur":      fmtDur,
	"urlq":     url.PathEscape,
	"thisWeek": func() string { return config.WeekKey(time.Now()) },
	"div60":    func(n int) int { return n / 60 },
	"mod60":    func(n int) int { return n % 60 },
	"code":     codeSegs,
	"plural":   plural,
}

// plural formats a count with its noun: "1 note", "12 notes".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func fmtDur(d time.Duration) string {
	m := int(d.Round(time.Minute) / time.Minute)
	if m < 60 {
		return fmt.Sprintf("%dm", m)
	}
	if m%60 == 0 {
		return fmt.Sprintf("%dh", m/60)
	}
	return fmt.Sprintf("%dh %dm", m/60, m%60)
}

// render executes the named page inside the layout, buffering so a template
// error becomes a clean 500 instead of a half-written page.
func render(w http.ResponseWriter, page string, data any) {
	t, err := template.New(page).Funcs(funcs).ParseFS(templateFS, "templates/layout.html", "templates/"+page+".html")
	if err != nil {
		http.Error(w, "template: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		log.Printf("web: render %s: %v", page, err)
		http.Error(w, "template: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

// Eight colour-blind-safe colours, assigned in config category order.
var palette = []string{"#0072B2", "#E69F00", "#009E73", "#CC79A7", "#56B4E9", "#D55E00", "#6A5ACD", "#8C6D31"}

const otherColor = "#7A808C"

// colorMap assigns palette colours to categories in configured order.
func colorMap(cats []string) map[string]string {
	m := make(map[string]string, len(cats))
	for i, c := range cats {
		if i < len(palette) {
			m[c] = palette[i]
		} else {
			m[c] = otherColor
		}
	}
	return m
}

func colorOf(m map[string]string, cat string) string {
	if c, ok := m[cat]; ok {
		return c
	}
	return otherColor
}

// mustJSON marshals v for a data attribute (html/template escapes it).
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}
