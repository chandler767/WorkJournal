package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"workjournal/internal/config"
	"workjournal/internal/media"
)

func TestCheckLoopback(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:4747", "localhost:80", "[::1]:4747"} {
		if err := checkLoopback(addr); err != nil {
			t.Errorf("%s refused: %v", addr, err)
		}
	}
	for _, addr := range []string{"0.0.0.0:4747", ":4747", "192.168.1.5:4747", "example.com:80", "[::]:4747", "nonsense"} {
		if err := checkLoopback(addr); err == nil {
			t.Errorf("%s accepted", addr)
		}
	}
}

func TestServeRefusesNonLoopback(t *testing.T) {
	a := newTestApp(t)
	cfg := a.Cfg()
	cfg.ListenAddr = "0.0.0.0:0"
	a.cfg.set(cfg)
	started := false
	a.Supervisor.Start = func(context.Context, string, []string) (func() error, func(os.Signal) error, error) {
		started = true
		return nil, nil, errors.New("unused")
	}
	// A regression would serve until ctx ends; bound it so the test fails, not hangs.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := runServe(ctx, a)
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("err = %v", err)
	}
	if started {
		t.Fatal("recorder started despite refusal")
	}
}

// TestServeSmoke runs the whole serve loop against fakes: the recorder start
// and control socket are fake and claude is never run.
func TestServeSmoke(t *testing.T) {
	a := newTestApp(t)
	ctl := &fakeControl{}
	a.Control, a.Jobs.Control, a.Server.Control = ctl, ctl, ctl
	a.Runner.Exec = media.Runner(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("claude disabled in tests")
	})
	launched := make(chan []string, 1)
	a.Supervisor.Start = func(ctx context.Context, bin string, args []string) (func() error, func(os.Signal) error, error) {
		launched <- append([]string{bin}, args...)
		done := make(chan struct{})
		var once sync.Once
		stop := func(os.Signal) error { once.Do(func() { close(done) }); return nil }
		return func() error { <-done; return nil }, stop, nil
	}
	a.Supervisor.StopTimeout = 10 * time.Millisecond
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- serveOn(ctx, a, ln, nil) }()

	select {
	case argv := <-launched:
		if argv[0] != "/usr/bin/open" || !slices.Contains(argv, a.Loc.RecorderApp()) {
			t.Fatalf("launch argv = %v", argv)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recorder never launched")
	}
	req, _ := http.NewRequest("GET", "http://"+ln.Addr().String()+"/day/"+config.DayKey(time.Now()), nil)
	req.Host = "127.0.0.1"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /day = %d: %s", resp.StatusCode, body)
	}

	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("serve returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop")
	}
	if !slices.Contains(ctl.sentCmds(), "quit") {
		t.Fatalf("recorder not told to quit: %v", ctl.sent)
	}
}

