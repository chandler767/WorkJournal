package web

import (
	"net/url"
	"strings"
	"testing"
)

const seedWeek = "2026-W41" // contains seedDay 2026-10-07

func TestWeekPageRendersMarkdownSafely(t *testing.T) {
	e := newEnv(t)
	writeFile(t, e.p.WeeklyJournal(seedWeek), "## Themes\n- **Scheduler** work\n\n<script>alert(1)</script>\n\n[bad](javascript:alert(2)) [ok](https://example.com)\n")
	rec := e.get("/week/" + seedWeek)
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	b := rec.Body.String()
	mustContain(t, b, "<h2>Themes</h2>", "<strong>Scheduler</strong>", `href="https://example.com"`)
	if strings.Contains(b, "<script>alert") {
		t.Error("script tag rendered")
	}
	if strings.Contains(strings.ToLower(b), `href="javascript`) {
		t.Error("javascript: href rendered")
	}
}

func TestDayPageRendersMarkdownSafely(t *testing.T) {
	e := newEnv(t)
	writeFile(t, e.p.DailyJournal(seedDay), "## Summary\n**Bold** text\n\n<script>alert(1)</script>\n\n[bad](javascript:alert(2))\n")
	b := e.get("/day/" + seedDay).Body.String()
	mustContain(t, b, "<h2>Summary</h2>", "<strong>Bold</strong>")
	if strings.Contains(b, "<script>alert") || strings.Contains(strings.ToLower(b), `href="javascript`) {
		t.Error("unsafe markup rendered on day page")
	}
}

func TestWeekPageShowsDailyTotals(t *testing.T) {
	e := newEnv(t)
	b := e.get("/week/" + seedWeek).Body.String()
	mustContain(t, b, "2026-W41", "/day/2026-10-07", "Deep work", "40m", "/week/2026-W40", "/week/2026-W42", "No weekly summary yet", "/week/2026-W41/rollup")
}

func TestWeekPageBadWeek(t *testing.T) {
	e := newEnv(t)
	for _, w := range []string{"2026-41", "abc", "2027-W53", "2026-W00"} {
		if code := e.get("/week/" + w).Code; code != 400 {
			t.Errorf("%s: code %d", w, code)
		}
	}
}

func TestWeekRollupPost(t *testing.T) {
	e := newEnv(t)
	rec := e.post("/week/"+seedWeek+"/rollup", url.Values{}, map[string]string{"Referer": "http://127.0.0.1:4747/week/" + seedWeek})
	if rec.Code != 303 || rec.Header().Get("Location") != "/week/"+seedWeek {
		t.Fatalf("code %d loc %q", rec.Code, rec.Header().Get("Location"))
	}
	if len(e.jobs.rolled) != 1 || e.jobs.rolled[0] != seedWeek {
		t.Errorf("rolled = %v", e.jobs.rolled)
	}
	if rec := e.post("/week/bad/rollup", url.Values{}, nil); rec.Code != 400 {
		t.Errorf("bad week code %d", rec.Code)
	}
	if rec := e.post("/week/"+seedWeek+"/rollup", url.Values{}, map[string]string{"Origin": "http://evil.example"}); rec.Code != 403 {
		t.Errorf("cross-origin code %d", rec.Code)
	}
}

func TestLayoutHasWeekLink(t *testing.T) {
	e := newEnv(t)
	mustContain(t, e.get("/settings").Body.String(), `href="/week/20`)
}
