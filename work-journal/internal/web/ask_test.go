package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"workjournal/internal/skills"
)

type fakeAsker struct {
	mu     sync.Mutex
	calls  []string
	bypass []bool
	answer string
	err    error
	block  chan struct{} // when set, Ask waits for it
}

func (f *fakeAsker) Ask(_ context.Context, q string, bypass bool) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, q)
	f.bypass = append(f.bypass, bypass)
	block := f.block
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	return f.answer, f.err
}

func (f *fakeAsker) n() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func askEnv(t *testing.T) (*env, *fakeAsker) {
	e := newEnv(t)
	a := &fakeAsker{answer: "You agreed to **ship on Friday** ([Standup](http://127.0.0.1:4747/meeting/2026-10-07-0910-zoom)).<script>alert(1)</script>"}
	e.srv.Ask = a
	e.h = e.srv.Handler()
	return e, a
}

func askForm(q string) url.Values { return url.Values{"q": {q}} }

func TestAskRunsInBackgroundAndShowsAnswer(t *testing.T) {
	e, a := askEnv(t)
	rec := e.post("/ask", askForm("What did we decide in standup?"), map[string]string{"Origin": "http://127.0.0.1:4747"})
	if rec.Code != 303 {
		t.Fatalf("code %d: %s", rec.Code, rec.Body)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/ask?pending=") {
		t.Fatalf("Location %q", loc)
	}
	e.srv.askWait()
	if a.n() != 1 || a.calls[0] != "What did we decide in standup?" || a.bypass[0] {
		t.Fatalf("calls %q bypass %v", a.calls, a.bypass)
	}
	body := e.get(loc).Body.String()
	mustContain(t, body, "What did we decide in standup?", "<strong>ship on Friday</strong>",
		`href="http://127.0.0.1:4747/meeting/2026-10-07-0910-zoom"`, `data-rev-url="/api/rev/ask"`)
	if strings.Contains(body, "<script>alert") {
		t.Error("raw HTML from the answer rendered")
	}
	v, ok, _ := e.st.GetKV("ask_history")
	if !ok || !strings.Contains(v, "ship on Friday") {
		t.Fatalf("ask_history %q", v)
	}
}

func TestAskPendingPageRefreshesUntilAnswered(t *testing.T) {
	e, a := askEnv(t)
	a.block = make(chan struct{})
	loc := e.post("/ask", askForm("How long were meetings?"), nil).Header().Get("Location")
	body := e.get(loc).Body.String()
	mustContain(t, body, "How long were meetings?", "Thinking", "data-poll-ms=")
	rev0 := e.get("/api/rev/ask").Body.String()
	close(a.block)
	e.srv.askWait()
	rev1 := e.get("/api/rev/ask").Body.String()
	if rev0 == rev1 {
		t.Fatal("rev unchanged after the answer landed")
	}
	if body := e.get(loc).Body.String(); strings.Contains(body, "Thinking") {
		t.Error("still pending after the answer")
	}
}

