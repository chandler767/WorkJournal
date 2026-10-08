package scheduler

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"workjournal/internal/config"
	"workjournal/internal/store"
)

type fakeJobs struct {
	calls       []string
	failClose   map[string]bool // days whose close fails
	failRoll    bool
	failCurrent bool
}

func (f *fakeJobs) rec(s string) error                   { f.calls = append(f.calls, s); return nil }
func (f *fakeJobs) ProcessPending(context.Context) error { return f.rec("process") }
func (f *fakeJobs) ProcessCurrent(context.Context) error {
	if err := f.rec("current"); err != nil || f.failCurrent {
		return fmt.Errorf("boom")
	}
	return nil
}
func (f *fakeJobs) ProcessNow(context.Context) (time.Time, error) { return time.Time{}, f.rec("now") }
func (f *fakeJobs) ReprocessHour(_ context.Context, d string, h int) error {
	return f.rec(fmt.Sprintf("reprocess %s %d", d, h))
}
func (f *fakeJobs) CloseDay(_ context.Context, d string) error {
	f.rec("close " + d)
	if f.failClose[d] {
		return fmt.Errorf("boom")
	}
	return nil
}
func (f *fakeJobs) Rollup(_ context.Context, w string) error {
	f.rec("rollup " + w)
	if f.failRoll {
		return fmt.Errorf("boom")
	}
	return nil
}
func (f *fakeJobs) Retain(context.Context) error    { return f.rec("retain") }
func (f *fakeJobs) DiskCheck(context.Context) error { return f.rec("disk") }
func (f *fakeJobs) EnrollSpeaker(_ context.Context, m, s, n string) error {
	return f.rec("enroll " + m + s + n)
}

// journals is the set of days with a daily journal file.
type journals map[string]bool

func newSched(t *testing.T) (*Scheduler, *fakeJobs, *store.Store) {
	s, j, st, _ := newSchedJ(t)
	return s, j, st
}

func newSchedJ(t *testing.T) (*Scheduler, *fakeJobs, *store.Store, journals) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	j := &fakeJobs{failClose: map[string]bool{}}
	js := journals{}
	cfg := config.Default()
	cfg.ProcessIntervalMinutes = 60 // hourly processing only; partial runs have their own tests
	return &Scheduler{
		Cfg:             func() config.Config { return cfg },
		Jobs:            j,
		Store:           st,
		Logf:            func(string, ...any) {},
		HasDailyJournal: func(day string) bool { return js[day] },
	}, j, st, js
}

// without returns calls excluding the per-tick disk check.
func (f *fakeJobs) without() []string {
	var out []string
	for _, c := range f.calls {
		if c != "disk" {
			out = append(out, c)
		}
	}
	f.calls = nil
	return out
}

func at(day string, h, m int) time.Time { return atS(day, h, m, 0) }

func atS(day string, h, m, s int) time.Time {
	d, _ := time.ParseInLocation("2006-01-02", day, time.Local)
	return time.Date(d.Year(), d.Month(), d.Day(), h, m, s, 0, time.Local)
}

