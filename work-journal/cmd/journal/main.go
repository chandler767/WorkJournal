// Command journal runs the work journal: the recorder supervisor, the
// processing scheduler and the local dashboard (serve), plus setup, doctor
// and one-shot jobs.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"workjournal/internal/config"
	"workjournal/internal/process"
	"workjournal/internal/retention"
)

const usage = `usage: journal <command> [flags]

commands:
  serve                       run the recorder, scheduler and dashboard
  setup [--rebuild]           create the data dir and config, build the recorder
                              (only if missing; rebuilding resets its permissions),
                              download the whisper model, install the skills
  doctor                      check prerequisites and permissions
  process [--day D --hour H]  process pending hours, or reprocess one hour
  close-day [--day D]         write the day's journal entry (default today)
  rollup [--week W]           write the weekly rollup (default this week)
  import FILE --start TIME [--title T] [--context C]
                              import a recording made away from the Mac (a phone
                              memo) as an in-person meeting starting at TIME
                              (RFC 3339, e.g. 2026-10-21T18:30:00-04:00), and
                              process it now
  autostart on|off|status     run serve at login (on also starts it now; off stops it)
  mcp                         serve the journal read-only to Claude clients (MCP over stdio)

common flags:
  --config PATH   config file (default ~/WorkJournal/config.toml)
  --repo DIR      repo root holding .claude/skills (default: found from the binary)
`

// shutdownSignals end serve cleanly; SIGHUP covers a closed terminal, which
// would otherwise kill journal and orphan the recorder.
var shutdownSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	fset := flag.NewFlagSet("journal "+cmd, flag.ContinueOnError)
	fset.SetOutput(stderr)
	home, _ := os.UserHomeDir()
	cfgPath := fset.String("config", filepath.Join(home, "WorkJournal", "config.toml"), "config file")
	repo := fset.String("repo", "", "repo root holding .claude/skills")
	day := fset.String("day", "", "day YYYY-MM-DD")
	hour := fset.Int("hour", -1, "hour 0-23")
	week := fset.String("week", "", "ISO week YYYY-Www")
	rebuild := fset.Bool("rebuild", false, "setup: rebuild the recorder even if it is built")
	start := fset.String("start", "", "import: when the recording started (RFC 3339)")
	title := fset.String("title", "", "import: the meeting's title")
	note := fset.String("context", "", "import: your note about the recording")
	if err := fset.Parse(interspersed(rest)); err != nil {
		return 2
	}
	if !allowed(cmd, fset) {
		fmt.Fprintf(stderr, "journal %s: unsupported flag\n%s", cmd, usage)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals...)
	defer stop()

	if cmd == "mcp" {
		// Needs no source tree: Claude clients start it from anywhere.
		if err := runMCP(ctx, *cfgPath, &mcp.StdioTransport{}); err != nil {
			fmt.Fprintf(stderr, "journal mcp: %v\n", err)
			return 1
		}
		return 0
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "journal: %v\n", err)
		return 1
	}
	loc, err := resolveLocations(*repo, defaultStarts()...)
	if err != nil {
		fmt.Fprintf(stderr, "journal: %v\n", err)
		return 1
	}

	switch cmd {
	case "setup":
		err = runSetup(ctx, *cfgPath, cfg, loc, *rebuild, defaultSetupDeps(stdout, stderr), stdout)
	case "doctor":
		if !printChecks(stdout, RunChecks(defaultDoctorDeps(cfg, loc))) {
			return 1
		}
		return 0
	case "autostart":
		if fset.NArg() != 1 {
			fmt.Fprintf(stderr, "journal autostart: want on, off or status\n%s", usage)
			return 2
		}
		var d autostartDeps
		if d, err = defaultAutostartDeps(*cfgPath, cfg); err == nil {
			err = runAutostart(fset.Arg(0), d, stdout)
		}
	case "import":
		if fset.NArg() != 1 || *start == "" {
			fmt.Fprintf(stderr, "journal import: want one FILE and --start\n%s", usage)
			return 2
		}
		var t time.Time
		if t, err = time.Parse(time.RFC3339, *start); err != nil {
			fmt.Fprintf(stderr, "journal import: --start must be RFC 3339 (e.g. 2026-10-21T18:30:00-04:00), got %q\n", *start)
			return 2
		}
		err = runImport(ctx, cfg, *cfgPath, loc, process.ImportRequest{Path: fset.Arg(0), Start: t, Title: *title, Context: *note}, stdout)
	case "serve", "process", "close-day", "rollup":
		err = runApp(ctx, cmd, cfg, *cfgPath, loc, *day, *hour, *week)
	default:
		fmt.Fprintf(stderr, "journal: unknown command %q\n%s", cmd, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "journal %s: %v\n", cmd, err)
		return 1
	}
	return 0
}

