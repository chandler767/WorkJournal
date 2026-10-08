package web

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"workjournal/internal/config"
	"workjournal/internal/process"
	"workjournal/internal/scheduler"
	"workjournal/internal/store"
)

func TestProcessNowRedirectsWithNotice(t *testing.T) {
	e := newEnv(t)
	e.jobs.upto = seedTime(10, 32)
	rec := e.post("/process-now", url.Values{}, nil)
	if rec.Code != 303 || rec.Header().Get("Location") != "/day/2026-10-07?notice=processed&upto=10:32" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if e.jobs.processedNow != 1 {
		t.Fatalf("ProcessNow called %d times", e.jobs.processedNow)
	}
	body := e.get("/day/2026-10-07?notice=processed&upto=10:32").Body.String()
	mustContain(t, body, "Processed up to 10:32")
}

func TestProcessNowCrossOriginRefusedAndErrorsShown(t *testing.T) {
	e := newEnv(t)
	if c := e.post("/process-now", url.Values{}, map[string]string{"Origin": "http://evil.example"}).Code; c != 403 {
		t.Fatalf("cross-origin code %d", c)
	}
	if e.jobs.processedNow != 0 {
		t.Fatal("cross-origin request processed")
	}
	e.jobs.upto = seedTime(10, 32)
	e.jobs.nowErr = errors.New("whisper failed")
	rec := e.post("/process-now", url.Values{}, nil)
	if rec.Code != 303 || rec.Header().Get("Location") != "/day/2026-10-07?notice=process-failed" {
		t.Fatalf("failure: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	mustContain(t, e.get("/day/2026-10-07?notice=process-failed").Body.String(), "Processing failed")
}

func TestDayNoticeIsAllowListed(t *testing.T) {
	e := newEnv(t)
	for _, q := range []string{"notice=<script>x</script>", "notice=processed&upto=<b>1</b>", "notice=processed&upto=99:99:99"} {
		body := e.get("/day/2026-10-07?" + q).Body.String()
		if strings.Contains(body, "<script>x") || strings.Contains(body, "<b>1") || strings.Contains(body, "99:99") {
			t.Errorf("%s reflected", q)
		}
	}
	mustContain(t, e.get("/day/2026-10-07?notice=processed&upto=bad").Body.String(), "Processed")
}

func TestProcessNowButtonOnTodayOnly(t *testing.T) {
	e := newEnv(t)
	today := config.DayKey(time.Now())
	mustContain(t, e.get("/day/"+today).Body.String(), `action="/process-now"`, "Process now")
	if strings.Contains(e.get("/day/2026-10-01").Body.String(), `action="/process-now"`) {
		t.Error("Process now offered on a past day")
	}
}

func TestPartialHourShowsInProgress(t *testing.T) {
	e := newEnv(t)
	if err := e.st.UpsertHour(store.HourRecord{Day: seedDay, Hour: 11, Status: store.StatusPartial, UpdatedAt: seedTime(11, 15),
		SummaryJSON: `{"narrative":"Started the cache work.","timeline":[],"notes":["Cache design"],"main_focus":"Cache","action_items":[]}`}); err != nil {
		t.Fatal(err)
	}
	day := e.get("/day/" + seedDay).Body.String()
	mustContain(t, day, "in progress", "Cache design")
	hour := e.get("/hour/" + seedDay + "/11").Body.String()
	mustContain(t, hour, "in progress", "Summary so far — updates every 15 min", "Started the cache work.")
	if strings.Contains(e.get("/hour/"+seedDay+"/09").Body.String(), "Summary so far") {
		t.Error("finished hour labelled in progress")
	}
}

// A failed reprocess is logged and shown as an allow-listed notice on the
// day page instead of a bare 500.
func TestReprocessFailureLoggedAndNoticed(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	for _, tc := range []struct {
		err    error
		notice string
		text   string
	}{
		{fmt.Errorf("%w: 1555-system.m4a: afconvert: exit 1", process.ErrChunkNotReady), "reprocess-not-ready", "still being recorded"},
		{scheduler.ErrMediaExpired, "reprocess-expired", "raw recordings were already removed"},
		{errors.New("whisper-cli: exit 2"), "reprocess-failed", "Reprocessing failed"},
	} {
		e := newEnv(t)
		logs.Reset()
		e.jobs.reprocessErr = tc.err
		rec := e.post("/hour/2026-10-07/15/reprocess", nil, nil)
		if want := "/day/2026-10-07?notice=" + tc.notice; rec.Code != 303 || rec.Header().Get("Location") != want {
			t.Errorf("%v: got %d %q, want 303 %q", tc.err, rec.Code, rec.Header().Get("Location"), want)
		}
		if !strings.Contains(logs.String(), "2026-10-07 15") || !strings.Contains(logs.String(), tc.err.Error()) {
			t.Errorf("%v: log %q lacks the hour and error", tc.err, logs.String())
		}
		mustContain(t, e.get("/day/2026-10-07?notice="+tc.notice).Body.String(), tc.text)
	}
}