func kv(t *testing.T, st *store.Store, key string) string {
	t.Helper()
	v, _, err := st.GetKV(key)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTickProcessesOncePerHour(t *testing.T) {
	s, j, _ := newSched(t)
	ctx := context.Background()
	s.Tick(ctx, at("2026-10-07", 9, 2))
	if got := j.without(); !reflect.DeepEqual(got, []string{"process", "retain"}) {
		t.Fatalf("first tick: %v", got)
	}
	s.Tick(ctx, at("2026-10-07", 9, 30))
	if got := j.without(); len(got) != 0 {
		t.Fatalf("same hour: %v", got)
	}
	s.Tick(ctx, at("2026-10-07", 10, 2))
	if got := j.without(); !reflect.DeepEqual(got, []string{"process", "retain"}) {
		t.Fatalf("next hour: %v", got)
	}
}

// I1: the last file of the previous hour may still be fresh just after the
// hour boundary; processing then would mark the hour attempted while the
// previous hour was skipped as fresh.
func TestTickProcessWaitsTwoMinutesIntoTheHour(t *testing.T) {
	s, j, st := newSched(t)
	ctx := context.Background()
	s.Tick(ctx, atS("2026-10-07", 11, 0, 5))
	if got := j.without(); contains(got, "process") {
		t.Fatalf("processed at 11:00:05: %v", got)
	}
	if v := kv(t, st, "last_process_hour"); v != "" {
		t.Fatalf("marker set at 11:00:05: %q", v)
	}
	s.Tick(ctx, atS("2026-10-07", 11, 1, 59))
	if got := j.without(); contains(got, "process") {
		t.Fatalf("processed at 11:01:59: %v", got)
	}
	s.Tick(ctx, at("2026-10-07", 11, 2))
	if got := j.without(); !contains(got, "process") {
		t.Fatalf("not processed at 11:02: %v", got)
	}
	if v := kv(t, st, "last_process_hour"); v != "2026-10-07T11" {
		t.Fatalf("marker = %q", v)
	}
}

func TestTickDiskCheckEveryTick(t *testing.T) {
	s, j, _ := newSched(t)
	s.Tick(context.Background(), at("2026-10-07", 9, 0))
	s.Tick(context.Background(), at("2026-10-07", 9, 1))
	n := 0
	for _, c := range j.calls {
		if c == "disk" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("disk checks = %d", n)
	}
}

func TestTickClosesDayTwoMinutesAfterCloseTime(t *testing.T) {
	s, j, _ := newSched(t)
	ctx := context.Background()
	s.Tick(ctx, at("2026-10-07", 17, 59))
	j.without()
	s.Tick(ctx, atS("2026-10-07", 18, 0, 5))
	if got := j.without(); contains(got, "close 2026-10-07") || contains(got, "process") {
		t.Fatalf("18:00:05 closed or processed: %v", got)
	}
	s.Tick(ctx, at("2026-10-07", 18, 2))
	got := j.without()
	pi, ci := index(got, "process"), index(got, "close 2026-10-07")
	if ci < 0 {
		t.Fatalf("18:02 did not close: %v", got)
	}
	// Hour 17 is processed (its files are settled) before the close.
	if pi < 0 || pi > ci {
		t.Fatalf("want process before close, got %v", got)
	}
	s.Tick(ctx, at("2026-10-07", 18, 30))
	if got := j.without(); contains(got, "close 2026-10-07") {
		t.Fatalf("closed again: %v", got)
	}
}

func contains(list []string, v string) bool { return index(list, v) >= 0 }

func index(list []string, v string) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return -1
}

func closes(calls []string) []string {
	var out []string
	for _, c := range calls {
		if strings.HasPrefix(c, "close ") {
			out = append(out, c)
		}
	}
	return out
}

func TestTickCatchesUpMissedDays(t *testing.T) {
	s, j, st := newSched(t)
	// last close 3 days ago (Oct 4); Oct 5 and Oct 6 have data, Oct 7 is today.
	if err := st.SetKV("last_close_day", "2026-10-04"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"2026-10-05", "2026-10-06"} {
		if err := st.UpsertHour(store.HourRecord{Day: d, Hour: 10, Status: store.StatusDone}); err != nil {
			t.Fatal(err)
		}
	}
	s.Tick(context.Background(), at("2026-10-07", 8, 2))
	want := []string{"close 2026-10-05", "close 2026-10-06"}
	if got := closes(j.calls); !reflect.DeepEqual(got, want) {
		t.Fatalf("closes = %v, want %v", got, want)
	}
}

func TestTickFirstRunDoesNotCatchUp(t *testing.T) {
	s, j, st := newSched(t)
	if err := st.UpsertHour(store.HourRecord{Day: "2026-10-05", Hour: 10, Status: store.StatusDone}); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background(), at("2026-10-07", 8, 2))
	if got := j.without(); contains(got, "close 2026-10-05") {
		t.Fatalf("caught up on first run: %v", got)
	}
}

