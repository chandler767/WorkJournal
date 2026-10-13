package skills

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParseUsageLimit(t *testing.T) {
	now := time.Date(2026, 10, 13, 10, 0, 0, 0, time.Local)
	today := func(h, m int) time.Time { return time.Date(2026, 10, 13, h, m, 0, 0, time.Local) }
	cases := []struct {
		msg     string
		limited bool
		reset   time.Time
	}{
		{"Claude AI usage limit reached|1760400000", true, time.Unix(1760400000, 0)},
		{"5-hour limit reached ∙ resets 3pm", true, today(15, 0)},
		{"You've hit your limit · resets 3pm (Europe/London)", true, today(15, 0)},
		{"Usage limit reached, resets 15:00", true, today(15, 0)},
		{"Usage limit reached, resets 9:00", true, today(9, 0).AddDate(0, 0, 1)}, // earlier than now: tomorrow
		{"Rate limited. Please try again at 9:30 PM.", true, today(21, 30)},
		{"Weekly limit reached, reset at 12am", true, today(0, 0).AddDate(0, 0, 1)},
		{"API Error: 429 {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\"}}", true, time.Time{}},
		{"Credit balance is too low", true, time.Time{}},
		{"You are out of credits", true, time.Time{}},
		{"Quota exceeded for this organization", true, time.Time{}},
		// A time zone after the time is ignored: the time is read as local.
		{"You've hit your limit · resets 3pm (America/New_York)", true, today(15, 0)},
		// A date-qualified reset is not parsed: the pause falls back to an hour.
		{"Weekly limit reached · resets Oct 20, 9am", true, time.Time{}},
		// "resets" alone is not a usage limit.
		{"Your session resets 3pm", false, time.Time{}},
		{"Counter resets at 15:00 every day", false, time.Time{}},
		{"Invalid API key · Please run /login", false, time.Time{}},
		{"claude: exit status 1: something went wrong (pid 14290)", false, time.Time{}},
		{"", false, time.Time{}},
	}
	for _, c := range cases {
		ul, ok := ParseUsageLimit(c.msg, now)
		if ok != c.limited || !ul.ResetAt.Equal(c.reset) {
			t.Errorf("ParseUsageLimit(%q) = %v, %v; want %v, %v", c.msg, ul.ResetAt, ok, c.reset, c.limited)
		}
	}
}

func TestRunnerUsageLimitedEnvelopeIsNotRetried(t *testing.T) {
	f := &fakeExec{outs: []string{envelope("Claude AI usage limit reached|1760400000", true), envelope(goodHour, false)}}
	var got HourSummary
	err := newRunner(f).Run(context.Background(), "summarize-hour", "/data/in.json", &got)
	var ul ErrUsageLimited
	if !errors.As(err, &ul) {
		t.Fatalf("err = %v, want ErrUsageLimited", err)
	}
	if !ul.ResetAt.Equal(time.Unix(1760400000, 0)) {
		t.Fatalf("ResetAt = %v", ul.ResetAt)
	}
	if len(f.calls) != 1 {
		t.Fatalf("Exec called %d times, want 1 (no second attempt)", len(f.calls))
	}
	if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("usage-limited must stay transient: %v", err)
	}
}

func TestRunnerUsageLimitedNonZeroExit(t *testing.T) {
	// claude exits non-zero with the is_error envelope on stdout.
	f := &fakeExec{outs: []string{envelope("5-hour limit reached ∙ resets 3pm", true)}, err: errors.New("claude: exit status 1: ")}
	err := newRunner(f).Run(context.Background(), "close-day", "/data/in.json", &Markdown{})
	var ul ErrUsageLimited
	if !errors.As(err, &ul) || ul.ResetAt.Hour() != 15 {
		t.Fatalf("err = %v (reset %v), want ErrUsageLimited resetting at 15:00", err, ul.ResetAt)
	}
	if len(f.calls) != 1 {
		t.Fatalf("Exec called %d times, want 1", len(f.calls))
	}

	// Only stderr says so.
	f = &fakeExec{err: errors.New("claude: exit status 1: API Error: 429 rate_limit_error")}
	err = newRunner(f).Run(context.Background(), "close-day", "/data/in.json", &Markdown{})
	if !errors.As(err, &ul) || !ul.ResetAt.IsZero() || len(f.calls) != 1 {
		t.Fatalf("err = %v, calls %d; want ErrUsageLimited with unknown reset after 1 call", err, len(f.calls))
	}

	// An ordinary failure is still retried once.
	f = &fakeExec{err: errors.New("claude: exit status 1: boom")}
	err = newRunner(f).Run(context.Background(), "close-day", "/data/in.json", &Markdown{})
	if errors.As(err, &ul) || len(f.calls) != 2 {
		t.Fatalf("err = %v, calls %d; want a plain failure after 2 calls", err, len(f.calls))
	}
}

func TestRunnerSuccessfulResultMentioningLimitsIsNotUsageLimited(t *testing.T) {
	out := `{"narrative":"Hit the API rate limit (429) and fixed the quota; resets 3pm","timeline":[],"notes":[],"main_focus":"x","action_items":[]}`
	f := &fakeExec{outs: []string{envelope(out, false)}}
	var got HourSummary
	if err := newRunner(f).Run(context.Background(), "summarize-hour", "/data/in.json", &got); err != nil {
		t.Fatal(err)
	}
}

// memKV is an in-memory KV.
type memKV map[string]string

func (m memKV) GetKV(k string) (string, bool, error) { v, ok := m[k]; return v, ok, nil }
func (m memKV) SetKV(k, v string) error              { m[k] = v; return nil }

// stubRunner returns err and counts calls.
type stubRunner struct {
	err   error
	calls int
}

