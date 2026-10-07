package web

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"workjournal/internal/capture"
	"workjournal/internal/config"
	"workjournal/internal/store"
)

type fakeControl struct {
	calls     []string
	statusErr error
}

func (f *fakeControl) Pause() error  { f.calls = append(f.calls, "pause"); return nil }
func (f *fakeControl) Resume() error { f.calls = append(f.calls, "resume"); return nil }
func (f *fakeControl) Status() (capture.Status, error) {
	if f.statusErr != nil {
		return capture.Status{}, f.statusErr
	}
	return capture.Status{State: "recording", Screens: true, Audio: true}, nil
}
func (f *fakeControl) Send(string) error { return nil }

type fakeJobs struct {
	reprocessed, closed []string
	enrolled            [][3]string // meeting, speaker, name
	enrollErr           error
	enrollSaw           *store.Store // checked at call time: participant already saved?
	sawParticipants     []store.ParticipantRecord
}

func (f *fakeJobs) ProcessPending(context.Context) error { return nil }
func (f *fakeJobs) ReprocessHour(_ context.Context, d string, h int) error {
	f.reprocessed = append(f.reprocessed, d+"/"+string(rune('0'+h/10))+string(rune('0'+h%10)))
	return nil
}
func (f *fakeJobs) CloseDay(_ context.Context, d string) error {
	f.closed = append(f.closed, d)
	return nil
}
func (f *fakeJobs) Rollup(context.Context, string) error { return nil }
func (f *fakeJobs) Retain(context.Context) error         { return nil }
func (f *fakeJobs) DiskCheck(context.Context) error      { return nil }
func (f *fakeJobs) EnrollSpeaker(_ context.Context, m, sp, n string) error {
	f.enrolled = append(f.enrolled, [3]string{m, sp, n})
	if f.enrollSaw != nil {
		f.sawParticipants, _ = f.enrollSaw.Participants(m)
	}
	return f.enrollErr
}

type env struct {
	srv  *Server
	h    http.Handler
	st   *store.Store
	ctl  *fakeControl
	jobs *fakeJobs
	p    config.Paths
}

func newEnv(t *testing.T) *env {
	t.Helper()
	p := config.Paths{Root: t.TempDir()}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	Seed(t, st, p)
	cfg := config.Default()
	ctl, jobs := &fakeControl{}, &fakeJobs{}
	s := &Server{Store: st, Paths: p, Cfg: func() config.Config { return cfg }, Control: ctl, Jobs: jobs}
	return &env{srv: s, h: s.Handler(), st: st, ctl: ctl, jobs: jobs, p: p}
}

func (e *env) get(path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", path, nil)
	req.Host = "127.0.0.1:4747"
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *env) post(path string, form url.Values, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Host = "127.0.0.1:4747"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func mustContain(t *testing.T, body string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(body, s) {
			t.Errorf("body missing %q\n%s", s, body)
		}
	}
}

func TestDayPageShowsNotesAndTotals(t *testing.T) {
	e := newEnv(t)
	rec := e.get("/day/2026-10-07")
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	b := rec.Body.String()
	mustContain(t, b, "Refactored the scheduler", "Scheduler work", "Deep work", "40m", "Close the day",
		"Shipped &lt;b&gt;scheduler&lt;/b&gt; work.", "Standup", "<svg", "recording")
	if strings.Contains(b, "<b>scheduler</b>") {
		t.Error("journal text not escaped")
	}
}

func TestDayPageNeedsReviewBadge(t *testing.T) {
	e := newEnv(t)
	b := e.get("/day/2026-10-07").Body.String()
	mustContain(t, b, `class="badge`, "needs review", "/hour/2026-10-07/10/reprocess")
}

func TestDayPageRecorderNotRunningAndDiskWarning(t *testing.T) {
	e := newEnv(t)
	e.ctl.statusErr = errors.New("dial recorder")
	if err := e.st.SetKV("disk_state", "low"); err != nil {
		t.Fatal(err)
	}
	rec := e.get("/day/2026-10-07")
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	mustContain(t, rec.Body.String(), "Recorder not running", "disk-warning")
	_ = e.st.SetKV("disk_state", "normal")
	if strings.Contains(e.get("/day/2026-10-07").Body.String(), "disk-warning") {
		t.Error("warning shown for normal disk state")
	}
}

func TestHourPageShowsSpeakers(t *testing.T) {
	e := newEnv(t)
	rec := e.get("/hour/2026-10-07/09")
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	mustContain(t, rec.Body.String(), "Priya", "Let us ship on Friday", "#t=", `type="range"`, "<audio",
		"/media/2026-10-07/audio/0900-mic.m4a", "/media/2026-10-07/screens/090500.jpg", "<select", "Send release notes")
}

func TestInvalidDateAndHourRejected(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/day/nope", "/day/2026-13-45", "/hour/2026-10-07/24", "/hour/2026-10-07/ab", "/hour/bad/09"} {
		if c := e.get(p).Code; c != 400 {
			t.Errorf("%s = %d, want 400", p, c)
		}
	}
}

func TestRootRedirectsToToday(t *testing.T) {
	e := newEnv(t)
	rec := e.get("/")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/day/") {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestMediaServesAndRejectsTraversal(t *testing.T) {
	e := newEnv(t)
	if rec := e.get("/media/2026-10-07/screens/090500.jpg"); rec.Code != 200 {
		t.Fatalf("serve = %d", rec.Code)
	}
	for _, p := range []string{
		"/media/2026-10-07/screens/..%2f..%2fconfig.toml",
		"/media/2026-10-07/screens/a%2fb.jpg",
		"/media/2026-10-07/other/090500.jpg",
		"/media/bad/screens/090500.jpg",
	} {
		if c := e.get(p).Code; c != 400 {
			t.Errorf("%s = %d, want 400", p, c)
		}
	}
}

