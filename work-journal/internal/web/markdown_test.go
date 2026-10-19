package web

import (
	"strings"
	"testing"
)

func TestRenderMarkdownStripsImagesAndForeignLinks(t *testing.T) {
	src := "Leak ![](https://x/y?d=secret) and ![alt text](/media/x.jpg) then [click](https://evil) " +
		"or <https://evil.example/auto> or [proto](//evil.example/p) or [js](javascript:alert(1)).\n\n" +
		"Keep [Standup](http://127.0.0.1:4747/meeting/2026-10-07-0910-zoom), [hour](/hour/2026-10-07/09) and [here](#q).\n\n" +
		"[ref]: https://evil.example/ref\nAlso [a ref][ref] and [other port](http://127.0.0.1:9999/x).\n"
	got, err := renderMarkdown(src, "127.0.0.1:4747")
	if err != nil {
		t.Fatal(err)
	}
	h := string(got)
	for _, bad := range []string{"<img", "https://x/y", "javascript"} {
		if strings.Contains(h, bad) {
			t.Errorf("rendered %q: %s", bad, h)
		}
	}
	for _, l := range []string{`href="https://evil"`, `href="https://evil.example/auto"`, `href="//evil.example/p"`, `href="https://evil.example/ref"`, `href="http://127.0.0.1:9999/x"`} {
		if strings.Contains(h, l) {
			t.Errorf("foreign link kept: %s in %s", l, h)
		}
	}
	for _, keep := range []string{`href="http://127.0.0.1:4747/meeting/2026-10-07-0910-zoom"`, `href="/hour/2026-10-07/09"`, `href="#q"`,
		"click", "Standup", "alt text", "a ref", "other port"} {
		if !strings.Contains(h, keep) {
			t.Errorf("missing %q in %s", keep, h)
		}
	}
}

func TestCSPOnEveryPage(t *testing.T) {
	e, _ := askEnv(t)
	want := "default-src 'self'; img-src 'self' data:; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
	for _, p := range []string{"/", "/ask", "/day/" + seedDay, "/hour/" + seedDay + "/09", "/static/app.js", "/nope"} {
		if got := e.get(p).Header().Get("Content-Security-Policy"); got != want {
			t.Errorf("%s: CSP %q", p, got)
		}
	}
	if got := e.post("/ask", askForm("q"), nil).Header().Get("Content-Security-Policy"); got != want {
		t.Errorf("POST /ask: CSP %q", got)
	}
	e.srv.askWait()
}

func TestAskAnswerRendersNoExternalImageOrLink(t *testing.T) {
	e, a := askEnv(t)
	a.answer = "Done ![](https://x/y?d=secret) [click](https://evil) [ok](http://127.0.0.1:4747/hour/2026-10-07/09)"
	loc := e.post("/ask", askForm("q"), nil).Header().Get("Location")
	e.srv.askWait()
	b := e.get(loc).Body.String()
	if strings.Contains(b, "https://x/y") || strings.Contains(b, `href="https://evil"`) || strings.Contains(b, "<img") {
		t.Fatalf("external image or link rendered:\n%s", b)
	}
	mustContain(t, b, `href="http://127.0.0.1:4747/hour/2026-10-07/09"`, "click")
}

func TestJournalMarkdownIsSanitized(t *testing.T) {
	e := newEnv(t)
	writeFile(t, e.p.DailyJournal(seedDay), "# Day\n![](https://x/y?d=secret) [click](https://evil)\n")
	writeFile(t, e.p.WeeklyJournal("2026-W41"), "# Week\n![](https://x/w) [w](https://evil/w)\n")
	for _, p := range []string{"/day/" + seedDay, "/week/2026-W41"} {
		b := e.get(p).Body.String()
		if strings.Contains(b, "https://x/") || strings.Contains(b, `href="https://evil`) {
			t.Errorf("%s renders external image or link", p)
		}
	}
}
