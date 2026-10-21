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

	"workjournal/internal/process"
	"workjournal/internal/store"
	"workjournal/internal/upload"
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

// runServe checks the listen addresses, binds them and serves until ctx
// ends. The phone upload listener is opened only when upload_addr is set,
// and then only with a usable upload_token.
func runServe(ctx context.Context, a *App) error {
	cfg := a.Cfg()
	if err := checkLoopback(cfg.ListenAddr); err != nil {
		return err
	}
	if err := cfg.CheckUpload(); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return err
	}
	var up net.Listener
	if cfg.UploadAddr != "" {
		if up, err = net.Listen("tcp", cfg.UploadAddr); err != nil {
			ln.Close()
			return fmt.Errorf("upload_addr: %w", err)
		}
	}
	return serveOn(ctx, a, ln, up)
}

// serveOn runs the recorder supervisor, the scheduler and the dashboard on
// ln (and the phone upload page on up, when not nil) until ctx ends or a
// server fails, then shuts them down in order.
func serveOn(ctx context.Context, a *App, ln, up net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for _, w := range startupWarnings(a) {
		log.Print(w)
	}
	srv := &http.Server{Handler: a.Server.Handler(), ReadHeaderTimeout: 10 * time.Second}
	var upSrv *http.Server
	if up != nil {
		// No ReadTimeout: a 1 GB upload over Wi-Fi takes minutes.
		upSrv = &http.Server{Handler: a.uploadServer().Handler(), ReadHeaderTimeout: 10 * time.Second}
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		if err := a.Supervisor.Run(ctx); err != nil {
			log.Printf("supervisor: %v", err)
		}
	})
	wg.Go(func() { a.Scheduler.Run(ctx, time.Minute) })
	srvErr := make(chan error, 2)
	go func() { srvErr <- srv.Serve(ln) }()
	log.Printf("journal: dashboard on http://%s", ln.Addr())
	if upSrv != nil {
		go func() {
			if err := upSrv.Serve(up); err != nil {
				srvErr <- fmt.Errorf("upload: %w", err)
			}
		}()
		log.Printf("journal: phone upload page on %s (open it on a phone on the same network)", uploadURL(up.Addr(), lanAddrs()))
	}

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
	if upSrv != nil {
		if serr := upSrv.Shutdown(shutCtx); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
			log.Printf("upload shutdown: %v", serr)
		}
	}
	a.Server.CloseAsks(5 * time.Second) // a running Ask stops its claude
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

// uploadServer is the phone upload page over the app's data.
func (a *App) uploadServer() *upload.Server {
	return &upload.Server{
		Token: a.Cfg().UploadToken,
		Paths: a.Paths,
		Store: a.Store,
		Import: func(req process.ImportRequest) (store.MeetingRecord, error) {
			return process.Import(a.Paths, a.Store, req, time.Now())
		},
	}
}

// uploadURL is the address to open on the phone: the listener's own host,
// or for a wildcard listener (0.0.0.0, ::) the Mac's first LAN address.
func uploadURL(addr net.Addr, lan []net.IP) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "http://" + addr.String()
	}
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		host = "<this Mac's IP address>"
		if len(lan) > 0 {
			host = lan[0].String()
		}
	}
	return "http://" + net.JoinHostPort(host, port)
}

// lanAddrs returns the Mac's private IPv4 addresses on interfaces that are
// up, the ones a phone on the same network reaches.
func lanAddrs() []net.IP {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []net.IP
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && n.IP.IsPrivate() {
				out = append(out, n.IP)
			}
		}
	}
	return out
}