func (s *stubRunner) Run(context.Context, string, string, any) error { s.calls++; return s.err }

func TestPausingRunnerSetsPauseAndShortCircuits(t *testing.T) {
	now := time.Date(2026, 10, 13, 10, 0, 0, 0, time.Local)
	kv := memKV{}
	inner := &stubRunner{err: fmt.Errorf("skill summarize-hour: %w", ErrUsageLimited{ResetAt: now.Add(2 * time.Hour), Message: "limit reached"})}
	g := &PausingRunner{Runner: inner, KV: kv, Now: func() time.Time { return now }}

	err := g.Run(context.Background(), "summarize-hour", "/in.json", nil)
	var ul ErrUsageLimited
	if !errors.As(err, &ul) || inner.calls != 1 {
		t.Fatalf("err %v, calls %d", err, inner.calls)
	}
	st, err := ReadPause(kv, now)
	if err != nil || !st.Active || !st.Until.Equal(now.Add(2*time.Hour)) || !st.ResetKnown || !strings.Contains(st.Reason, "limit reached") {
		t.Fatalf("pause = %+v, %v", st, err)
	}

	// While paused every skill short-circuits without running claude.
	inner.err = nil
	for _, skill := range []string{"summarize-hour", "categorize-time", "summarize-meeting", "privacy-filter", "close-day", "weekly-rollup"} {
		err := g.Run(context.Background(), skill, "/in.json", nil)
		if !errors.As(err, &ul) || !ul.ResetAt.Equal(now.Add(2*time.Hour)) {
			t.Fatalf("%s while paused: %v", skill, err)
		}
	}
	if inner.calls != 1 {
		t.Fatalf("claude ran %d times while paused", inner.calls-1)
	}

	// Once the pause has passed, claude runs again.
	now = now.Add(2 * time.Hour)
	if err := g.Run(context.Background(), "summarize-hour", "/in.json", nil); err != nil || inner.calls != 2 {
		t.Fatalf("after pause: err %v, calls %d", err, inner.calls)
	}
}

func TestPausingRunnerUnknownAndFarResets(t *testing.T) {
	now := time.Date(2026, 10, 13, 10, 0, 0, 0, time.Local)
	for _, c := range []struct {
		reset time.Time
		want  time.Time
		known bool
	}{
		{time.Time{}, now.Add(time.Hour), false},
		{now.Add(30 * time.Hour), now.Add(24 * time.Hour), true},
		{now.Add(-time.Hour), now.Add(time.Hour), false}, // already passed
	} {
		kv := memKV{}
		g := &PausingRunner{Runner: &stubRunner{err: ErrUsageLimited{ResetAt: c.reset}}, KV: kv, Now: func() time.Time { return now }}
		g.Run(context.Background(), "close-day", "/in.json", nil)
		st, _ := ReadPause(kv, now)
		if !st.Until.Equal(c.want) || st.ResetKnown != c.known {
			t.Errorf("reset %v: pause %+v, want until %v known %v", c.reset, st, c.want, c.known)
		}
	}
}

func TestPausingRunnerDisabled(t *testing.T) {
	now := time.Date(2026, 10, 13, 10, 0, 0, 0, time.Local)
	kv := memKV{}
	inner := &stubRunner{err: ErrUsageLimited{}}
	g := &PausingRunner{Runner: inner, KV: kv, Now: func() time.Time { return now }, Enabled: func() bool { return false }}
	g.Run(context.Background(), "close-day", "/in.json", nil)
	if st, _ := ReadPause(kv, now); st.Active || st.Until != (time.Time{}) {
		t.Fatalf("pause set while disabled: %+v", st)
	}
	// A pause left from before is ignored too.
	if err := SetPause(kv, now, ErrUsageLimited{ResetAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	inner.err = nil
	if err := g.Run(context.Background(), "close-day", "/in.json", nil); err != nil || inner.calls != 2 {
		t.Fatalf("disabled pause still short-circuits: %v, calls %d", err, inner.calls)
	}
}

func TestClearPause(t *testing.T) {
	now := time.Date(2026, 10, 13, 10, 0, 0, 0, time.Local)
	kv := memKV{}
	SetPause(kv, now, ErrUsageLimited{})
	if err := ClearPause(kv); err != nil {
		t.Fatal(err)
	}
	if st, _ := ReadPause(kv, now); st.Active || !st.Until.IsZero() {
		t.Fatalf("pause not cleared: %+v", st)
	}
}

func TestUnparseablePauseIsClearedAndLoggedOnce(t *testing.T) {
	now := time.Date(2026, 10, 13, 10, 0, 0, 0, time.Local)
	kv := memKV{KVPausedUntil: "soon-ish", KVPauseReason: "x"}
	var logs []string
	inner := &stubRunner{}
	g := &PausingRunner{Runner: inner, KV: kv, Now: func() time.Time { return now }, Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }}
	for i := 0; i < 2; i++ {
		if err := g.Run(context.Background(), "close-day", "/in.json", nil); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if inner.calls != 2 {
		t.Fatalf("claude ran %d times, want 2 (not paused)", inner.calls)
	}
	if kv[KVPausedUntil] != "" {
		t.Fatalf("bad pause not cleared: %q", kv[KVPausedUntil])
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "soon-ish") {
		t.Fatalf("logs = %q, want one line naming the bad value", logs)
	}

	kv[KVPausedUntil] = "garbage"
	st, err := ReadPause(kv, now)
	if !errors.Is(err, ErrBadPause) || st.Active || kv[KVPausedUntil] != "" {
		t.Fatalf("ReadPause = %+v, %v; key %q", st, err, kv[KVPausedUntil])
	}
	if _, err := ReadPause(kv, now); err != nil {
		t.Fatalf("second read: %v", err)
	}
}