// I2: a failing close is retried at most once per hour.
func TestTickFailedCloseRetriedOncePerHour(t *testing.T) {
	s, j, st := newSched(t)
	j.failClose["2026-10-07"] = true
	ctx := context.Background()
	s.Tick(ctx, at("2026-10-07", 18, 2))
	if v := kv(t, st, "last_close_day"); v != "" {
		t.Fatalf("marker advanced to %q", v)
	}
	if v := kv(t, st, "last_process_hour"); v != "2026-10-07T18" {
		t.Fatalf("process marker = %q", v)
	}
	s.Tick(ctx, at("2026-10-07", 18, 3))
	s.Tick(ctx, at("2026-10-07", 18, 40))
	if got := closes(j.calls); len(got) != 1 {
		t.Fatalf("closes in hour 18 = %v, want 1", got)
	}
	s.Tick(ctx, at("2026-10-07", 19, 2))
	if got := closes(j.calls); len(got) != 2 {
		t.Fatalf("closes after 19:02 = %v, want 2", got)
	}
	if v := kv(t, st, "close_failures:2026-10-07"); v != "2" {
		t.Fatalf("close_failures = %q", v)
	}
	if v := kv(t, st, "last_close_attempt_hour"); v != "2026-10-07T19" {
		t.Fatalf("last_close_attempt_hour = %q", v)
	}
}

// I2: a day failing three attempts is skipped and later days proceed.
func TestTickCatchUpSkipsDayAfterThreeFailures(t *testing.T) {
	s, j, st := newSched(t)
	if err := st.SetKV("last_close_day", "2026-10-04"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"2026-10-05", "2026-10-06"} {
		if err := st.UpsertHour(store.HourRecord{Day: d, Hour: 10, Status: store.StatusDone}); err != nil {
			t.Fatal(err)
		}
	}
	j.failClose["2026-10-05"] = true
	ctx := context.Background()
	s.Tick(ctx, at("2026-10-07", 8, 2))
	s.Tick(ctx, at("2026-10-07", 8, 30))
	s.Tick(ctx, at("2026-10-07", 9, 2))
	if got := closes(j.calls); !reflect.DeepEqual(got, []string{"close 2026-10-05", "close 2026-10-05"}) {
		t.Fatalf("closes after two hours = %v", got)
	}
	if v := kv(t, st, "last_close_day"); v != "2026-10-04" {
		t.Fatalf("marker = %q before giving up", v)
	}
	j.calls = nil
	s.Tick(ctx, at("2026-10-07", 10, 2))
	if got := closes(j.calls); !reflect.DeepEqual(got, []string{"close 2026-10-05", "close 2026-10-06"}) {
		t.Fatalf("third hour closes = %v, want the failing day then the next", got)
	}
	if v := kv(t, st, "last_close_day"); v != "2026-10-06" {
		t.Fatalf("marker = %q", v)
	}
	if v := kv(t, st, "close_failures:2026-10-05"); v != "3" {
		t.Fatalf("close_failures = %q", v)
	}
}

func TestTickCancelledCloseIsNotCountedOrMarked(t *testing.T) {
	s, j, st := newSched(t)
	j.failClose["2026-10-07"] = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.Tick(ctx, at("2026-10-07", 18, 2))
	for _, k := range []string{"close_failures:2026-10-07", "last_close_attempt_hour"} {
		if v := kv(t, st, k); v != "" {
			t.Fatalf("%s = %q after a cancelled close", k, v)
		}
	}
}

func TestRollupOnlyOnRollupWeekday(t *testing.T) {
	s, j, _, js := newSchedJ(t)
	js["2026-10-08"], js["2026-10-09"] = true, true
	ctx := context.Background()
	// Thursday Oct 8 2026 after close: no rollup.
	s.Tick(ctx, at("2026-10-08", 18, 5))
	if got := j.without(); contains(got, "rollup 2026-W41") {
		t.Fatalf("rollup on Thursday: %v", got)
	}
	// Friday Oct 9 before close: no rollup.
	s.Tick(ctx, at("2026-10-09", 12, 2))
	if got := j.without(); contains(got, "rollup 2026-W41") {
		t.Fatalf("rollup before close: %v", got)
	}
	// Friday at close time but before it settles: nothing.
	s.Tick(ctx, atS("2026-10-09", 18, 0, 5))
	if got := j.without(); contains(got, "rollup 2026-W41") || contains(got, "close 2026-10-09") {
		t.Fatalf("18:00:05: %v", got)
	}
	// Friday after close: close then rollup.
	s.Tick(ctx, at("2026-10-09", 18, 2))
	got := j.without()
	ci, ri := index(got, "close 2026-10-09"), index(got, "rollup 2026-W41")
	if ci < 0 || ri < ci {
		t.Fatalf("want close then rollup, got %v", got)
	}
	s.Tick(ctx, at("2026-10-09", 18, 30))
	if got := j.without(); contains(got, "rollup 2026-W41") {
		t.Fatalf("rollup repeated: %v", got)
	}
}

