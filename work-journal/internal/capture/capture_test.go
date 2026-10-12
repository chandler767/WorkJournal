package capture

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
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

func TestRecorderArgsScreenCapture(t *testing.T) {
	c := config.Default()
	p := config.Paths{Root: "/data"}
	args := RecorderArgs(c, p)
	for _, kv := range [][2]string{
		{"--min-gap", "3"}, {"--idle-backstop", "60"}, {"--change-sensitivity", "medium"},
		{"--capture-on-switch", "true"}, {"--meeting-interval", "5"},
	} {
		if !hasPair(args, kv[0], kv[1]) {
			t.Errorf("missing %s %s in %v", kv[0], kv[1], args)
		}
	}
	if slices.Contains(args, "--interval") {
		t.Errorf("--interval passed alongside --idle-backstop: %v", args)
	}
	c.ScreenshotMinGapSec, c.ScreenshotIdleBackstopSec = 5, 120
	c.ScreenChangeSensitivity, c.CaptureOnAppSwitch = "high", false
	args = RecorderArgs(c, p)
	for _, kv := range [][2]string{
		{"--min-gap", "5"}, {"--idle-backstop", "120"}, {"--change-sensitivity", "high"}, {"--capture-on-switch", "false"},
	} {
		if !hasPair(args, kv[0], kv[1]) {
			t.Errorf("missing %s %s in %v", kv[0], kv[1], args)
		}
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
		Start: func(ctx context.Context, bin string, args []string) (func() error, func(os.Signal) error, error) {
			n := len(f.starts)
			f.starts = append(f.starts, args)
			return func() error {
				up, err := f.run(n)
				f.now = f.now.Add(up)
				if f.onDone != nil {
					f.onDone(n)
				}
				return err
			}, nil, nil
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
		Start: func(context.Context, string, []string) (func() error, func(os.Signal) error, error) {
			close(started)
			return func() error { <-release; return nil }, nil, nil
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
		Start: func(context.Context, string, []string) (func() error, func(os.Signal) error, error) {
			return func() error { return nil }, nil, nil
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

func TestSupervisorEscalatesToTermThenKill(t *testing.T) {
	defer func(old time.Duration) { termGrace = old }(termGrace)
	termGrace = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	dead := make(chan struct{})
	var mu sync.Mutex
	var sigs []os.Signal
	s := &Supervisor{
		Bin:         "rec",
		StopTimeout: 50 * time.Millisecond,
		Start: func(context.Context, string, []string) (func() error, func(os.Signal) error, error) {
			close(started)
			return func() error { <-dead; return nil },
				func(sig os.Signal) error {
					mu.Lock()
					defer mu.Unlock()
					sigs = append(sigs, sig)
					if sig == syscall.SIGKILL {
						close(dead)
					}
					return nil
				}, nil
		},
	}
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	<-started
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sigs) != 2 || sigs[0] != syscall.SIGTERM || sigs[1] != syscall.SIGKILL {
		t.Fatalf("signals = %v, want [terminated killed]", sigs)
	}
}

func TestSupervisorStopsGracefullyWithinTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	exit := make(chan struct{})
	signalled := false
	s := &Supervisor{
		Bin:         "rec",
		StopTimeout: 2 * time.Second,
		Start: func(context.Context, string, []string) (func() error, func(os.Signal) error, error) {
			close(started)
			return func() error { <-exit; return nil },
				func(os.Signal) error { signalled = true; return nil }, nil
		},
	}
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	<-started
	cancel()
	time.AfterFunc(20*time.Millisecond, func() { close(exit) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	if signalled {
		t.Error("signal sent although process exited within timeout")
	}
}

// fakeRecorder serves the control socket; on quit it replies ok and then,
// unless stubborn, removes the socket after a short delay like the recorder.
func fakeRecorder(t *testing.T, sock string, stubborn bool) (cmds func() []string) {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []string
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 64)
			n, _ := c.Read(buf)
			cmd := strings.TrimSpace(string(buf[:n]))
			mu.Lock()
			got = append(got, cmd)
			mu.Unlock()
			if cmd == "status" {
				c.Write([]byte(`{"state":"recording","since":"2026-10-07T09:30:00Z"}` + "\n"))
			} else {
				c.Write([]byte("ok\n"))
			}
			c.Close()
			if cmd == "quit" && !stubborn {
				time.Sleep(100 * time.Millisecond)
				ln.Close() // removes the socket file
				return
			}
		}
	}()
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

func shortSockDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "wj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// I4: an orphaned recorder still holding the socket is told to quit before
// a new one is launched.
func TestQuitRunningRecorder(t *testing.T) {
	sock := filepath.Join(shortSockDir(t), "r.sock")
	cmds := fakeRecorder(t, sock, false)
	found, err := QuitRunning(sock, time.Second, 5*time.Second)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if got := cmds(); !slices.Equal(got, []string{"status", "quit"}) {
		t.Fatalf("cmds = %v", got)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket still there: %v", err)
	}
}

func TestQuitRunningNothingListening(t *testing.T) {
	dir := shortSockDir(t)
	if found, err := QuitRunning(filepath.Join(dir, "none.sock"), time.Second, time.Second); found || err != nil {
		t.Fatalf("no socket: found=%v err=%v", found, err)
	}
	// A stale socket file left by a crashed recorder: nobody answers.
	stale := filepath.Join(dir, "stale.sock")
	ln, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	began := time.Now()
	if found, err := QuitRunning(stale, time.Second, time.Second); found || err != nil {
		t.Fatalf("stale socket: found=%v err=%v", found, err)
	}
	if time.Since(began) > 900*time.Millisecond {
		t.Fatalf("stale probe took %s", time.Since(began))
	}
}

func TestQuitRunningRecorderThatStays(t *testing.T) {
	sock := filepath.Join(shortSockDir(t), "r.sock")
	fakeRecorder(t, sock, true)
	found, err := QuitRunning(sock, time.Second, 200*time.Millisecond)
	if !found || err == nil {
		t.Fatalf("found=%v err=%v, want a timeout error", found, err)
	}
}

// Rotate waits for the recorder to finish writing the chunks, which can take
// longer than the normal command timeout.
func TestSocketControlRotateWaitsForSlowReply(t *testing.T) {
	defer func(old time.Duration) { commandTimeout = old }(commandTimeout)
	commandTimeout = 100 * time.Millisecond
	sock := filepath.Join(shortSockDir(t), "r.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 4)
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
			time.Sleep(300 * time.Millisecond) // finishing the chunks
			if cmd == "rotate" {
				c.Write([]byte("ok\n"))
			} else {
				c.Write([]byte("error: unknown command\n"))
			}
			c.Close()
		}
	}()
	ctl := NewSocketControl(sock)
	if err := ctl.Rotate(); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if cmd := <-got; cmd != "rotate" {
		t.Errorf("sent %q, want rotate", cmd)
	}
	if err := ctl.Send("pause"); err == nil {
		t.Error("slow reply to an ordinary command should time out")
	}
}

func TestSocketControlRotateReportsError(t *testing.T) {
	sock := filepath.Join(shortSockDir(t), "r.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		buf := make([]byte, 64)
		c.Read(buf)
		c.Write([]byte("error: unknown command \"rotate\"\n"))
		c.Close()
	}()
	err = NewSocketControl(sock).Rotate()
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("Rotate err = %v, want the recorder's error", err)
	}
}

func TestRecorderArgsAudioMode(t *testing.T) {
	c := config.Default()
	p := config.Paths{Root: "/data"}
	if args := RecorderArgs(c, p); !hasPair(args, "--audio-mode", "on") {
		t.Errorf("default: missing --audio-mode on in %v", args)
	}
	c.AudioMode = "auto"
	if args := RecorderArgs(c, p); !hasPair(args, "--audio-mode", "auto") {
		t.Errorf("missing --audio-mode auto in %v", args)
	}
}

// audioRecorder serves "status" with the given JSON and answers "ok" to
// everything else, recording the commands.
func audioRecorder(t *testing.T, status string) (sock string, cmds func() []string) {
	t.Helper()
	sock = filepath.Join(shortSockDir(t), "r.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var got []string
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 64)
			n, _ := c.Read(buf)
			cmd := strings.TrimSpace(string(buf[:n]))
			mu.Lock()
			got = append(got, cmd)
			mu.Unlock()
			if cmd == "status" {
				c.Write([]byte(status + "\n"))
			} else {
				c.Write([]byte("ok\n"))
			}
			c.Close()
		}
	}()
	return sock, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