// allowed reports whether every flag set on the command line applies to cmd.
func allowed(cmd string, fset *flag.FlagSet) bool {
	extra := map[string][]string{"setup": {"rebuild"}, "process": {"day", "hour"}, "close-day": {"day"}, "rollup": {"week"},
		"import": {"start", "title", "context"}}[cmd]
	ok := true
	fset.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "config":
			return
		case "repo":
			if cmd != "mcp" {
				return
			}
		}
		for _, e := range extra {
			if f.Name == e {
				return
			}
		}
		ok = false
	})
	return ok
}

// interspersed moves the arguments that are not flags (or flag values) to
// the end, so `journal import FILE --start T` parses like
// `journal import --start T FILE`. Every flag but --rebuild takes a value.
func interspersed(args []string) []string {
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return append(append(flags, args[i:]...), rest...)
		case !strings.HasPrefix(a, "-") || a == "-":
			rest = append(rest, a)
		default:
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if !strings.Contains(name, "=") && name != "rebuild" && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		}
	}
	return append(flags, rest...)
}

// runImport stores the recording as an imported meeting and processes it
// (process.Pipeline.ProcessImports). A processing failure is reported but
// the import stays: serve tries it again, and its meeting shows the error.
func runImport(ctx context.Context, cfg config.Config, cfgPath string, loc Locations, req process.ImportRequest, stdout io.Writer) error {
	a, err := Build(cfg, Options{ConfigPath: cfgPath, Loc: loc})
	if err != nil {
		return err
	}
	defer a.Close()
	m, err := process.Import(a.Paths, a.Store, req, time.Now())
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "imported %s as meeting %s; processing it now\n", req.Path, m.ID)
	if err := a.Jobs.ProcessImports(ctx); err != nil {
		return fmt.Errorf("processing failed (the recording is kept and serve tries again): %w", err)
	}
	rec, _, err := a.Store.Import(m.ID)
	if err != nil {
		return err
	}
	if rec.Error != "" {
		return fmt.Errorf("%s: %s", rec.Status, rec.Error)
	}
	fmt.Fprintf(stdout, "done: http://%s/meeting/%s\n", cfg.ListenAddr, m.ID)
	return nil
}

func runApp(ctx context.Context, cmd string, cfg config.Config, cfgPath string, loc Locations, day string, hour int, week string) error {
	a, err := Build(cfg, Options{ConfigPath: cfgPath, Loc: loc})
	if err != nil {
		return err
	}
	defer a.Close()
	now := time.Now()
	switch cmd {
	case "serve":
		return runServe(ctx, a)
	case "process":
		if day == "" && hour < 0 {
			return a.Jobs.ProcessPending(ctx)
		}
		prev := now.Add(-time.Hour)
		if day == "" {
			day = config.DayKey(prev)
		}
		if hour < 0 {
			hour = prev.Hour()
		}
		if hour > 23 {
			return fmt.Errorf("--hour must be 0-23, got %d", hour)
		}
		if _, err := time.Parse("2006-01-02", day); err != nil {
			return fmt.Errorf("--day must be YYYY-MM-DD, got %q", day)
		}
		return a.Jobs.ReprocessHour(ctx, day, hour)
	case "close-day":
		if day == "" {
			day = config.DayKey(now)
		}
		if _, err := time.Parse("2006-01-02", day); err != nil {
			return fmt.Errorf("--day must be YYYY-MM-DD, got %q", day)
		}
		return a.Jobs.CloseDay(ctx, day)
	case "rollup":
		if week == "" {
			week = config.WeekKey(now)
		}
		return a.Jobs.Rollup(ctx, week)
	}
	return fmt.Errorf("unknown command %q", cmd)
}

func defaultDoctorDeps(cfg config.Config, loc Locations) DoctorDeps {
	return DoctorDeps{
		Cfg:               cfg,
		RecorderApp:       loc.RecorderApp(),
		SkillsDir:         loc.SkillsDir,
		LookPath:          exec.LookPath,
		Stat:              os.Stat,
		Glob:              filepath.Glob,
		FreeGB:            func(p string) (float64, error) { return retention.FreeGB(nearestExisting(p)) },
		SelfTest:          func(app string) error { return recorderSelfTest(app, execOpen) },
		ClaudeResponds:    func() error { return claudeResponds(execKeepingStdout) },
		SpeakerSeparation: speakerSeparationWorks,
	}
}
