package capture

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"workjournal/internal/config"
)

func hasPair(args []string, flag, val string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == val {
			return true
		}
	}
	return false
}

func TestRecorderArgsIncludesExclusions(t *testing.T) {
	c := config.Default()
	p := config.Paths{Root: "/data"}
	args := RecorderArgs(c, p)
	if args[0] != "run" {
		t.Fatalf("args[0] = %q", args[0])
	}
	for _, kv := range [][2]string{
		{"--exclude-app", "1Password"}, {"--exclude-app", "Keychain Access"},
		{"--exclude-title", "Incognito"}, {"--meeting-app", "zoom.us"},
		{"--meeting-title", "Huddle"}, {"--meeting-interval", "5"},
		{"--data-dir", "/data"}, {"--socket", "/data/recorder.sock"},
		{"--pause-auto-resume", "60"}, {"--dashboard-url", "http://127.0.0.1:4747"},
	} {
		if !hasPair(args, kv[0], kv[1]) {
			t.Errorf("missing %s %s in %v", kv[0], kv[1], args)
		}
	}
	if slices.Contains(args, "--all-displays") {
		t.Error("--all-displays present when false")
	}
	c.AllDisplays = true
	if !slices.Contains(RecorderArgs(c, p), "--all-displays") {
		t.Error("--all-displays missing when true")
	}
}

type fakeEnv struct {
	now    time.Time
	sleeps []time.Duration
	starts [][]string
	run    func(n int) (time.Duration, error) // uptime and exit error of nth run (0-based)
	onDone func(n int)
}

func (f *fakeEnv) sup() *Supervisor {
	return &Supervisor{
		Bin:  "rec",
		Args: []string{"run"},
		Now:  func() time.Time { return f.now },
		Sleep: func(d time.Duration) {
			f.sleeps = append(f.sleeps, d)
			f.now = f.now.Add(d)
		},
		Start: func(ctx context.Context, bin string, args []string) (func() error, error) {
			n := len(f.starts)
			f.starts = append(f.starts, args)
			return func() error {
				up, err := f.run(n)
				f.now = f.now.Add(up)
				if f.onDone != nil {
					f.onDone(n)
				}
				return err
			}, nil
		},
	}
}

func TestSupervisorRestartsWithBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeEnv{now: time.Unix(1000, 0), run: func(int) (time.Duration, error) { return 0, nil }}
	f.onDone = func(n int) {
		if n == 3 {
			cancel()
		}
	}
	if err := f.sup().Run(ctx); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if !slices.Equal(f.sleeps, want) {
		t.Fatalf("sleeps = %v, want %v", f.sleeps, want)
	}
}

func TestSupervisorBackoffCapsAndResets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeEnv{now: time.Unix(1000, 0)}
	f.run = func(n int) (time.Duration, error) {
		if n == 9 {
			return 6 * time.Minute, nil
		}
		return 0, nil
	}
	f.onDone = func(n int) {
		if n == 10 {
			cancel()
		}
	}
	f.sup().Run(ctx)
	if f.sleeps[8] != 60*time.Second { // 1,2,4,8,16,32,60,60,60
		t.Errorf("sleeps = %v, want cap at 60s", f.sleeps)
	}
	if f.sleeps[9] != time.Second {
		t.Errorf("sleep after healthy run = %v, want 1s", f.sleeps[9])
	}
}

func TestSupervisorReportsGap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	base := time.Unix(1000, 0)
	f := &fakeEnv{now: base, run: func(int) (time.Duration, error) { return 10 * time.Second, nil }}
	f.onDone = func(n int) {
		if n == 1 {
			cancel()
		}
	}
	s := f.sup()
	type gap struct{ from, to time.Time }
	var gaps []gap
	s.OnGap = func(from, to time.Time) { gaps = append(gaps, gap{from, to}) }
	s.Run(ctx)
	if len(gaps) != 1 {
		t.Fatalf("gaps = %v", gaps)
	}
	if !gaps[0].from.Equal(base.Add(10*time.Second)) || !gaps[0].to.Equal(base.Add(11*time.Second)) {
		t.Errorf("gap = %+v", gaps[0])
	}
}

func TestSupervisorAddsDegradedAfterThreeFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeEnv{now: time.Unix(1000, 0), run: func(n int) (time.Duration, error) {
		if n == 6 {
			return 6 * time.Minute, nil
		}
		return time.Second, nil
	}}
	f.onDone = func(n int) {
		if n == 7 {
			cancel()
		}
	}
	s := f.sup()
	var logs []string
	s.Logf = func(format string, a ...any) { logs = append(logs, format) }
	s.Run(ctx)
	count := func(args []string) int {
		c := 0
		for _, a := range args {
			if a == "--degraded" {
				c++
			}
		}
		return c
	}
	for i, want := range []int{0, 0, 0, 1, 1, 1, 1, 0} {
		if got := count(f.starts[i]); got != want {
			t.Errorf("start %d: --degraded count %d, want %d (%v)", i, got, want, f.starts[i])
		}
	}
	found := false
	for _, l := range logs {
		found = found || strings.Contains(l, "recorder failing")
	}
	if !found {
		t.Error("no 'recorder failing' log")
	}
}

func TestSupervisorStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	started := make(chan struct{})
	s := &Supervisor{
		Bin: "rec",
		Start: func(context.Context, string, []string) (func() error, error) {
			close(started)
			return func() error { <-release; return nil }, nil
		},
	}
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	<-started
	cancel()
	select {
	case <-done:
		t.Fatal("Run returned before process exited")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after process exit")
	}
}

func TestSupervisorDefaultSleepReturnsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Supervisor{
		Start: func(context.Context, string, []string) (func() error, error) {
			return func() error { return nil }, nil
		},
	}
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	time.Sleep(50 * time.Millisecond) // now in the 1s back-off sleep
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Run did not return promptly on cancel")
	}
}

func TestSocketControlRoundTrip(t *testing.T) {
	dir, err := os.MkdirTemp("", "wj")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "r.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 8)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 64)
			n, _ := c.Read(buf)
			cmd := strings.TrimSpace(string(buf[:n]))
			got <- cmd
			switch cmd {
			case "status":
				c.Write([]byte(`{"state":"paused","since":"2026-10-07T09:30:00Z","meeting":true,"screens":false,"audio":true}` + "\n"))
			case "bogus":
				c.Write([]byte("error: unknown\n"))
			default:
				c.Write([]byte("ok\n"))
			}
			c.Close()
		}
	}()
	ctl := NewSocketControl(sock)
	st, err := ctl.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.State != "paused" || !st.Meeting || st.Screens || !st.Audio ||
		!st.Since.Equal(time.Date(2026, 10, 7, 9, 30, 0, 0, time.UTC)) {
		t.Errorf("status = %+v", st)
	}
	if err := ctl.Pause(); err != nil {
		t.Error(err)
	}
	if err := ctl.Resume(); err != nil {
		t.Error(err)
	}
	if err := ctl.Send("screens_off"); err != nil {
		t.Error(err)
	}
	err = ctl.Send("bogus")
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Errorf("bogus err = %v", err)
	}
	var cmds []string
	for range 5 {
		cmds = append(cmds, <-got)
	}
	if !slices.Equal(cmds, []string{"status", "pause", "resume", "screens_off", "bogus"}) {
		t.Errorf("cmds = %v", cmds)
	}
	if err := NewSocketControl(filepath.Join(dir, "none.sock")).Pause(); err == nil {
		t.Error("expected dial error")
	}
}
