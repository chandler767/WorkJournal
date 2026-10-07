package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
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
	go func() { errc <- serveOn(ctx, a, ln) }()

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
