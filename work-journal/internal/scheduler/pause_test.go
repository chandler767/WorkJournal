package scheduler

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"workjournal/internal/config"
	"workjournal/internal/skills"
)

func logsTo(s *Scheduler) *[]string {
	var logs []string
	s.Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	return &logs
}

func countContaining(logs []string, sub string) int {
	n := 0
	for _, l := range logs {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

// While Claude is paused the close and rollup are not attempted, their
// markers and failure counts stay, and the pause is logged once.
func TestTickWhilePausedSkipsCloseAndRollupAndLogsOnce(t *testing.T) {
	s, j, st, js := newSchedJ(t)
	logs := logsTo(s)
	js["2026-10-09"] = true
	if err := st.SetKV("last_close_day", "2026-10-08"); err != nil {
		t.Fatal(err)
	}
	now := at("2026-10-09", 18, 2)
	if err := skills.SetPause(st, now, skills.ErrUsageLimited{ResetAt: at("2026-10-09", 20, 0)}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s.Tick(ctx, now)
	s.Tick(ctx, at("2026-10-09", 18, 30))
	s.Tick(ctx, at("2026-10-09", 19, 2))
	if n := countPrefix(j.calls, "close ") + countPrefix(j.calls, "rollup "); n != 0 {
		t.Fatalf("close/rollup attempted while paused: %v", j.calls)
	}
	for _, k := range []string{"last_close_attempt_hour", "last_rollup_attempt_hour", "close_failures:2026-10-09", "last_rollup_week"} {
		if v := kv(t, st, k); v != "" {
			t.Fatalf("%s = %q while paused", k, v)
		}
	}
	if v := kv(t, st, "last_close_day"); v != "2026-10-08" {
		t.Fatalf("last_close_day advanced to %q", v)
	}
	if n := countContaining(*logs, "summaries paused until"); n != 1 {
		t.Fatalf("pause logged %d times: %q", n, *logs)
	}
	// Local processing still runs hourly (transcription, OCR).
	if n := countPrefix(j.calls, "process"); n != 2 {
		t.Fatalf("process ran %d times, want 2 (hours 18 and 19): %v", n, j.calls)
	}
}

// When the pause has passed the KV is cleared and pending hours are
// processed on that tick, even though this hour's run already happened;
// close and rollup follow.
func TestTickResumeProcessesPendingImmediately(t *testing.T) {
	s, j, st, js := newSchedJ(t)
	logs := logsTo(s)
	js["2026-10-09"] = true
	if err := st.SetKV("last_close_day", "2026-10-08"); err != nil {
		t.Fatal(err)
	}
	if err := skills.SetPause(st, at("2026-10-09", 17, 0), skills.ErrUsageLimited{ResetAt: at("2026-10-09", 18, 20)}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s.Tick(ctx, at("2026-10-09", 18, 2))
	j.without()
	s.Tick(ctx, at("2026-10-09", 18, 20))
	got := j.without()
	if index(got, "process") != 0 {
		t.Fatalf("resume tick did not process pending first: %v", got)
	}
	if !contains(got, "close 2026-10-09") || !contains(got, "rollup 2026-W41") {
		t.Fatalf("close/rollup not retried after resume: %v", got)
	}
	if st2, _ := skills.ReadPause(st, at("2026-10-09", 18, 20)); !st2.Until.IsZero() {
		t.Fatalf("pause not cleared: %+v", st2)
	}
	if n := countContaining(*logs, "resuming"); n != 1 {
		t.Fatalf("resume logged %d times: %q", n, *logs)
	}
}

// A close that hits the usage limit does not count as a failure nor use up
// the hour's attempt.
func TestTickUsageLimitedCloseIsNotAFailure(t *testing.T) {
	s, j, st := newSched(t)
	now := at("2026-10-07", 18, 2)
	j.closeErr = fmt.Errorf("journal: close: %w", skills.ErrUsageLimited{ResetAt: at("2026-10-07", 18, 40)})
	s.Tick(context.Background(), now)
	if v := kv(t, st, "close_failures:2026-10-07"); v != "" {
		t.Fatalf("close_failures = %q", v)
	}
	if v := kv(t, st, "last_close_day"); v != "" {
		t.Fatalf("last_close_day = %q", v)
	}
	if v := kv(t, st, "last_close_attempt_hour"); v != "" {
		t.Fatalf("attempt used up: %q", v)
	}
}

// With pause_on_usage_limit off a usage-limited close counts as a failure,
// as before, and a stored pause is ignored.
func TestTickPauseDisabled(t *testing.T) {
	s, j, st := newSched(t)
	cfg := config.Default()
	cfg.ProcessIntervalMinutes = 60
	cfg.PauseOnUsageLimit = false
	s.Cfg = func() config.Config { return cfg }
	now := at("2026-10-07", 18, 2)
	if err := skills.SetPause(st, now, skills.ErrUsageLimited{ResetAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	j.closeErr = skills.ErrUsageLimited{}
	s.Tick(context.Background(), now)
	if !contains(j.calls, "close 2026-10-07") {
		t.Fatalf("close skipped although pausing is off: %v", j.calls)
	}
	if v := kv(t, st, "close_failures:2026-10-07"); v != "1" {
		t.Fatalf("close_failures = %q, want 1", v)
	}
}
