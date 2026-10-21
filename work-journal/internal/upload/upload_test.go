package upload

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workjournal/internal/config"
	"workjournal/internal/process"
	"workjournal/internal/store"
)

const token = "correct-horse-battery-staple"

type upEnv struct {
	t     *testing.T
	s     *Server
	paths config.Paths
	st    *store.Store
	srv   *httptest.Server
}

func newUpEnv(t *testing.T) *upEnv {
	t.Helper()
	paths := config.Paths{Root: t.TempDir()}
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.DB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := &Server{Token: token, Paths: paths, Store: st, Logf: t.Logf,
		Import: func(req process.ImportRequest) (store.MeetingRecord, error) {
			return process.Import(paths, st, req, time.Now())
		}}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return &upEnv{t: t, s: s, paths: paths, st: st, srv: srv}
}

// client does not follow redirects, so the cookie and Location are visible.
func client() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (e *upEnv) do(req *http.Request) (*http.Response, string) {
	e.t.Helper()
	resp, err := client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func (e *upEnv) get(path string, cookie *http.Cookie) (*http.Response, string) {
	e.t.Helper()
	req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
	req.Header.Set("Accept", "text/html")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return e.do(req)
}

func goodCookie() *http.Cookie { return &http.Cookie{Name: CookieName, Value: token} }

// form builds a multipart upload with an audio file of size bytes.
func form(t *testing.T, name string, size int, fields map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		w.WriteField(k, v)
	}
	if name != "" {
		fw, err := w.CreateFormFile("audio", name)
		if err != nil {
			t.Fatal(err)
		}
		fw.Write(bytes.Repeat([]byte{'a'}, size))
	}
	w.Close()
	return &buf, w.FormDataContentType()
}

func (e *upEnv) upload(cookie *http.Cookie, name string, size int, fields map[string]string) (*http.Response, string) {
	e.t.Helper()
	body, ct := form(e.t, name, size, fields)
	req, _ := http.NewRequest("POST", e.srv.URL+"/upload", body)
	req.Header.Set("Content-Type", ct)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return e.do(req)
}

func TestEveryRouteNeedsTheToken(t *testing.T) {
	e := newUpEnv(t)
	bad := &http.Cookie{Name: CookieName, Value: "wrong-token-wrong-token"}
	paths := []string{"/", "/upload", "/day/2026-10-07", "/meeting/x", "/media/2026-10-07/audio/0900-mic.m4a",
		"/static/app.css", "/search?q=x", "/settings", "/mcp", "/?token=" + token}
	for _, c := range []*http.Cookie{nil, bad} {
		for _, p := range paths {
			for _, method := range []string{"GET", "POST"} {
				req, _ := http.NewRequest(method, e.srv.URL+p, nil)
				if c != nil {
					req.AddCookie(c)
				}
				if resp, _ := e.do(req); resp.StatusCode != http.StatusUnauthorized {
					t.Errorf("%s %s (cookie %v): %d, want 401", method, p, c != nil, resp.StatusCode)
				}
			}
		}
	}
	// A browser gets the token prompt with the 401, and no upload form.
	resp, body := e.get("/", nil)
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, `action="/token"`) || strings.Contains(body, `action="/upload"`) {
		t.Errorf("prompt: %d %s", resp.StatusCode, body)
	}
	// An upload without the token stores nothing.
	if resp, _ := e.upload(nil, "memo.m4a", 10, map[string]string{"start": "2026-10-07T18:30"}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("upload without token: %d", resp.StatusCode)
	}
	if ms, _ := e.st.MeetingsOn("2026-10-07"); len(ms) != 0 {
		t.Errorf("meetings = %+v", ms)
	}
}