// I2: a failing rollup is retried at most once per hour.
func TestTickFailedRollupRetriedOncePerHour(t *testing.T) {
	s, j, st, js := newSchedJ(t)
	js["2026-10-09"] = true
	j.failRoll = true
	if err := st.SetKV("last_close_day", "2026-10-08"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s.Tick(ctx, at("2026-10-09", 18, 2))
	s.Tick(ctx, at("2026-10-09", 18, 3))
	s.Tick(ctx, at("2026-10-09", 18, 50))
	if n := countPrefix(j.calls, "rollup "); n != 1 {
		t.Fatalf("rollups in hour 18 = %d, want 1 (%v)", n, j.calls)
	}
	s.Tick(ctx, at("2026-10-09", 19, 2))
	if n := countPrefix(j.calls, "rollup "); n != 2 {
		t.Fatalf("rollups after 19:02 = %d, want 2", n)
	}
	if v := kv(t, st, "last_rollup_week"); v != "" {
		t.Fatalf("rollup marker advanced to %q", v)
	}
	if v := kv(t, st, "last_rollup_attempt_hour"); v != "2026-10-09T19" {
		t.Fatalf("last_rollup_attempt_hour = %q", v)
	}
}

func countPrefix(calls []string, p string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, p) {
			n++
		}
	}
	return n
}

