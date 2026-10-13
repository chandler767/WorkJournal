package web

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"workjournal/internal/skills"
)

func TestDayAndHourPagesShowClaudePauseBanner(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	until := time.Date(now.Year(), now.Month(), now.Day(), 15, 0, 0, 0, time.Local)
	if !until.After(now) {
		until = until.AddDate(0, 0, 1)
	}
	if err := skills.SetPause(e.st, now, skills.ErrUsageLimited{ResetAt: until}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/day/" + seedDay, "/hour/" + seedDay + "/09"} {
		body := e.get(path).Body.String()
		mustContain(t, body, "Claude usage limit reached", "summaries paused until 3:00", `action="/claude/resume"`, "Run now anyway")
	}

	// Unknown reset time.
	e = newEnv(t)
	if err := skills.SetPause(e.st, now, skills.ErrUsageLimited{}); err != nil {
		t.Fatal(err)
	}
	mustContain(t, e.get("/day/"+seedDay).Body.String(), "summaries paused for about an hour")

	// Not paused: no banner.
	e = newEnv(t)
	if body := e.get("/day/" + seedDay).Body.String(); strings.Contains(body, "Claude usage limit") || strings.Contains(body, "/claude/resume") {
		t.Error("banner shown while not paused")
	}
}

func TestClaudePauseChangesRevs(t *testing.T) {
	e := newEnv(t)
	day0, _ := e.srv.dayRev(seedDay)
	hour0, _ := e.srv.hourRev(seedDay, 9)
	if err := skills.SetPause(e.st, time.Now(), skills.ErrUsageLimited{}); err != nil {
		t.Fatal(err)
	}
	day1, _ := e.srv.dayRev(seedDay)
	hour1, _ := e.srv.hourRev(seedDay, 9)
	if day0 == day1 || hour0 == hour1 {
		t.Fatal("pausing does not change the page revs")
	}
}

func TestClaudeResumeClearsPauseAndRunsNow(t *testing.T) {
	e := newEnv(t)
	if err := skills.SetPause(e.st, time.Now(), skills.ErrUsageLimited{ResetAt: time.Now().Add(3 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var pausedDuring bool
	e.jobs.onProcessNow = func() {
		st, _ := skills.ReadPause(e.st, time.Now())
		pausedDuring = st.Active
	}
	e.jobs.upto = seedTime(10, 32)
	rec := e.post("/claude/resume", url.Values{}, nil)
	if rec.Code != 303 || rec.Header().Get("Location") != "/day/2026-10-07?notice=processed&upto=10:32" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if e.jobs.processedNow != 1 || pausedDuring {
		t.Fatalf("ProcessNow ran %d times, paused during it: %v", e.jobs.processedNow, pausedDuring)
	}
	if st, _ := skills.ReadPause(e.st, time.Now()); st.Active {
		t.Fatalf("pause not cleared: %+v", st)
	}

	// Claude still limited: the run sets the pause again and the notice says so.
	e.jobs.onProcessNow = func() { skills.SetPause(e.st, time.Now(), skills.ErrUsageLimited{}) }
	rec = e.post("/claude/resume", url.Values{}, nil)
	loc := rec.Header().Get("Location")
	if rec.Code != 303 || loc != "/day/2026-10-07?notice=claude-still-limited" {
		t.Fatalf("still limited: %d %q", rec.Code, loc)
	}
	mustContain(t, e.get(loc).Body.String(), "Claude is still at its usage limit")
}

func TestClaudeResumeCrossOriginRefused(t *testing.T) {
	e := newEnv(t)
	if c := e.post("/claude/resume", url.Values{}, map[string]string{"Origin": "http://evil.example"}).Code; c != 403 {
		t.Fatalf("code %d", c)
	}
	if e.jobs.processedNow != 0 {
		t.Fatal("cross-origin resume ran")
	}
}

func TestSettingsPauseOnUsageLimit(t *testing.T) {
	e := newSettingsEnv(t)
	body := e.get("/settings").Body.String()
	mustContain(t, body, "Pause summaries when Claude usage runs out", `name="pause_on_usage_limit"`)
	f := e.form()
	f.Set("pause_on_usage_limit", "false")
	if rec := e.post("/settings", f, nil); rec.Code != 303 || e.cfg.PauseOnUsageLimit {
		t.Fatalf("code %d, saved %v", rec.Code, e.cfg.PauseOnUsageLimit)
	}
	// An older form without the field keeps the current value.
	if rec := e.post("/settings", e.form(), nil); rec.Code != 303 || e.cfg.PauseOnUsageLimit {
		t.Fatalf("missing field: %d %v", rec.Code, e.cfg.PauseOnUsageLimit)
	}
	f.Set("pause_on_usage_limit", "true")
	if rec := e.post("/settings", f, nil); rec.Code != 303 || !e.cfg.PauseOnUsageLimit {
		t.Fatalf("code %d, saved %v", rec.Code, e.cfg.PauseOnUsageLimit)
	}
}

func TestPauseBannerHiddenWhenPausingOff(t *testing.T) {
	e := newSettingsEnv(t)
	e.cfg.PauseOnUsageLimit = false
	if err := skills.SetPause(e.st, time.Now(), skills.ErrUsageLimited{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(e.get("/day/"+seedDay).Body.String(), "Claude usage limit") {
		t.Error("banner shown although pausing is off")
	}
}
