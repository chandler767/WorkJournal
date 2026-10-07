package capture

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"syscall"
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
	// Start launches the process and returns a function that blocks until it
	// exits and a function that signals it.
	Start func(ctx context.Context, bin string, args []string) (wait func() error, signal func(os.Signal) error, err error)
	// StopTimeout is how long to wait for the process to exit after ctx is
	// cancelled before sending SIGTERM (0 means 10s).
	StopTimeout time.Duration
	// OnGap is called for each downtime: from = process exit, to = next successful start.
	OnGap func(from, to time.Time)
	// Sleep defaults to a context-aware time.Sleep.
	Sleep func(time.Duration)
	// Now defaults to time.Now.
	Now func() time.Time
}

func execStart(_ context.Context, bin string, args []string) (func() error, func(os.Signal) error, error) {
	cmd := exec.Command(bin, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return cmd.Wait, cmd.Process.Signal, nil
}

// termGrace is a var so tests can shorten it.
var termGrace = 3 * time.Second

// stop waits for the process to exit after ctx cancellation, escalating to
// SIGTERM and then SIGKILL if it does not.
func (s *Supervisor) stop(exited <-chan error, signal func(os.Signal) error, logf func(string, ...any)) {
	timeout := s.StopTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	wait := func(d time.Duration) bool {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-exited:
			return true
		case <-t.C:
			return false
		}
	}
	if wait(timeout) {
		return
	}
	if signal == nil {
		<-exited
		return
	}
	logf("recorder did not exit within %s, sending SIGTERM", timeout)
	if err := signal(syscall.SIGTERM); err != nil {
		logf("SIGTERM failed: %v", err)
	}
	if wait(termGrace) {
		return
	}
	logf("recorder did not exit after SIGTERM, sending SIGKILL")
	if err := signal(syscall.SIGKILL); err != nil {
		logf("SIGKILL failed: %v", err)
	}
	<-exited
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
		wait, signal, err := start(ctx, s.Bin, args)
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
			waitCh := make(chan error, 1)
			go func() { waitCh <- wait() }()
			var werr error
			select {
			case werr = <-waitCh:
			case <-ctx.Done():
				exitedCh := make(chan error, 1)
				go func() { exitedCh <- <-waitCh }()
				s.stop(exitedCh, signal, logf)
				return nil
			}
			exited := now()
			downSince = exited
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