func TestMediaExpiredReturns410(t *testing.T) {
	e := newEnv(t)
	rec := e.get("/media/2026-10-07/audio/2359-mic.m4a")
	if rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), "raw media expired") {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}

func TestPausePostsToControl(t *testing.T) {
	e := newEnv(t)
	rec := e.post("/pause", nil, map[string]string{"Referer": "http://127.0.0.1:4747/day/2026-10-06"})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/day/2026-10-06" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	e.post("/resume", nil, nil)
	if strings.Join(e.ctl.calls, ",") != "pause,resume" {
		t.Fatalf("calls = %v", e.ctl.calls)
	}
}

func TestPostRejectsForeignOrigin(t *testing.T) {
	e := newEnv(t)
	rec := e.post("/pause", nil, map[string]string{"Origin": "http://evil.example"})
	if rec.Code != 403 || len(e.ctl.calls) != 0 {
		t.Fatalf("got %d calls=%v", rec.Code, e.ctl.calls)
	}
	rec = e.post("/pause", nil, map[string]string{"Origin": "http://127.0.0.1:4747"}) // httptest host
	if rec.Code != 303 {
		t.Fatalf("same-origin = %d", rec.Code)
	}
}

func TestCloseDayAndReprocess(t *testing.T) {
	e := newEnv(t)
	e.post("/close-day/2026-10-07", nil, nil)
	e.post("/hour/2026-10-07/10/reprocess", nil, nil)
	if len(e.jobs.closed) != 1 || e.jobs.closed[0] != "2026-10-07" {
		t.Errorf("closed = %v", e.jobs.closed)
	}
	if len(e.jobs.reprocessed) != 1 || e.jobs.reprocessed[0] != "2026-10-07/10" {
		t.Errorf("reprocessed = %v", e.jobs.reprocessed)
	}
}

func TestSegmentCategoryOverride(t *testing.T) {
	e := newEnv(t)
	form := url.Values{"date": {"2026-10-07"}, "hour": {"10"}, "start": {seedTime(10, 0).Format("2006-01-02T15:04:05.999999999Z07:00")}, "category": {"Deep work"}}
	rec := e.post("/segment/category", form, map[string]string{"Referer": "http://127.0.0.1:4747/hour/2026-10-07/10"})
	if rec.Code != 303 || rec.Header().Get("Location") != "/hour/2026-10-07/10" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	segs, _ := e.st.SegmentsOn("2026-10-07")
	var got string
	for _, g := range segs {
		if g.Hour == 10 {
			got = g.Category + "/" + g.Source
		}
	}
	if got != "Deep work/user" {
		t.Fatalf("segment = %q", got)
	}
	// second override of the same app must not duplicate the suggestion
	e.post("/segment/category", form, nil)
	v, _, _ := e.st.GetKV("rule_suggestions")
	if strings.Count(v, `"Safari"`) != 1 || !strings.Contains(v, `"category":"Deep work"`) {
		t.Fatalf("rule_suggestions = %s", v)
	}
}

func TestSegmentCategoryRejectsUnknownCategory(t *testing.T) {
	e := newEnv(t)
	form := url.Values{"date": {"2026-10-07"}, "hour": {"10"}, "start": {seedTime(10, 0).Format("2006-01-02T15:04:05.999999999Z07:00")}, "category": {"Bogus"}}
	if c := e.post("/segment/category", form, nil).Code; c != 400 {
		t.Fatalf("code %d", c)
	}
}

func TestStaticAssetsServed(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/static/app.css", "/static/app.js", "/static/chart.js"} {
		if c := e.get(p).Code; c != 200 {
			t.Errorf("%s = %d", p, c)
		}
	}
}

func TestServerBindsLocalhostOnly(t *testing.T) {
	host, _, err := net.SplitHostPort(config.Default().ListenAddr)
	if err != nil || host != "127.0.0.1" {
		t.Fatalf("ListenAddr host = %q err=%v", host, err)
	}
}

func TestRedirectBackRejectsProtocolRelative(t *testing.T) {
	e := newEnv(t)
	for _, ref := range []string{
		"http://127.0.0.1:4747//evil.example/x",
		"http://127.0.0.1:4747/\\evil.example",
		"http://127.0.0.1:4747/other/page",
	} {
		rec := e.post("/pause", nil, map[string]string{"Referer": ref})
		loc := rec.Header().Get("Location")
		if rec.Code != 303 || !strings.HasPrefix(loc, "/day/") {
			t.Errorf("referer %q -> %d %q, want /day/ fallback", ref, rec.Code, loc)
		}
	}
	rec := e.post("/pause", nil, map[string]string{"Referer": "http://127.0.0.1:4747/settings"})
	if rec.Header().Get("Location") != "/settings" {
		t.Errorf("settings referer -> %q", rec.Header().Get("Location"))
	}
}

func TestRejectsForeignHost(t *testing.T) {
	e := newEnv(t)
	for host, want := range map[string]int{"evil.example": 403, "evil.example:4747": 403, "localhost:4747": 200, "127.0.0.1:4747": 200, "[::1]:4747": 200, "localhost": 200} {
		req := httptest.NewRequest("GET", "/day/2026-10-07", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Host %q = %d, want %d", host, rec.Code, want)
		}
		if want == 403 && !strings.Contains(rec.Body.String(), "forbidden host") {
			t.Errorf("Host %q body %q", host, rec.Body.String())
		}
	}
}
