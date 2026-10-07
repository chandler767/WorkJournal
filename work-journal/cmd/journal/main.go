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
	"syscall"
	"time"

	"workjournal/internal/config"
	"workjournal/internal/media"
	"workjournal/internal/retention"
)

const usage = `usage: journal <command> [flags]

commands:
  serve                       run the recorder, scheduler and dashboard
  setup                       create the data dir and config, build the recorder,
                              download the whisper model, install the skills
  doctor                      check prerequisites and permissions
  process [--day D --hour H]  process pending hours, or reprocess one hour
  close-day [--day D]         write the day's journal entry (default today)
  rollup [--week W]           write the weekly rollup (default this week)

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
	if err := fset.Parse(rest); err != nil {
		return 2
	}
	if !allowed(cmd, fset) {
		fmt.Fprintf(stderr, "journal %s: unsupported flag\n%s", cmd, usage)
		return 2
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

	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals...)
	defer stop()

	switch cmd {
	case "setup":
		err = runSetup(ctx, *cfgPath, cfg, loc, defaultSetupDeps(stdout, stderr), stdout)
	case "doctor":
		if !printChecks(stdout, RunChecks(defaultDoctorDeps(cfg, loc))) {
			return 1
		}
		return 0
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
	extra := map[string][]string{"process": {"day", "hour"}, "close-day": {"day"}, "rollup": {"week"}}[cmd]
	ok := true
	fset.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "config", "repo":
			return
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
		Cfg:            cfg,
		RecorderApp:    loc.RecorderApp(),
		SkillsDir:      loc.SkillsDir,
		LookPath:       exec.LookPath,
		Stat:           os.Stat,
		Glob:           filepath.Glob,
		FreeGB:         func(p string) (float64, error) { return retention.FreeGB(nearestExisting(p)) },
		SelfTest:       func(app string) error { return recorderSelfTest(app, execOpen) },
		ClaudeResponds: func() error { return claudeResponds(media.ExecRunner) },
	}
}
