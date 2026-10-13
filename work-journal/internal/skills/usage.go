package skills

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ErrUsageLimited means claude refused to run because a Claude usage or rate
// limit was reached. ResetAt is when the limit resets, zero when claude did
// not say. It is transient (errors.Is(err, ErrUnavailable) holds) and never
// consumes the second attempt.
type ErrUsageLimited struct {
	ResetAt time.Time
	Message string // what claude said, on one line, possibly cut
}

func (e ErrUsageLimited) Error() string {
	s := "claude usage limit reached"
	if !e.ResetAt.IsZero() {
		s += " (resets " + e.ResetAt.Local().Format("Jan 2 3:04 PM") + ")"
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// Is makes a usage-limited run a transient failure for callers that only
// distinguish ErrUnavailable from ErrInvalidOutput.
func (e ErrUsageLimited) Is(target error) bool { return target == ErrUnavailable }

// usageLimitRE matches what claude says when a usage, rate or credit limit
// stops it.
var usageLimitRE = regexp.MustCompile(`(?i)usage limit|rate[ _]limit|credit balance|out of credits|quota|\b429\b|limit reached|\bresets\b`)

var (
	resetEpochRE = regexp.MustCompile(`\|\s*(\d{10})\b`)
	resetClockRE = regexp.MustCompile(`(?i)\b(?:resets|reset at|try again at)\s+(?:at\s+)?(\d{1,2})(?::(\d{2}))?\s*([ap]\.?m\.?)?`)
)

// ParseUsageLimit reports whether msg (claude's error result, stderr or exit
// error) says a usage limit was reached, and when it resets: a Unix time
// after "|" ("…limit reached|1760400000"), or a local clock time after
// "resets", "reset at" or "try again at" (3pm, 15:00, 9:30 PM; a time
// already past today is tomorrow).
func ParseUsageLimit(msg string, now time.Time) (ErrUsageLimited, bool) {
	if !usageLimitRE.MatchString(msg) {
		return ErrUsageLimited{}, false
	}
	ul := ErrUsageLimited{Message: snippet(msg, resultSnippetMax)}
	if m := resetEpochRE.FindStringSubmatch(msg); m != nil {
		sec, _ := strconv.ParseInt(m[1], 10, 64)
		ul.ResetAt = time.Unix(sec, 0)
		return ul, true
	}
	if m := resetClockRE.FindStringSubmatch(msg); m != nil {
		h, _ := strconv.Atoi(m[1])
		min := 0
		if m[2] != "" {
			min, _ = strconv.Atoi(m[2])
		}
		ampm := strings.ToLower(strings.ReplaceAll(m[3], ".", ""))
		if (m[2] != "" || ampm != "") && min < 60 {
			switch {
			case ampm == "" && h <= 23:
			case ampm != "" && h >= 1 && h <= 12:
				h %= 12
				if ampm == "pm" {
					h += 12
				}
			default:
				return ul, true
			}
			now = now.Local()
			t := time.Date(now.Year(), now.Month(), now.Day(), h, min, 0, 0, time.Local)
			if t.Before(now) {
				t = t.AddDate(0, 0, 1)
			}
			ul.ResetAt = t
		}
	}
	return ul, true
}

// KV is the key-value store holding the pause (store.Store).
type KV interface {
	GetKV(key string) (string, bool, error)
	SetKV(key, val string) error
}

// KV keys of the usage-limit pause.
const (
	KVPausedUntil     = "claude_paused_until"      // RFC3339; "" when not paused
	KVPauseReason     = "claude_pause_reason"      // what claude said
	KVPauseResetKnown = "claude_pause_reset_known" // "true" when claude said when the limit resets
)

// DefaultPause is how long Claude work pauses when claude did not say when
// its limit resets; MaxPause bounds any pause.
const (
	DefaultPause = time.Hour
	MaxPause     = 24 * time.Hour
)

// PauseState is the stored usage-limit pause. Until is zero when there is
// none; Active is whether it is still in effect at the time it was read.
type PauseState struct {
	Until      time.Time
	Reason     string
	ResetKnown bool
	Active     bool
}

// ReadPause returns the stored pause as of now.
func ReadPause(kv KV, now time.Time) (PauseState, error) {
	v, _, err := kv.GetKV(KVPausedUntil)
	if err != nil || v == "" {
		return PauseState{}, err
	}
	until, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return PauseState{}, fmt.Errorf("%s %q: %w", KVPausedUntil, v, err)
	}
	reason, _, err := kv.GetKV(KVPauseReason)
	if err != nil {
		return PauseState{}, err
	}
	known, _, err := kv.GetKV(KVPauseResetKnown)
	if err != nil {
		return PauseState{}, err
	}
	return PauseState{Until: until, Reason: reason, ResetKnown: known == "true", Active: now.Before(until)}, nil
}

// SetPause pauses Claude work until ul's reset time, or for DefaultPause
// when that is unknown or already past, and never for more than MaxPause.
func SetPause(kv KV, now time.Time, ul ErrUsageLimited) error {
	until, known := ul.ResetAt, true
	if !until.After(now) {
		until, known = now.Add(DefaultPause), false
	}
	if until.After(now.Add(MaxPause)) {
		until = now.Add(MaxPause)
	}
	if err := kv.SetKV(KVPauseReason, ul.Message); err != nil {
		return err
	}
	if err := kv.SetKV(KVPauseResetKnown, strconv.FormatBool(known)); err != nil {
		return err
	}
	return kv.SetKV(KVPausedUntil, until.Local().Format(time.RFC3339))
}

// ClearPause removes the pause.
func ClearPause(kv KV) error {
	for _, k := range []string{KVPausedUntil, KVPauseReason, KVPauseResetKnown} {
		if err := kv.SetKV(k, ""); err != nil {
			return err
		}
	}
	return nil
}

// PausingRunner pauses all Claude work once claude reports a usage limit:
// a run returning ErrUsageLimited stores a pause (SetPause), and while it
// lasts every run returns ErrUsageLimited at once without starting claude.
// Enabled (pause_on_usage_limit) off disables both; nil means on.
type PausingRunner struct {
	Runner  Runner
	KV      KV
	Enabled func() bool
	Now     func() time.Time // nil: time.Now
	Logf    func(string, ...any)
}

func (g *PausingRunner) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *PausingRunner) enabled() bool { return g.Enabled == nil || g.Enabled() }

func (g *PausingRunner) Run(ctx context.Context, skill, inputPath string, out any) error {
	if !g.enabled() {
		return g.Runner.Run(ctx, skill, inputPath, out)
	}
	now := g.now()
	st, err := ReadPause(g.KV, now)
	if err != nil {
		return fmt.Errorf("skill %s: read claude pause: %w", skill, err)
	}
	if st.Active {
		return fmt.Errorf("skill %s: claude paused: %w", skill, ErrUsageLimited{ResetAt: st.Until, Message: st.Reason})
	}
	err = g.Runner.Run(ctx, skill, inputPath, out)
	var ul ErrUsageLimited
	if errors.As(err, &ul) {
		if perr := SetPause(g.KV, g.now(), ul); perr != nil && g.Logf != nil {
			g.Logf("skills: store claude pause: %v", perr)
		}
	}
	return err
}
