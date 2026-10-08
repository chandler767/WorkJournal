package web

import (
	"io"
	"regexp"
	"strings"
	"testing"

	"workjournal/internal/store"
)

func upsertHour(t *testing.T, e *env, hour int, status, summary string) {
	t.Helper()
	if err := e.st.UpsertHour(store.HourRecord{Day: seedDay, Hour: hour, Status: status, UpdatedAt: seedTime(12, 0), SummaryJSON: summary}); err != nil {
		t.Fatal(err)
	}
}

// article returns the day page's hour card for hh ("" when there is none).
func article(body, hh string) string {
	re := regexp.MustCompile(`(?s)<article class="hour"[^>]*>.*?</article>`)
	for _, a := range re.FindAllString(body, -1) {
		if strings.Contains(a, `href="/hour/`+seedDay+`/`+hh+`"`) {
			return a
		}
	}
	return ""
}

func TestDayHourHeaderKeepsBadgesSeparate(t *testing.T) {
	e := newEnv(t)
	upsertHour(t, e, 11, store.StatusPartial, `{"narrative":"Started the cache work.","notes":["Cache design"],"main_focus":"Cache"}`)
	a := article(e.get("/day/"+seedDay).Body.String(), "11")
	mustContain(t, a, `<a class="hour-time" href="/hour/`+seedDay+`/11">11:00</a>`, `<span class="hour-title">Cache</span>`,
		`<span class="hour-badges"><span class="badge">in progress</span></span>`)
	if strings.Index(a, "hour-title") > strings.Index(a, "hour-badges") {
		t.Error("badges before title")
	}
	css := cssText(t, e)
	if !regexp.MustCompile(`\.badge \{[^}]*white-space: nowrap[^}]*flex-shrink: 0`).MatchString(css) {
		t.Error(".badge must not wrap or shrink")
	}
}

func TestDayHourNarrativeOpenNotesCollapsedActionsVisible(t *testing.T) {
	e := newEnv(t)
	a := article(e.get("/day/"+seedDay).Body.String(), "09")
	mustContain(t, a, `<p class="narrative">Rewrote the scheduler retry loop`, `<details class="notes"><summary>2 notes</summary>`,
		`<h4 class="actions-h">Action items</h4>`, "Send release notes")
	if strings.Contains(a, "<summary>Narrative") || strings.Contains(a, `<details class="notes" open`) {
		t.Errorf("narrative must be open and notes collapsed on the day page:\n%s", a)
	}
	n, d, end, act := strings.Index(a, `class="narrative"`), strings.Index(a, "<details"), strings.Index(a, "</details>"), strings.Index(a, "actions-h")
	if !(n < d && d < end && end < act) {
		t.Errorf("order narrative=%d details=%d /details=%d actions=%d", n, d, end, act)
	}
	upsertHour(t, e, 8, store.StatusDone, `{"notes":["Only one"],"main_focus":"One"}`)
	mustContain(t, article(e.get("/day/"+seedDay).Body.String(), "08"), "<summary>1 note</summary>")
}

func TestHourPageNotesOpen(t *testing.T) {
	e := newEnv(t)
	body := e.get("/hour/" + seedDay + "/09").Body.String()
	mustContain(t, body, `<details class="notes" open><summary>2 notes</summary>`, `<h3 class="actions-h">Action items</h3>`)
}

func TestReprocessShownOnlyWhereUseful(t *testing.T) {
	e := newEnv(t)
	upsertHour(t, e, 11, store.StatusPartial, `{"notes":["x"]}`)
	upsertHour(t, e, 12, store.StatusTranscribed, "")
	upsertHour(t, e, 13, store.StatusPending, "")
	body := e.get("/day/" + seedDay).Body.String()
	for hh, want := range map[string]bool{"09": false, "10": true, "11": true, "12": true, "13": false} {
		a := article(body, hh)
		if a == "" {
			t.Fatalf("no card for %s", hh)
		}
		if got := strings.Contains(a, "/reprocess"); got != want {
			t.Errorf("hour %s reprocess shown=%v, want %v", hh, got, want)
		}
		if want {
			mustContain(t, a, `<form class="reprocess"`, `<button type="submit" class="link">Reprocess</button>`)
		}
	}
	mustContain(t, e.get("/hour/"+seedDay+"/09").Body.String(), `action="/hour/`+seedDay+`/09/reprocess"`)
}

func TestEmptyHourIsOneMutedLine(t *testing.T) {
	e := newEnv(t)
	upsertHour(t, e, 2, store.StatusDone, `{}`)
	body := e.get("/day/" + seedDay).Body.String()
	mustContain(t, body, `<p class="hour-empty muted">02:00 · no activity</p>`)
	if article(body, "02") != "" || strings.Contains(body, "/02/reprocess") {
		t.Error("empty hour rendered as a card")
	}
}

func TestDayNavShortLabels(t *testing.T) {
	e := newEnv(t)
	body := e.get("/day/" + seedDay).Body.String()
	mustContain(t, body, `<span class="long">&larr; 2026-10-06</span><span class="short">&larr; Oct 6</span>`,
		`<span class="long">2026-10-08 &rarr;</span><span class="short">Oct 8 &rarr;</span>`, `<div class="timeline-wrap">`)
}

func TestNotesRenderBackticksAsCode(t *testing.T) {
	e := newEnv(t)
	upsertHour(t, e, 8, store.StatusDone, "{\"notes\":[\"Ran `go test ./...` in /Users/x/a/very/long/path and `<b>x</b>`\"],\"main_focus\":\"T\"}")
	for _, path := range []string{"/day/" + seedDay, "/hour/" + seedDay + "/08"} {
		body := e.get(path).Body.String()
		mustContain(t, body, "<li>Ran <code>go test ./...</code> in /Users/x/a/very/long/path and <code>&lt;b&gt;x&lt;/b&gt;</code></li>")
	}
	if !strings.Contains(cssText(t, e), "overflow-wrap: anywhere") {
		t.Error("no overflow-wrap: anywhere in css")
	}
}

func TestCodeSegs(t *testing.T) {
	got := codeSegs("a `b` c `unclosed")
	want := []codeSeg{{Text: "a "}, {Text: "b", Code: true}, {Text: " c `unclosed"}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("seg %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func cssText(t *testing.T, e *env) string {
	t.Helper()
	rec := e.get("/static/app.css")
	b, _ := io.ReadAll(rec.Body)
	return string(b)
}
