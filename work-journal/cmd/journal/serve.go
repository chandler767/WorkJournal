package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// checkLoopback refuses any listen address whose host is not loopback: the
// dashboard serves raw recordings and must never be reachable off-machine.
func checkLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("listen_addr %q: %w", addr, err)
	}
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return nil
	}
	return fmt.Errorf("listen_addr %q is not a loopback address (use 127.0.0.1, localhost or ::1)", addr)
}

// runServe checks the listen address, binds it and serves until ctx ends.
func runServe(ctx context.Context, a *App) error {
	addr := a.Cfg().ListenAddr
	if err := checkLoopback(addr); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return serveOn(ctx, a, ln)
}

// serveOn runs the recorder supervisor, the scheduler and the dashboard on
// ln until ctx ends or the server fails, then shuts them down in order.
func serveOn(ctx context.Context, a *App, ln net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for _, w := range startupWarnings(a) {
		log.Print(w)
	}
	srv := &http.Server{Handler: a.Server.Handler(), ReadHeaderTimeout: 10 * time.Second}

	var wg sync.WaitGroup
	wg.Go(func() {
		if err := a.Supervisor.Run(ctx); err != nil {
			log.Printf("supervisor: %v", err)
		}
	})
	wg.Go(func() { a.Scheduler.Run(ctx, time.Minute) })
	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.Serve(ln) }()
	log.Printf("journal: dashboard on http://%s", ln.Addr())

	var err error
	select {
	case <-ctx.Done():
	case err = <-srvErr:
		err = fmt.Errorf("http server: %w", err)
	}
	log.Printf("journal: shutting down")
	_ = a.Control.Send("quit") // the recorder finishes its files and exits
	cancel()
	shutCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	if serr := srv.Shutdown(shutCtx); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
		log.Printf("http shutdown: %v", serr)
	}
	wg.Wait()
	return err
}

// startupWarnings lists conditions under which serve runs but cannot do
// its job; each is logged loudly at startup.
func startupWarnings(a *App) []string {
	var w []string
	if a.Loc.SkillsDir == "" {
		w = append(w, "WARNING: no installed Claude Code skills found (.claude/skills/tessl__summarize-hour); "+
			"hours will be recorded but not summarized. Run `journal setup` or pass --repo <repo root>.")
	}
	return w
}
