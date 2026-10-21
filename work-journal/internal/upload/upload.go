// Package upload is the phone upload page: a second, opt-in listener
// (upload_addr) that serves only a form for importing a recording from a
// phone's browser on the local network. It shares no routes with the
// dashboard, which stays loopback-only: no day pages, recordings,
// transcripts or MCP. Every request needs upload_token; the first visit
// asks for it and keeps it in an HttpOnly cookie, so it never sits in a URL.
package upload

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"

	"workjournal/internal/config"
	"workjournal/internal/process"
	"workjournal/internal/store"
)

//go:embed page.html
var pageFS embed.FS

//go:embed script.js
var script string

var page = template.Must(template.ParseFS(pageFS, "page.html"))

// scriptHash allows the page's one inline script in the CSP.
var scriptHash = func() string {
	h := sha256.Sum256([]byte(script))
	return base64.StdEncoding.EncodeToString(h[:])
}()

// CookieName holds the upload token in the phone's browser.
const CookieName = "journal_upload"

// cookieAge is how long the phone stays signed in.
const cookieAge = 90 * 24 * time.Hour

// formSlack is room in a request for the form fields besides the file.
const formSlack = 1 << 20

// csp keeps the page to itself: its own inline script (by hash) and style,
// no other origin, no framing.
const csp = "default-src 'none'; script-src 'sha256-%s'; style-src 'unsafe-inline'; img-src data:; " +
	"form-action 'self'; frame-ancestors 'none'; base-uri 'none'"

// Server serves the upload page.
type Server struct {
	Token string // upload_token; never empty when serving
	Paths config.Paths
	Store *store.Store
	// Import stores an upload (process.Import); tests may replace it.
	Import func(req process.ImportRequest) (store.MeetingRecord, error)
	Logf   func(string, ...any) // nil: log.Printf
	// MaxBytes caps an uploaded file; 0 means process.MaxImportBytes.
	MaxBytes int64
}

func (s *Server) maxBytes() int64 {
	if s.MaxBytes > 0 {
		return s.MaxBytes
	}
	return process.MaxImportBytes
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// Handler returns the upload routes, every one behind the token.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleForm)
	mux.HandleFunc("POST /upload", s.handleUpload)
	mux.HandleFunc("POST /token", s.handleToken)
	return s.headers(s.auth(mux))
}

// headers sets the page policy on every response.
func (s *Server) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", fmt.Sprintf(csp, scriptHash))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// validToken compares in constant time.
func (s *Server) validToken(t string) bool {
	if s.Token == "" || t == "" {
		return false
	}
	a, b := sha256.Sum256([]byte(t)), sha256.Sum256([]byte(s.Token))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// auth lets through only requests carrying the token cookie, and POST
// /token, which sets it. Everything else is 401; a browser asking for a
// page gets the token prompt with it.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/token" {
			next.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie(CookieName); err == nil && s.validToken(c.Value) {
			next.ServeHTTP(w, r)
			return
		}
		s.prompt(w, r, "", http.StatusUnauthorized)
	})
}