func TestTokenPromptSetsHttpOnlyCookie(t *testing.T) {
	e := newUpEnv(t)
	post := func(tok string) (*http.Response, string) {
		req, _ := http.NewRequest("POST", e.srv.URL+"/token", strings.NewReader(url.Values{"token": {tok}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return e.do(req)
	}
	resp, body := post("not-the-token-at-all")
	if resp.StatusCode != http.StatusUnauthorized || len(resp.Cookies()) != 0 || !strings.Contains(body, "not right") {
		t.Errorf("wrong token: %d %v", resp.StatusCode, resp.Cookies())
	}
	resp, _ = post(token)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("right token: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	cs := resp.Cookies()
	if len(cs) != 1 || cs[0].Name != CookieName || !cs[0].HttpOnly || cs[0].SameSite != http.SameSiteStrictMode || cs[0].MaxAge <= 0 {
		t.Fatalf("cookie = %+v", cs)
	}
	if strings.Contains(resp.Header.Get("Location"), token) {
		t.Error("token in the redirect URL")
	}
	resp, body = e.get("/", cs[0])
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `action="/upload"`) || !strings.Contains(body, "1 GB") {
		t.Errorf("form with cookie: %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "script-src 'sha256-"+scriptHash+"'") {
		t.Errorf("csp = %q", resp.Header.Get("Content-Security-Policy"))
	}
	// The cookie opens only the upload routes.
	if resp, _ := e.get("/day/2026-10-07", cs[0]); resp.StatusCode != http.StatusNotFound {
		t.Errorf("dashboard route with cookie: %d, want 404", resp.StatusCode)
	}
}

func TestUploadImportsRecording(t *testing.T) {
	e := newUpEnv(t)
	resp, body := e.upload(goodCookie(), "New Recording 3.m4a", 2048, map[string]string{
		"start": "2026-10-07T18:30", "tz": "America/New_York", "title": "AI Tinkerers meetup", "context": "Maya demoed.",
	})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Uploaded <strong>AI Tinkerers meetup</strong>") {
		t.Fatalf("upload: %d %s", resp.StatusCode, body)
	}
	ms, err := e.st.MeetingsOn("2026-10-07")
	if err != nil || len(ms) != 1 {
		t.Fatalf("meetings = %+v, %v", ms, err)
	}
	m := ms[0]
	if m.ID != "2026-10-07-1830-imported" || !m.Imported || m.ImportStatus != store.StatusPending {
		t.Errorf("meeting = %+v", m)
	}
	if _, off := m.Start.Zone(); off != -4*3600 {
		t.Errorf("start %v not in the phone's zone", m.Start)
	}
	n, err := process.ReadImportNote(e.paths.Import(m.ID))
	if err != nil || n.Title != "AI Tinkerers meetup" || n.Context != "Maya demoed." || n.Original != "New Recording 3.m4a" {
		t.Errorf("note = %+v, %v", n, err)
	}
	if fi, err := os.Stat(filepath.Join(e.paths.Import(m.ID), "audio.m4a")); err != nil || fi.Size() != 2048 {
		t.Errorf("audio: %v %v", fi, err)
	}
	// The temp upload was moved, not left behind.
	ents, _ := os.ReadDir(e.paths.Imports())
	if len(ents) != 1 {
		t.Errorf("imports/ holds %d entries", len(ents))
	}
}

func TestUploadRejectsWrongTypeAndSize(t *testing.T) {
	e := newUpEnv(t)
	e.s.MaxBytes = 1000
	start := map[string]string{"start": "2026-10-07T18:30"}
	for _, c := range []struct {
		name string
		size int
		code int
		msg  string
	}{
		{"clip.mov", 10, http.StatusUnsupportedMediaType, "not a supported audio file"},
		{"notes.txt", 10, http.StatusUnsupportedMediaType, "not a supported audio file"},
		{"memo.m4a", 1001, http.StatusRequestEntityTooLarge, "over the 1 GB limit"},
		{"", 0, http.StatusBadRequest, "Choose an audio file"},
	} {
		resp, body := e.upload(goodCookie(), c.name, c.size, start)
		if resp.StatusCode != c.code || !strings.Contains(body, c.msg) {
			t.Errorf("%s (%d bytes): %d, want %d with %q", c.name, c.size, resp.StatusCode, c.code, c.msg)
		}
	}
	if resp, body := e.upload(goodCookie(), "memo.m4a", 10, nil); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "start time is required") {
		t.Errorf("no start: %d", resp.StatusCode)
	}
	if ms, _ := e.st.MeetingsOn("2026-10-07"); len(ms) != 0 {
		t.Errorf("rejected uploads made meetings %+v", ms)
	}
	if ents, _ := os.ReadDir(e.paths.Imports()); len(ents) != 0 {
		t.Errorf("rejected uploads left %d files", len(ents))
	}
	// A body announcing more than the cap is refused before it is read.
	req := httptest.NewRequest("POST", "/upload", strings.NewReader(""))
	req.ContentLength = process.MaxImportBytes + 2<<20
	req.AddCookie(goodCookie())
	e.s.MaxBytes = 0
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "1 GB") {
		t.Errorf("announced 1 GB+: %d", rec.Code)
	}
}

func TestParseStart(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	for _, c := range []struct {
		v, tz string
		want  time.Time
	}{
		{"2026-10-07T18:30", "America/New_York", time.Date(2026, 10, 7, 18, 30, 0, 0, ny)},
		{"2026-10-07T18:30:15", "America/New_York", time.Date(2026, 10, 7, 18, 30, 15, 0, ny)},
		{"2026-10-07T18:30:00-04:00", "", time.Date(2026, 10, 7, 22, 30, 0, 0, time.UTC)},
	} {
		got, err := ParseStart(c.v, c.tz)
		if err != nil || !got.Equal(c.want) {
			t.Errorf("ParseStart(%q, %q) = %v, %v; want %v", c.v, c.tz, got, err, c.want)
		}
	}
	for _, v := range []string{"", "yesterday", "18:30"} {
		if _, err := ParseStart(v, "America/New_York"); err == nil {
			t.Errorf("ParseStart(%q) accepted", v)
		}
	}
	// A local time is never read on the Mac's clock.
	for _, tz := range []string{"", "Not/AZone"} {
		if _, err := ParseStart("2026-10-07T18:30", tz); err == nil {
			t.Errorf("ParseStart with tz %q accepted", tz)
		}
	}
}