// I3: without installed skills serve says so loudly and the dashboard
// shows a banner.
func TestServeWarnsWhenSkillsMissing(t *testing.T) {
	repo, mod := fakeRepo(t)
	cfg := config.Default()
	cfg.DataDir = filepath.Join(t.TempDir(), "data")
	a, err := Build(cfg, Options{ConfigPath: filepath.Join(cfg.DataDir, "config.toml"), Loc: Locations{ModuleRoot: mod, RepoRoot: repo}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if !a.Server.SkillsMissing {
		t.Fatal("dashboard not told the skills are missing")
	}
	if w := startupWarnings(a); len(w) != 1 || !strings.Contains(w[0], "WARNING") || !strings.Contains(w[0], "skills") {
		t.Fatalf("warnings = %q", w)
	}
	if w := startupWarnings(newTestApp(t)); len(w) != 0 {
		t.Fatalf("warnings with skills present = %q", w)
	}
}

func TestServeRefusesUploadWithoutToken(t *testing.T) {
	for _, tok := range []string{"", "short-token-15ch"[:15]} {
		a := newTestApp(t)
		cfg := a.Cfg()
		cfg.ListenAddr, cfg.UploadAddr, cfg.UploadToken = "127.0.0.1:0", "127.0.0.1:0", tok
		a.cfg.set(cfg)
		started := false
		a.Supervisor.Start = func(context.Context, string, []string) (func() error, func(os.Signal) error, error) {
			started = true
			return nil, nil, errors.New("unused")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := runServe(ctx, a)
		cancel()
		if err == nil || !strings.Contains(err.Error(), "upload_token") {
			t.Errorf("token %q: err = %v", tok, err)
		}
		if started {
			t.Errorf("token %q: recorder started despite refusal", tok)
		}
	}
	// With upload_addr unset no token is needed.
	cfg := config.Default()
	if err := cfg.CheckUpload(); err != nil {
		t.Errorf("default config: %v", err)
	}
	cfg.UploadAddr, cfg.UploadToken = "0.0.0.0:4748", strings.Repeat("x", config.MinUploadToken)
	if err := cfg.CheckUpload(); err != nil {
		t.Errorf("16-character token: %v", err)
	}
}

// TestServeUploadListener runs serve with the upload page on a second
// listener: it answers 401 without the token, serves only the upload page
// with it, and the dashboard stays on its own listener.
func TestServeUploadListener(t *testing.T) {
	a := newTestApp(t)
	cfg := a.Cfg()
	cfg.UploadToken = "correct-horse-battery-staple"
	a.cfg.set(cfg)
	ctl := &fakeControl{}
	a.Control, a.Jobs.Control, a.Server.Control = ctl, ctl, ctl
	a.Runner.Exec = media.Runner(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("claude disabled in tests")
	})
	a.Supervisor.Start = func(ctx context.Context, bin string, args []string) (func() error, func(os.Signal) error, error) {
		done := make(chan struct{})
		var once sync.Once
		return func() error { <-done; return nil }, func(os.Signal) error { once.Do(func() { close(done) }); return nil }, nil
	}
	a.Supervisor.StopTimeout = 10 * time.Millisecond
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- serveOn(ctx, a, ln, up) }()

	get := func(addr, path string, cookie bool) int {
		t.Helper()
		req, _ := http.NewRequest("GET", "http://"+addr+path, nil)
		if cookie {
			req.AddCookie(&http.Cookie{Name: "journal_upload", Value: cfg.UploadToken})
		}
		var resp *http.Response
		for i := 0; i < 50; i++ {
			if resp, err = http.DefaultClient.Do(req); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	day := "/day/" + config.DayKey(time.Now())
	for _, p := range []string{"/", day, "/meeting/2026-10-07-0900-zoom", "/media/2026-10-07/audio/0900-mic.m4a", "/static/app.css"} {
		if code := get(up.Addr().String(), p, false); code != http.StatusUnauthorized {
			t.Errorf("upload listener %s without token: %d", p, code)
		}
	}
	if code := get(up.Addr().String(), "/", true); code != http.StatusOK {
		t.Errorf("upload page with token: %d", code)
	}
	for _, p := range []string{day, "/media/2026-10-07/audio/0900-mic.m4a", "/static/app.css", "/search"} {
		if code := get(up.Addr().String(), p, true); code != http.StatusNotFound {
			t.Errorf("upload listener %s with token: %d, want 404", p, code)
		}
	}
	if code := get(ln.Addr().String(), day, false); code != http.StatusOK {
		t.Errorf("dashboard: %d", code)
	}
	if code := get(ln.Addr().String(), "/upload", false); code == http.StatusOK {
		t.Errorf("dashboard serves the upload route")
	}

	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("serve returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop")
	}
	if c, err := net.Dial("tcp", up.Addr().String()); err == nil {
		c.Close()
		t.Error("upload listener still open after shutdown")
	}
}

func TestUploadURL(t *testing.T) {
	lan := []net.IP{net.ParseIP("192.168.1.23")}
	for _, c := range []struct {
		addr string
		lan  []net.IP
		want string
	}{
		{"0.0.0.0:4748", lan, "http://192.168.1.23:4748"},
		{"[::]:4748", lan, "http://192.168.1.23:4748"},
		{"10.0.0.5:4748", lan, "http://10.0.0.5:4748"},
		{"0.0.0.0:4748", nil, "http://<this Mac's IP address>:4748"},
	} {
		addr, err := net.ResolveTCPAddr("tcp", c.addr)
		if err != nil {
			t.Fatal(err)
		}
		if got := uploadURL(addr, c.lan); got != c.want {
			t.Errorf("uploadURL(%s) = %q, want %q", c.addr, got, c.want)
		}
	}
}