func TestSocketControlAudioMode(t *testing.T) {
	sock, cmds := audioRecorder(t, `{"state":"recording","since":"2026-10-07T09:30:00Z","audio_mode":"auto","audio_active":true,"audio_reason":"call"}`)
	ctl := NewSocketControl(sock)
	st, err := ctl.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.AudioMode != "auto" || !st.AudioActive || st.AudioReason != "call" {
		t.Errorf("status = %+v", st)
	}
	if err := ctl.SetAudioMode("off"); err != nil {
		t.Fatal(err)
	}
	if err := ctl.SetAudioMode("loud"); err == nil {
		t.Error("invalid mode accepted")
	}
	if got := cmds(); !slices.Equal(got, []string{"status", "audio_mode off"}) {
		t.Errorf("cmds = %v (an invalid mode must not reach the recorder)", got)
	}
}

type modeControl struct {
	Control
	st  Status
	err error
}

func (m modeControl) Status() (Status, error) { return m.st, m.err }

func TestSyncAudioModePersistsRecorderChange(t *testing.T) {
	cfg := config.Default()
	var saved []config.Config
	save := func(c config.Config) error { saved = append(saved, c); cfg = c; return nil }
	get := func() config.Config { return cfg }

	// Menu-bar change: the recorder reports auto while the config says on.
	changed, err := SyncAudioMode(modeControl{st: Status{AudioMode: "auto"}}, get, save)
	if err != nil || !changed || len(saved) != 1 || saved[0].AudioMode != "auto" {
		t.Fatalf("changed %v err %v saved %+v", changed, err, saved)
	}
	// Same mode: nothing to save.
	if changed, err := SyncAudioMode(modeControl{st: Status{AudioMode: "auto"}}, get, save); err != nil || changed || len(saved) != 1 {
		t.Fatalf("same mode: changed %v err %v saves %d", changed, err, len(saved))
	}
	// Recorder down, or an older recorder that reports no mode, or nonsense: no change, no error.
	for _, ctl := range []Control{
		modeControl{err: os.ErrNotExist},
		modeControl{st: Status{}},
		modeControl{st: Status{AudioMode: "loud"}},
	} {
		if changed, err := SyncAudioMode(ctl, get, save); err != nil || changed || len(saved) != 1 {
			t.Fatalf("%+v: changed %v err %v saves %d", ctl, changed, err, len(saved))
		}
	}
}
