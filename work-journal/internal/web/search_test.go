package web

import (
	"net/url"
	"strings"
	"testing"

	"workjournal/internal/store"
)

func seedSearch(t *testing.T, e *env) {
	t.Helper()
	if err := e.st.ReplaceText(seedDay, 9, []store.TextDoc{
		{Kind: "transcript", Day: seedDay, Hour: 9, TS: seedTime(9, 10), Text: "Priya: ship the zebra release", Ref: "hour/" + seedDay + "/09#600"},
		{Kind: "note", Day: seedDay, Hour: 9, TS: seedTime(9, 0), Text: "zebra <script>alert(1)</script> note", Ref: "hour/" + seedDay + "/09#0"},
		{Kind: "ocr", Day: seedDay, Hour: 9, TS: seedTime(9, 5), Text: "zebra evil", Ref: "https://evil.example/x"},
		{Kind: "meeting", Day: seedDay, Hour: 9, TS: seedTime(9, 30), Text: "zebra meeting", Ref: "meeting/" + "2026-10-07-0910-zoom"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSearchLinksToHourAndTimestamp(t *testing.T) {
	e := newEnv(t)
	seedSearch(t, e)
	rec := e.get("/search?q=" + url.QueryEscape("zebra"))
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	body := rec.Body.String()
	mustContain(t, body, `href="/hour/`+seedDay+`/09#600"`, `<mark>zebra</mark>`, `href="/meeting/2026-10-07-0910-zoom"`)
	if strings.Contains(body, "evil.example") {
		t.Error("unsafe ref linked")
	}
	if strings.Contains(body, "<script>alert(1)") {
		t.Error("snippet not escaped")
	}
	mustContain(t, body, "&lt;script&gt;")
}

func TestSearchEmptyQuery(t *testing.T) {
	e := newEnv(t)
	seedSearch(t, e)
	for _, q := range []string{"", "   "} {
		rec := e.get("/search?q=" + url.QueryEscape(q))
		if rec.Code != 200 {
			t.Errorf("q=%q code %d", q, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "<mark>") || strings.Contains(rec.Body.String(), `class="hit"`) {
			t.Errorf("q=%q shows results", q)
		}
	}
}

func TestSearchNoMatchesAndLimit(t *testing.T) {
	e := newEnv(t)
	seedSearch(t, e)
	mustContain(t, e.get("/search?q=qqqq").Body.String(), "No results")
}

func TestSplitMarks(t *testing.T) {
	got := splitMarks("a [b] c [d")
	if len(got) < 3 || got[1].Text != "b" || !got[1].Mark || got[0].Mark {
		t.Errorf("%+v", got)
	}
}