func TestAskOneAtATime(t *testing.T) {
	e, a := askEnv(t)
	a.block = make(chan struct{})
	first := e.post("/ask", askForm("first"), nil).Header().Get("Location")
	rec := e.post("/ask", askForm("second"), nil)
	if rec.Code != 303 || !strings.Contains(rec.Header().Get("Location"), "notice=busy") {
		t.Fatalf("second ask: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if !strings.Contains(rec.Header().Get("Location"), strings.TrimPrefix(first, "/ask?")) {
		t.Errorf("busy redirect %q does not point at the running ask %q", rec.Header().Get("Location"), first)
	}
	mustContain(t, e.get(rec.Header().Get("Location")).Body.String(), "already")
	close(a.block)
	e.srv.askWait()
	if a.n() != 1 {
		t.Fatalf("asker ran %d times", a.n())
	}
}

func TestAskValidation(t *testing.T) {
	e, a := askEnv(t)
	for _, q := range []string{"", "   ", strings.Repeat("x", 1001)} {
		if rec := e.post("/ask", askForm(q), nil); rec.Code != 400 {
			t.Errorf("q len %d: code %d", len(q), rec.Code)
		}
	}
	if rec := e.post("/ask", askForm(strings.Repeat("é", 1000)), nil); rec.Code != 303 {
		t.Errorf("1000 characters refused: %d", rec.Code)
	}
	e.srv.askWait()
	if rec := e.post("/ask", askForm("hi"), map[string]string{"Origin": "http://evil.example"}); rec.Code != 403 {
		t.Errorf("cross-origin: %d", rec.Code)
	}
	if a.n() != 1 {
		t.Fatalf("asker ran %d times", a.n())
	}
}

func TestAskHistoryKeepsLast20(t *testing.T) {
	e, _ := askEnv(t)
	for i := range 23 {
		e.post("/ask", askForm(fmt.Sprintf("question %02d", i)), nil)
		e.srv.askWait()
	}
	v, _, _ := e.st.GetKV("ask_history")
	var hist []map[string]any
	if err := json.Unmarshal([]byte(v), &hist); err != nil {
		t.Fatal(err)
	}
	if len(hist) != 20 {
		t.Fatalf("%d entries", len(hist))
	}
	body := e.get("/ask").Body.String()
	mustContain(t, body, "question 22", "question 03")
	if strings.Contains(body, "question 02") {
		t.Error("oldest entries kept")
	}
	if strings.Index(body, "question 22") > strings.Index(body, "question 03") {
		t.Error("history not newest first")
	}
}

func TestAskFailureIsShown(t *testing.T) {
	e, a := askEnv(t)
	a.err = errors.New("ask: claude failed: <b>boom</b>")
	loc := e.post("/ask", askForm("q?"), nil).Header().Get("Location")
	e.srv.askWait()
	body := e.get(loc).Body.String()
	mustContain(t, body, "q?", "&lt;b&gt;boom&lt;/b&gt;")
}

func TestAskRespectsUsagePause(t *testing.T) {
	e, a := askEnv(t)
	if err := skills.SetPause(e.st, time.Now(), skills.ErrUsageLimited{ResetAt: time.Now().Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	mustContain(t, e.get("/ask").Body.String(), "Claude usage limit reached", "Ask anyway")
	rec := e.post("/ask", askForm("still there?"), nil)
	if rec.Code != 200 || a.n() != 0 {
		t.Fatalf("paused ask: code %d, asker ran %d times", rec.Code, a.n())
	}
	mustContain(t, rec.Body.String(), "Ask anyway", ">still there?</textarea>", `name="force"`)
	rec = e.post("/ask", url.Values{"q": {"still there?"}, "force": {"1"}}, nil)
	if rec.Code != 303 {
		t.Fatalf("ask anyway: %d", rec.Code)
	}
	e.srv.askWait()
	if a.n() != 1 || !a.bypass[0] {
		t.Fatalf("ask anyway: calls %q bypass %v", a.calls, a.bypass)
	}
	if st, _ := skills.ReadPause(e.st, time.Now()); !st.Active {
		t.Error("ask anyway lifted the pause")
	}
}

func TestAskUsageLimitedAnswer(t *testing.T) {
	e, a := askEnv(t)
	a.err = fmt.Errorf("ask: %w", skills.ErrUsageLimited{})
	loc := e.post("/ask", askForm("q"), nil).Header().Get("Location")
	e.srv.askWait()
	mustContain(t, e.get(loc).Body.String(), "usage limit")
}

func TestDayPageHasAskBox(t *testing.T) {
	e, _ := askEnv(t)
	mustContain(t, e.get("/day/"+seedDay).Body.String(), `action="/ask"`, `name="q"`, `maxlength="1000"`)
}

func TestAskUnavailableWithoutAsker(t *testing.T) {
	e := newEnv(t)
	mustContain(t, e.get("/ask").Body.String(), "not available")
	if rec := e.post("/ask", askForm("q"), nil); rec.Code != 503 {
		t.Fatalf("code %d", rec.Code)
	}
}
