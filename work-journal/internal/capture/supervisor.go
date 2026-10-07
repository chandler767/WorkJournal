package capture

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"time"

	"workjournal/internal/config"
)

// RecorderArgs builds the recorder `run` argument list from the config.
func RecorderArgs(c config.Config, p config.Paths) []string {
	args := []string{
		"run",
		"--data-dir", p.Root,
		"--socket", p.Socket(),
		"--interval", strconv.Itoa(c.ScreenshotIntervalSec),
		"--meeting-interval", strconv.Itoa(c.MeetingScreenshotIntervalSec),
		"--chunk-minutes", strconv.Itoa(c.AudioChunkMinutes),
		"--width", strconv.Itoa(c.ScreenshotWidth),
		"--idle-minutes", strconv.Itoa(c.IdleMinutes),
		"--pause-auto-resume", strconv.Itoa(c.PauseAutoResumeMinutes),
		"--dashboard-url", "http://" + c.ListenAddr,
	}
	if c.AllDisplays {
		args = append(args, "--all-displays")
	}
	for _, kv := range []struct {
		flag string
		vals []string
	}{
		{"--exclude-app", c.ExcludedApps},
		{"--exclude-title", c.ExcludedTitleKeywords},
		{"--meeting-app", c.MeetingApps},
		{"--meeting-title", c.MeetingTitleKeywords},
	} {
		for _, v := range kv.vals {
			args = append(args, kv.flag, v)
		}
	}
	return args
}

const (
	minBackoff    = time.Second
	maxBackoff    = 60 * time.Second
	healthyUptime = 5 * time.Minute
	failThreshold = 3
	degradedFlag  = "--degraded"
)

// Supervisor runs the recorder and restarts it when it exits.
type Supervisor struct {
	Bin  string
	Args []string
	Logf func(string, ...any)
	// Start launches the process and returns a function that blocks until it exits.
	Start func(ctx context.Context, bin string, args []string) (wait func() error, err error)
	// OnGap is called for each downtime: from = process exit, to = next successful start.
	OnGap func(from, to time.Time)
	// Sleep defaults to a context-aware time.Sleep.
	Sleep func(time.Duration)
	// Now defaults to time.Now.
	Now func() time.Time
}

func execStart(_ context.Context, bin string, args []string) (func() error, error) {
	cmd := exec.Command(bin, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd.Wait, nil
}

// Run supervises the recorder until ctx is cancelled, then waits for the
// process to exit and returns nil. It sends nothing to the process itself.
func (s *Supervisor) Run(ctx context.Context) error {
	logf := s.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	start := s.Start
	if start == nil {
		start = execStart
	}
	now := s.Now
	if now == nil {
		now = time.Now
	}
	sleep := s.Sleep
	if sleep == nil {
		sleep = func(d time.Duration) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
			}
		}
	}

	backoff := minBackoff
	var failures []time.Time // recent short-lived runs
	degraded := false
	var downSince time.Time // zero when the recorder is up

	fail := func(at time.Time) {
		failures = append(failures, at)
		kept := failures[:0]
		for _, f := range failures {
			if at.Sub(f) <= healthyUptime {
				kept = append(kept, f)
			}
		}
		failures = kept
		if len(failures) >= failThreshold && !degraded {
			degraded = true
			logf("recorder failing: %d failures within %s, restarting in degraded mode", len(failures), healthyUptime)
		}
	}

	for ctx.Err() == nil {
		args := append([]string(nil), s.Args...)
		if degraded {
			args = append(args, degradedFlag)
		}
		wait, err := start(ctx, s.Bin, args)
		began := now()
		if err != nil {
			logf("recorder start failed: %v", err)
			if downSince.IsZero() {
				downSince = began
			}
			fail(began)
		} else {
			if !downSince.IsZero() {
				if s.OnGap != nil {
					s.OnGap(downSince, began)
				}
				downSince = time.Time{}
			}
			werr := wait()
			exited := now()
			downSince = exited
			if ctx.Err() != nil {
				return nil
			}
			if exited.Sub(began) >= healthyUptime {
				backoff = minBackoff
				failures = nil
				degraded = false
			} else {
				fail(exited)
			}
			logf("recorder exited after %s: %v", exited.Sub(began).Round(time.Second), werr)
		}
		if ctx.Err() != nil {
			return nil
		}
		sleep(backoff)
		backoff = min(backoff*2, maxBackoff)
	}
	return nil
}