// prompt answers 401 with the token form (or plain text to a non-browser).
func (s *Server) prompt(w http.ResponseWriter, r *http.Request, msg string, code int) {
	if r.Method != http.MethodGet || !strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.Error(w, "unauthorized: the upload token is required", code)
		return
	}
	s.render(w, code, pageData{NeedToken: true, Error: msg})
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if !s.validToken(strings.TrimSpace(r.PostFormValue("token"))) {
		s.logf("upload: wrong token from %s", r.RemoteAddr)
		s.render(w, http.StatusUnauthorized, pageData{NeedToken: true, Error: "That token is not right."})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: s.Token, Path: "/", MaxAge: int(cookieAge.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

type pageData struct {
	NeedToken bool
	Error     string
	Done      *doneInfo
	Accept    string // the file input's accept list
	Exts      string // the accepted types, for the page text
	Script    template.JS
}

type doneInfo struct {
	Title, When string
}

func (s *Server) render(w http.ResponseWriter, code int, d pageData) {
	d.Accept = strings.Join(process.ImportExts, ",") + ",audio/*"
	d.Exts = strings.Join(process.ImportExts, ", ")
	d.Script = template.JS(script)
	var buf bytes.Buffer
	if err := page.Execute(&buf, d); err != nil {
		s.logf("upload: render: %v", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) handleForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, pageData{})
}

// formError renders the form with msg and the status.
func (s *Server) formError(w http.ResponseWriter, code int, msg string) {
	s.render(w, code, pageData{Error: msg})
}

// handleUpload streams the file to a temp file under imports/ (so the
// move into place is a rename), checks it and imports it.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength > s.maxBytes()+formSlack {
		s.formError(w, http.StatusRequestEntityTooLarge, "That file is over the 1 GB limit.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBytes()+formSlack)
	mr, err := r.MultipartReader()
	if err != nil {
		s.formError(w, http.StatusBadRequest, "The upload was not a form.")
		return
	}
	var req process.ImportRequest
	var startText, tz string
	defer func() {
		if req.Path != "" {
			os.Remove(req.Path)
		}
	}()
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			s.uploadFailed(w, err)
			return
		}
		switch part.FormName() {
		case "audio":
			if req.Path != "" || part.FileName() == "" {
				part.Close()
				continue
			}
			req.Name = part.FileName()
			if err := process.CheckImportName(req.Name); err != nil {
				part.Close()
				s.formError(w, http.StatusUnsupportedMediaType, err.Error())
				return
			}
			if req.Path, err = s.save(part); err != nil {
				s.uploadFailed(w, err)
				return
			}
		case "start":
			startText, err = field(part, 100)
		case "tz":
			tz, err = field(part, 100)
		case "title":
			req.Title, err = field(part, 4*process.MaxImportTitle)
		case "context":
			req.Context, err = field(part, 4*process.MaxImportContext)
		default:
			part.Close()
		}
		if err != nil {
			s.uploadFailed(w, err)
			return
		}
	}
	if req.Path == "" {
		s.formError(w, http.StatusBadRequest, "Choose an audio file to upload.")
		return
	}
	if req.Start, err = ParseStart(startText, tz); err != nil {
		s.formError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Move = true
	m, err := s.Import(req)
	if err != nil {
		s.logf("upload: import %s: %v", req.Name, err)
		s.formError(w, http.StatusBadRequest, "Import failed: "+err.Error())
		return
	}
	req.Path = "" // moved into the import's folder
	s.logf("upload: imported %s as meeting %s", req.Name, m.ID)
	s.render(w, http.StatusOK, pageData{Done: &doneInfo{Title: m.Title, When: m.Start.Format("Mon Jan 2, 15:04")}})
}

// uploadFailed reports a read error: over the size cap, or the connection.
func (s *Server) uploadFailed(w http.ResponseWriter, err error) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) || errors.Is(err, errTooBig) {
		s.formError(w, http.StatusRequestEntityTooLarge, "That file is over the 1 GB limit.")
		return
	}
	s.logf("upload: %v", err)
	s.formError(w, http.StatusBadRequest, "The upload did not finish: "+err.Error())
}

var errTooBig = errors.New("file over the size limit")

// save writes the part to a temp file under imports/, at most maxBytes.
func (s *Server) save(part *multipart.Part) (string, error) {
	defer part.Close()
	if err := os.MkdirAll(s.Paths.Imports(), 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(s.Paths.Imports(), ".upload-*")
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, io.LimitReader(part, s.maxBytes()+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > s.maxBytes() {
		err = errTooBig
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// field reads a text part of at most max bytes.
func field(part *multipart.Part, max int64) (string, error) {
	defer part.Close()
	b, err := io.ReadAll(io.LimitReader(part, max+1))
	if err != nil {
		return "", err
	}
	if int64(len(b)) > max {
		return "", fmt.Errorf("the %s field is too long", part.FormName())
	}
	return string(b), nil
}

// ParseStart reads the form's start time: an RFC 3339 time, or a
// datetime-local value ("2026-10-21T18:30") in the phone's IANA time zone
// tz (the Mac's when tz is empty or unknown).
func ParseStart(v, tz string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, errors.New("The start time is required.")
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	loc := time.Local
	if tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, v, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("The start time %q is not a date and time.", v)
}
