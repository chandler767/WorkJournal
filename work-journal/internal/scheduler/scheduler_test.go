package scheduler

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"workjournal/internal/config"
	"workjournal/internal/store"
)

type fakeJobs struct{ calls []string }

func (f *fakeJobs) rec(s string) error                   { f.calls = append(f.calls, s); return nil }
func (f *fakeJobs) ProcessPending(context.Context) error { return f.rec("process") }
func (f *fakeJobs) ReprocessHour(_ context.Context, d string, h int) error {
	return f.rec(fmt.Sprintf("reprocess %s %d", d, h))
}
func (f *fakeJobs) CloseDay(_ context.Context, d string) error { return f.rec("close " + d) }
func (f *fakeJobs) Rollup(_ context.Context, w string) error   { return f.rec("rollup " + w) }
func (f *fakeJobs) Retain(context.Context) error               { return f.rec("retain") }
func (f *fakeJobs) DiskCheck(context.Context) error            { return f.rec("disk") }
func (f *fakeJobs) EnrollSpeaker(_ context.Context, m, s, n string) error {
	return f.rec("enroll " + m + s + n)
}

func newSched(t *testing.T) (*Scheduler, *fakeJobs, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	j := &fakeJobs{}
	return &Scheduler{
		Cfg:   func() config.Config { return config.Default() },
		Jobs:  j,
		Store: st,
		Logf:  func(string, ...any) {},
	}, j, st
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

func at(day string, h, m int) time.Time {
	d, _ := time.ParseInLocation("2006-01-02", day, time.Local)
	return d.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
}

func TestTickProcessesOncePerHour(t *testing.T) {
	s, j, _ := newSched(t)
	ctx := context.Background()
	s.Tick(ctx, at("2026-10-07", 9, 0))
	if got := j.without(); !reflect.DeepEqual(got, []string{"process", "retain"}) {
		t.Fatalf("first tick: %v", got)
	}
	s.Tick(ctx, at("2026-10-07", 9, 30))
	if got := j.without(); len(got) != 0 {
		t.Fatalf("same hour: %v", got)
	}
	s.Tick(ctx, at("2026-10-07", 10, 0))
	if got := j.without(); !reflect.DeepEqual(got, []string{"process", "retain"}) {
		t.Fatalf("next hour: %v", got)
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

func TestTickClosesDayAtCloseTime(t *testing.T) {
	s, j, _ := newSched(t)
	ctx := context.Background()
	s.Tick(ctx, at("2026-10-07", 17, 59))
	j.without()
	s.Tick(ctx, at("2026-10-07", 17, 59))
	if got := j.without(); len(got) != 0 {
		t.Fatalf("17:59 closed: %v", got)
	}
	s.Tick(ctx, at("2026-10-07", 18, 0))
	got := j.without()
	if !contains(got, "close 2026-10-07") {
		t.Fatalf("18:00 did not close: %v", got)
	}
	s.Tick(ctx, at("2026-10-07", 18, 30))
	if got := j.without(); contains(got, "close 2026-10-07") {
		t.Fatalf("closed again: %v", got)
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
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
	s.Tick(context.Background(), at("2026-10-07", 8, 0))
	var closes []string
	for _, c := range j.calls {
		if len(c) > 5 && c[:5] == "close" {
			closes = append(closes, c)
		}
	}
	want := []string{"close 2026-10-05", "close 2026-10-06"}
	if !reflect.DeepEqual(closes, want) {
		t.Fatalf("closes = %v, want %v", closes, want)
	}
}

func TestTickFirstRunDoesNotCatchUp(t *testing.T) {
	s, j, st := newSched(t)
	if err := st.UpsertHour(store.HourRecord{Day: "2026-10-05", Hour: 10, Status: store.StatusDone}); err != nil {
		t.Fatal(err)
	}
	s.Tick(context.Background(), at("2026-10-07", 8, 0))
	if got := j.without(); contains(got, "close 2026-10-05") {
		t.Fatalf("caught up on first run: %v", got)
	}
}

func TestTickFailedJobDoesNotAdvanceMarkerOrStopOthers(t *testing.T) {
	s, _, st := newSched(t)
	fj := &failingClose{}
	s.Jobs = fj
	s.Tick(context.Background(), at("2026-10-07", 18, 0))
	if v, ok, _ := st.GetKV("last_close_day"); ok {
		t.Fatalf("marker advanced to %q", v)
	}
	if v, ok, _ := st.GetKV("last_process_hour"); !ok || v != "2026-10-07T18" {
		t.Fatalf("process marker = %q %v", v, ok)
	}
	s.Tick(context.Background(), at("2026-10-07", 18, 1))
	if fj.closes != 2 {
		t.Fatalf("close attempts = %d, want retry", fj.closes)
	}
}

type failingClose struct {
	fakeJobs
	closes int
}

func (f *failingClose) CloseDay(context.Context, string) error {
	f.closes++
	return fmt.Errorf("boom")
}

func TestRollupOnlyOnRollupWeekday(t *testing.T) {
	s, j, _ := newSched(t)
	ctx := context.Background()
	// Thursday Oct 8 2026 after close: no rollup.
	s.Tick(ctx, at("2026-10-08", 18, 5))
	if got := j.without(); contains(got, "rollup 2026-W41") {
		t.Fatalf("rollup on Thursday: %v", got)
	}
	// Friday Oct 9 before close: no rollup.
	s.Tick(ctx, at("2026-10-09", 12, 0))
	if got := j.without(); contains(got, "rollup 2026-W41") {
		t.Fatalf("rollup before close: %v", got)
	}
	// Friday after close: close then rollup.
	s.Tick(ctx, at("2026-10-09", 18, 0))
	got := j.without()
	ci, ri := -1, -1
	for i, c := range got {
		if c == "close 2026-10-09" {
			ci = i
		}
		if c == "rollup 2026-W41" {
			ri = i
		}
	}
	if ci < 0 || ri < ci {
		t.Fatalf("want close then rollup, got %v", got)
	}
	s.Tick(ctx, at("2026-10-09", 18, 30))
	if got := j.without(); contains(got, "rollup 2026-W41") {
		t.Fatalf("rollup repeated: %v", got)
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
	s.Tick(ctx, at("2026-10-07", 10, 0))
	s.Tick(ctx, at("2026-10-07", 10, 1))
	if fp.process != 1 {
		t.Fatalf("process calls after 10:01 = %d, want 1", fp.process)
	}
	s.Tick(ctx, at("2026-10-07", 11, 0))
	if fp.process != 2 {
		t.Fatalf("process calls after 11:00 = %d, want 2", fp.process)
	}
}

func TestTickRetainFailureDoesNotBlockProcessing(t *testing.T) {
	s, _, st := newSched(t)
	fp := &failingProcess{failRetain: true}
	s.Jobs = fp
	ctx := context.Background()
	s.Tick(ctx, at("2026-10-07", 10, 0))
	s.Tick(ctx, at("2026-10-07", 10, 1))
	if fp.process != 1 || fp.retain != 1 {
		t.Fatalf("process=%d retain=%d, want 1 and 1", fp.process, fp.retain)
	}
	if v, _, _ := st.GetKV("last_process_hour"); v != "2026-10-07T10" {
		t.Fatalf("process marker = %q", v)
	}
	s.Tick(ctx, at("2026-10-07", 11, 0))
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
	s.Tick(ctx, at("2026-10-07", 10, 0))
	for _, k := range []string{"last_process_hour", "last_retain_hour"} {
		if v, ok, _ := st.GetKV(k); ok {
			t.Fatalf("%s advanced to %q", k, v)
		}
	}
	s.Tick(context.Background(), at("2026-10-07", 10, 1))
	if fp.process != 2 || fp.retain != 2 {
		t.Fatalf("process=%d retain=%d, want retry", fp.process, fp.retain)
	}
}