// I5: Friday was not closed live (asleep); Monday's catch-up closes Friday
// and then rolls up that week.
func TestTickMondayCatchUpClosesFridayThenRollsUp(t *testing.T) {
	s, j, st, js := newSchedJ(t)
	if err := st.SetKV("last_close_day", "2026-10-08"); err != nil { // Thursday
		t.Fatal(err)
	}
	if err := st.SetKV("last_rollup_week", "2026-W40"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertHour(store.HourRecord{Day: "2026-10-09", Hour: 10, Status: store.StatusDone}); err != nil {
		t.Fatal(err)
	}
	js["2026-10-08"], js["2026-10-09"] = true, true
	s.Tick(context.Background(), at("2026-10-12", 8, 2)) // Monday
	got := j.without()
	ci, ri := index(got, "close 2026-10-09"), index(got, "rollup 2026-W41")
	if ci < 0 || ri < ci {
		t.Fatalf("want close Friday then rollup W41, got %v", got)
	}
	if v := kv(t, st, "last_rollup_week"); v != "2026-W41" {
		t.Fatalf("last_rollup_week = %q", v)
	}
}

func TestTickRollupCatchUpOldestFirstAtMostFourWeeks(t *testing.T) {
	s, j, st, js := newSchedJ(t)
	if err := st.SetKV("last_close_day", "2026-10-11"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV("last_rollup_week", "2026-W30"); err != nil {
		t.Fatal(err)
	}
	// A journal on the Monday of every week W35..W41.
	for d := time.Date(2026, 8, 24, 0, 0, 0, 0, time.Local); d.Before(time.Date(2026, 10, 12, 0, 0, 0, 0, time.Local)); d = d.AddDate(0, 0, 7) {
		js[config.DayKey(d)] = true
	}
	s.Tick(context.Background(), at("2026-10-12", 8, 2))
	var rolls []string
	for _, c := range j.calls {
		if strings.HasPrefix(c, "rollup ") {
			rolls = append(rolls, c)
		}
	}
	want := []string{"rollup 2026-W38", "rollup 2026-W39", "rollup 2026-W40", "rollup 2026-W41"}
	if !reflect.DeepEqual(rolls, want) {
		t.Fatalf("rollups = %v, want %v", rolls, want)
	}
}

func TestTickRollupSkipsWeekWithoutJournal(t *testing.T) {
	s, j, st, _ := newSchedJ(t)
	if err := st.SetKV("last_close_day", "2026-10-11"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV("last_rollup_week", "2026-W40"); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background(), at("2026-10-12", 8, 2))
	if n := countPrefix(j.calls, "rollup "); n != 0 {
		t.Fatalf("rolled up a week without journals: %v", j.calls)
	}
}

func TestTickRollupWaitsForRollupDayClose(t *testing.T) {
	s, j, st, js := newSchedJ(t)
	js["2026-10-08"] = true
	j.failClose["2026-10-09"] = true
	if err := st.SetKV("last_close_day", "2026-10-08"); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background(), at("2026-10-09", 18, 2))
	if n := countPrefix(j.calls, "rollup "); n != 0 {
		t.Fatalf("rolled up before Friday closed: %v", j.calls)
	}
}

type failingProcess struct {
	fakeJobs
	process, retain int
	failProcess     bool
	failRetain      bool
}

func (f *failingProcess) ProcessPending(context.Context) error {
	f.process++
	if f.failProcess {
		return fmt.Errorf("boom")
	}
	return nil
}

func (f *failingProcess) Retain(context.Context) error {
	f.retain++
	if f.failRetain {
		return fmt.Errorf("boom")
	}
	return nil
}

func TestTickProcessFailureRetriesNextHourNotNextMinute(t *testing.T) {
	s, _, _ := newSched(t)
	fp := &failingProcess{failProcess: true}
	s.Jobs = fp
	ctx := context.Background()
	s.Tick(ctx, at("2026-10-07", 10, 2))
	s.Tick(ctx, at("2026-10-07", 10, 3))
	if fp.process != 1 {
		t.Fatalf("process calls after 10:03 = %d, want 1", fp.process)
	}
	s.Tick(ctx, at("2026-10-07", 11, 2))
	if fp.process != 2 {
		t.Fatalf("process calls after 11:02 = %d, want 2", fp.process)
	}
}

func TestTickRetainFailureDoesNotBlockProcessing(t *testing.T) {
	s, _, st := newSched(t)
	fp := &failingProcess{failRetain: true}
	s.Jobs = fp
	ctx := context.Background()
	s.Tick(ctx, at("2026-10-07", 10, 2))
	s.Tick(ctx, at("2026-10-07", 10, 3))
	if fp.process != 1 || fp.retain != 1 {
		t.Fatalf("process=%d retain=%d, want 1 and 1", fp.process, fp.retain)
	}
	if v := kv(t, st, "last_process_hour"); v != "2026-10-07T10" {
		t.Fatalf("process marker = %q", v)
	}
	s.Tick(ctx, at("2026-10-07", 11, 2))
	if fp.process != 2 || fp.retain != 2 {
		t.Fatalf("process=%d retain=%d, want 2 and 2", fp.process, fp.retain)
	}
}

func TestTickCancelledContextDoesNotAdvanceMarker(t *testing.T) {
	s, _, st := newSched(t)
	fp := &failingProcess{failProcess: true, failRetain: true}
	s.Jobs = fp
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.Tick(ctx, at("2026-10-07", 10, 2))
	for _, k := range []string{"last_process_hour", "last_retain_hour"} {
		if v, ok, _ := st.GetKV(k); ok {
			t.Fatalf("%s advanced to %q", k, v)
		}
	}
	s.Tick(context.Background(), at("2026-10-07", 10, 3))
	if fp.process != 2 || fp.retain != 2 {
		t.Fatalf("process=%d retain=%d, want retry", fp.process, fp.retain)
	}
}

// ProcessOnce is the guard CloseDay's internal ProcessPending goes through.
func TestProcessOnceSharesTheHourlyMarker(t *testing.T) {
	_, _, st := newSched(t)
	ctx := context.Background()
	runs := 0
	job := func(context.Context) error { runs++; return nil }

	if err := ProcessOnce(ctx, st, at("2026-10-07", 18, 2), job); err != nil {
		t.Fatal(err)
	}
	if runs != 1 || kv(t, st, "last_process_hour") != "2026-10-07T18" {
		t.Fatalf("runs=%d marker=%q", runs, kv(t, st, "last_process_hour"))
	}
	ProcessOnce(ctx, st, at("2026-10-07", 18, 40), job)
	if runs != 1 {
		t.Fatalf("ran twice in hour 18")
	}
	// Before the hour settles it runs (manual close) but does not mark, so
	// the hourly run still processes the previous hour once it settles.
	ProcessOnce(ctx, st, atS("2026-10-07", 19, 0, 30), job)
	if runs != 2 || kv(t, st, "last_process_hour") != "2026-10-07T18" {
		t.Fatalf("runs=%d marker=%q", runs, kv(t, st, "last_process_hour"))
	}
}

func TestTickRunsPartialProcessingOnIntervalSlots(t *testing.T) {
	s, j, _ := newSched(t)
	cfg := config.Default()
	cfg.ProcessIntervalMinutes = 15
	s.Cfg = func() config.Config { return cfg }
	ctx := context.Background()
	at := func(m int) time.Time { return time.Date(2026, 10, 7, 10, m, 0, 0, time.Local) }
	for _, c := range []struct {
		min  int
		want []string
	}{
		{0, []string{"retain"}}, // nothing of the new hour to summarize yet
		{1, nil},
		{7, []string{"process"}}, // the hourly run; no partial run at :07
		{14, nil},
		{15, []string{"current"}},
		{16, nil},
		{30, []string{"current"}},
		{44, nil},
		{45, []string{"current"}},
	} {
		j.calls = nil
		s.Tick(ctx, at(c.min))
		if got := j.without(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("10:%02d: calls = %v, want %v", c.min, got, c.want)
		}
	}
}

func TestTickInterval60HasNoPartialRuns(t *testing.T) {
	s, j, _ := newSched(t) // interval 60
	for m := 0; m < 60; m++ {
		s.Tick(context.Background(), time.Date(2026, 10, 7, 10, m, 0, 0, time.Local))
	}
	for _, c := range j.calls {
		if c == "current" {
			t.Fatalf("partial run with interval 60: %v", j.calls)
		}
	}
}

func TestTickPartialFailureWaitsForNextSlot(t *testing.T) {
	s, j, st := newSched(t)
	cfg := config.Default()
	cfg.ProcessIntervalMinutes = 15
	s.Cfg = func() config.Config { return cfg }
	j.failCurrent = true
	ctx := context.Background()
	s.Tick(ctx, time.Date(2026, 10, 7, 10, 15, 0, 0, time.Local))
	j.calls = nil
	s.Tick(ctx, time.Date(2026, 10, 7, 10, 16, 0, 0, time.Local))
	if got := j.without(); len(got) != 0 {
		t.Fatalf("failed partial run retried at once: %v", got)
	}
	if v, _, _ := st.GetKV("last_partial_run"); !strings.HasPrefix(v, "2026-10-07T10:15") {
		t.Fatalf("last_partial_run = %q", v)
	}
}

func TestPartialSlotDoesNotRetryFinishedHours(t *testing.T) {
	s, _, _ := newSched(t)
	cfg := config.Default()
	cfg.ProcessIntervalMinutes = 5
	s.Cfg = func() config.Config { return cfg }
	f := &failingProcess{failProcess: true}
	s.Jobs = f
	for m := 0; m < 60; m++ {
		s.Tick(context.Background(), time.Date(2026, 10, 7, 10, m, 0, 0, time.Local))
	}
	if f.process != 1 {
		t.Fatalf("ProcessPending ran %d times in an hour of partial slots, want 1 (the hourly run)", f.process)
	}
	n := 0
	for _, c := range f.calls {
		if c == "current" {
			n++
		}
	}
	if n != 11 { // :05 … :55
		t.Fatalf("partial runs = %d, want 11", n)
	}
}

func TestPartialSkipsFirstIntervalOfHour(t *testing.T) {
	s, j, _ := newSched(t)
	cfg := config.Default()
	cfg.ProcessIntervalMinutes = 30
	s.Cfg = func() config.Config { return cfg }
	var got []int
	for m := 0; m < 60; m++ {
		j.calls = nil
		s.Tick(context.Background(), time.Date(2026, 10, 7, 10, m, 0, 0, time.Local))
		for _, c := range j.calls {
			if c == "current" {
				got = append(got, m)
			}
		}
	}
	if !reflect.DeepEqual(got, []int{30}) {
		t.Fatalf("partial runs at minutes %v, want [30]", got)
	}
}
