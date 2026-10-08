package web

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"workjournal/internal/store"
)

func revOf(t *testing.T, e *env, path string) string {
	t.Helper()
	rec := e.get(path)
	if rec.Code != 200 {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type %q", ct)
	}
	var v struct{ Rev string }
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || v.Rev == "" {
		t.Fatalf("bad body %q: %v", rec.Body.String(), err)
	}
	return v.Rev
}

func TestDayRevStableThenChanges(t *testing.T) {
	e := newEnv(t)
	day := "/api/rev/day/" + seedDay
	base := revOf(t, e, day)
	if again := revOf(t, e, day); again != base {
		t.Fatalf("rev unstable: %s vs %s", base, again)
	}
	steps := map[string]func(){
		"hour updated": func() {
			upsertHour(t, e, 9, store.StatusDone, `{"narrative":"changed"}`)
		},
		"category overridden": func() {
			if err := e.st.SetSegmentCategory(seedDay, 9, seedTime(9, 40), "Deep work"); err != nil {
				t.Fatal(err)
			}
		},
		"meeting changed": func() {
			if err := e.st.UpsertMeeting(store.MeetingRecord{ID: "2026-10-07-0910-zoom", Day: seedDay, Start: seedTime(9, 10), End: seedTime(9, 30), App: "zoom.us", Title: "Standup", SummaryJSON: `{"summary":"x"}`}); err != nil {
				t.Fatal(err)
			}
		},
		"participant named": func() {
			if err := e.st.SetParticipant(store.ParticipantRecord{MeetingID: "2026-10-07-0910-zoom", Speaker: "S1", Name: "Priya"}); err != nil {
				t.Fatal(err)
			}
		},
		"journal written": func() {
			p := e.p.DailyJournal(seedDay)
			writeFile(t, p, "# Today\nA much longer entry than before.\n")
			future := time.Now().Add(time.Hour)
			if err := os.Chtimes(p, future, future); err != nil {
				t.Fatal(err)
			}
		},
		"recorder paused": func() { e.ctl.state = "paused" },
	}
	prev := base
	for _, name := range []string{"hour updated", "category overridden", "meeting changed", "participant named", "journal written", "recorder paused"} {
		steps[name]()
		now := revOf(t, e, day)
		if now == prev {
			t.Errorf("rev did not change after %s", name)
		}
		prev = now
	}
}

func TestDayRevOtherDayUnaffected(t *testing.T) {
	e := newEnv(t)
	other := "/api/rev/day/2026-10-06"
	base := revOf(t, e, other)
	upsertHour(t, e, 9, store.StatusDone, `{"narrative":"changed"}`)
	if revOf(t, e, other) != base {
		t.Error("rev of another day changed")
	}
}

func TestHourRevStableThenChanges(t *testing.T) {
	e := newEnv(t)
	h := "/api/rev/hour/" + seedDay + "/09"
	base := revOf(t, e, h)
	if revOf(t, e, h) != base {
		t.Fatal("hour rev unstable")
	}
	otherHour := revOf(t, e, "/api/rev/hour/"+seedDay+"/10")
	upsertHour(t, e, 9, store.StatusDone, `{"narrative":"changed"}`)
	r2 := revOf(t, e, h)
	if r2 == base {
		t.Error("hour rev did not change on update")
	}
	if revOf(t, e, "/api/rev/hour/"+seedDay+"/10") != otherHour {
		t.Error("other hour's rev changed")
	}
	if err := e.st.SetSegmentCategory(seedDay, 9, seedTime(9, 40), "Deep work"); err != nil {
		t.Fatal(err)
	}
	if revOf(t, e, h) == r2 {
		t.Error("hour rev did not change on override")
	}
}

func TestRevBadParamsAndHost(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/api/rev/day/nope", "/api/rev/day/2026-13-45", "/api/rev/hour/nope/09", "/api/rev/hour/" + seedDay + "/9", "/api/rev/hour/" + seedDay + "/24", "/api/rev/hour/" + seedDay + "/xx"} {
		if rec := e.get(p); rec.Code != 400 {
			t.Errorf("%s: %d, want 400", p, rec.Code)
		}
	}
	req := httptest.NewRequest("GET", "/api/rev/day/"+seedDay, nil)
	req.Host = "evil.example.com"
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("foreign host: %d, want 403", rec.Code)
	}
}

func TestDayPageEmbedsRevAndMatchesEndpoint(t *testing.T) {
	e := newEnv(t)
	body := e.get("/day/" + seedDay).Body.String()
	m := regexp.MustCompile(`data-rev="([^"]+)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no data-rev on day page")
	}
	if want := revOf(t, e, "/api/rev/day/"+seedDay); m[1] != want {
		t.Errorf("embedded %s != endpoint %s", m[1], want)
	}
	hb := e.get("/hour/" + seedDay + "/09").Body.String()
	hm := regexp.MustCompile(`data-rev="([^"]+)"`).FindStringSubmatch(hb)
	if hm == nil || hm[1] != revOf(t, e, "/api/rev/hour/"+seedDay+"/09") {
		t.Errorf("hour page rev mismatch: %v", hm)
	}
}

func TestDayPageNewestFirst(t *testing.T) {
	e := newEnv(t)
	upsertHour(t, e, 11, store.StatusPartial, `{"narrative":"Now.","notes":["x"],"main_focus":"Current"}`)
	upsertHour(t, e, 2, store.StatusDone, `{}`)
	upsertHour(t, e, 3, store.StatusDone, `{}`)
	b := e.get("/day/" + seedDay).Body.String()
	idx := func(s string) int {
		i := strings.Index(b, s)
		if i < 0 {
			t.Fatalf("missing %q", s)
		}
		return i
	}
	i11, i10, i09 := idx(`/hour/`+seedDay+`/11"`), idx(`/hour/`+seedDay+`/10"`), idx(`/hour/`+seedDay+`/09"`)
	if !(i11 < i10 && i10 < i09) {
		t.Errorf("cards not descending: 11=%d 10=%d 09=%d", i11, i10, i09)
	}
	if !(idx("03:00 · no activity") < idx("02:00 · no activity")) {
		t.Error("no-activity lines not descending")
	}
	if !(i09 < idx("03:00 · no activity")) {
		t.Error("expected 09 card before 03 empty line")
	}
}

func TestDayMeetingsNewestFirst(t *testing.T) {
	e := newEnv(t)
	if err := e.st.UpsertMeeting(store.MeetingRecord{ID: "2026-10-07-1400-meet", Day: seedDay, Start: seedTime(14, 0), End: seedTime(14, 30), App: "Meet", Title: "Planning"}); err != nil {
		t.Fatal(err)
	}
	b := e.get("/day/" + seedDay).Body.String()
	if strings.Index(b, "Planning") > strings.Index(b, "Standup") {
		t.Error("meetings not newest first")
	}
}
