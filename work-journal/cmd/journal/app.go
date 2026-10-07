package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"workjournal/internal/capture"
	"workjournal/internal/config"
	"workjournal/internal/diarize"
	"workjournal/internal/media"
	"workjournal/internal/process"
	"workjournal/internal/retention"
	"workjournal/internal/scheduler"
	"workjournal/internal/skills"
	"workjournal/internal/store"
	"workjournal/internal/web"
)

const moduleName = "workjournal"

// Locations are the source-tree paths the app needs at runtime.
type Locations struct {
	ModuleRoot string // directory holding go.mod for module workjournal
	RepoRoot   string // directory `tessl install` runs in
	SkillsDir  string // directory whose .claude/skills holds the installed skills; "" if not found
}

// RecorderApp is the built recorder bundle.
func (l Locations) RecorderApp() string {
	return filepath.Join(l.ModuleRoot, "recorder", "build", "WorkJournalRecorder.app")
}

// RecorderBin is the recorder executable inside the bundle, used directly for
// `recorder ocr` (Vision OCR needs no permission).
func (l Locations) RecorderBin() string {
	return filepath.Join(l.RecorderApp(), "Contents", "MacOS", "recorder")
}

// findModuleRoot walks up from each start in turn to the nearest directory
// whose go.mod declares module workjournal.
func findModuleRoot(starts ...string) (string, error) {
	for _, s := range starts {
		if s == "" {
			continue
		}
		for dir := filepath.Clean(s); ; dir = filepath.Dir(dir) {
			if isModuleRoot(dir) {
				return dir, nil
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	return "", fmt.Errorf("no go.mod for module %s above %s (run from the source tree or pass --repo)", moduleName, strings.Join(starts, ", "))
}

func isModuleRoot(dir string) bool {
	f, err := os.Open(filepath.Join(dir, "go.mod"))
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if fields := strings.Fields(sc.Text()); len(fields) == 2 && fields[0] == "module" {
			return fields[1] == moduleName
		}
	}
	return false
}

// findSkillsDir walks up from dir to the nearest directory containing
// .claude/skills/tessl__summarize-hour.
func findSkillsDir(from string) (string, bool) {
	for dir := filepath.Clean(from); ; dir = filepath.Dir(dir) {
		if fi, err := os.Stat(filepath.Join(dir, ".claude", "skills", "tessl__summarize-hour")); err == nil && fi.IsDir() {
			return dir, true
		}
		if filepath.Dir(dir) == dir {
			return "", false
		}
	}
}

// findRepoRoot is the nearest directory above the module root holding .git,
// or else the module root's parent.
func findRepoRoot(moduleRoot string) string {
	for dir := moduleRoot; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		if filepath.Dir(dir) == dir {
			return filepath.Dir(moduleRoot)
		}
	}
}

// resolveLocations finds the module root by walking up from starts (and from
// repo and repo/work-journal when repo is set). repo, when set, is both the
// skills WorkDir and the `tessl install` directory.
func resolveLocations(repo string, starts ...string) (Locations, error) {
	if repo != "" {
		abs, err := filepath.Abs(repo)
		if err != nil {
			return Locations{}, err
		}
		repo = abs
		starts = append([]string{filepath.Join(repo, "work-journal"), repo}, starts...)
	}
	mod, err := findModuleRoot(starts...)
	if err != nil {
		return Locations{}, err
	}
	loc := Locations{ModuleRoot: mod, RepoRoot: repo, SkillsDir: repo}
	if repo == "" {
		loc.RepoRoot = findRepoRoot(mod)
		loc.SkillsDir, _ = findSkillsDir(mod)
	}
	return loc, nil
}

// defaultStarts are where the module root is searched for: the executable's
// directory, then the working directory.
func defaultStarts() []string {
	var s []string
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		s = append(s, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		s = append(s, wd)
	}
	return s
}

func modelPath(p config.Paths, model string) string {
	return filepath.Join(p.Models(), "ggml-"+model+".bin")
}

// cfgHolder is the live configuration shared by the dashboard, scheduler
// and recorder launcher.
type cfgHolder struct {
	mu  sync.RWMutex
	cfg config.Config
}

func (h *cfgHolder) get() config.Config {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.cfg
}

func (h *cfgHolder) set(c config.Config) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg = c
}

// Options are the non-config inputs to Build.
type Options struct {
	ConfigPath string
	Loc        Locations
}

// App is the wired-up journal.
type App struct {
	Paths      config.Paths
	ConfigPath string
	Loc        Locations
	Store      *store.Store
	Runner     *skills.ClaudeRunner
	Summarizer *process.SkillSummarizer
	Pipeline   *process.Pipeline
	Control    capture.Control
	Voices     *diarize.Voices
	Jobs       *Jobs
	Scheduler  *scheduler.Scheduler
	Supervisor *capture.Supervisor
	Server     *web.Server
	cfg        *cfgHolder
	launch     func(bin string, args []string) (func() error, func(os.Signal) error, error)
}

// Cfg returns the live configuration.
func (a *App) Cfg() config.Config { return a.cfg.get() }

// SaveCfg writes c to the config file and makes it live. Jobs rebuild the
// pipeline and summarizer from it on every call; recorder settings apply on
// the recorder's next start.
func (a *App) SaveCfg(c config.Config) error {
	if err := config.Save(a.ConfigPath, c); err != nil {
		return err
	}
	a.cfg.set(c)
	return nil
}

// Close releases the store.
func (a *App) Close() error { return a.Store.Close() }

// Build constructs every component. It creates the data directories and
// opens the database but starts nothing.
func Build(cfg config.Config, opt Options) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	p := config.Paths{Root: cfg.DataDir}
	if err := p.Ensure(); err != nil {
		return nil, err
	}
	st, err := store.Open(p.DB())
	if err != nil {
		return nil, err
	}
	a := &App{Paths: p, ConfigPath: opt.ConfigPath, Loc: opt.Loc, Store: st, cfg: &cfgHolder{cfg: cfg}, launch: startProcess}

	a.Runner = &skills.ClaudeRunner{WorkDir: opt.Loc.SkillsDir, DataDir: filepath.Join(p.Root, "derived")}
	a.Summarizer = &process.SkillSummarizer{Runner: a.Runner, Store: st, Paths: p, Cfg: cfg, Logf: log.Printf}
	a.Voices = &diarize.Voices{Dir: p.Voices(), Threshold: cfg.VoiceMatchThreshold}
	a.Pipeline = &process.Pipeline{
		Cfg:         cfg,
		Paths:       p,
		Store:       st,
		Transcriber: media.Whisper{Run: media.ExecRunner, Bin: "whisper-cli", Model: modelPath(p, cfg.WhisperModel)},
		OCR:         media.RecorderOCR{Run: media.ExecRunner, Bin: opt.Loc.RecorderBin()},
		Diarizer:    diarize.NoopDiarizer{}, // Ruling 28: no diarizer ships yet
		Voices:      a.Voices,
		Run:         media.ExecRunner,
		Summarize:   a.Summarizer,
		Logf:        log.Printf,
	}
	a.Control = capture.NewSocketControl(p.Socket())
	a.Jobs = &Jobs{
		Cfg:        a.Cfg,
		Paths:      p,
		Store:      st,
		Pipeline:   a.Pipeline,
		Summarizer: a.Summarizer,
		Runner:     a.Runner,
		Control:    a.Control,
		FreeGB:     retention.FreeGB,
		Logf:       log.Printf,
	}
	a.Scheduler = &scheduler.Scheduler{Cfg: a.Cfg, Jobs: a.Jobs, Store: st, Logf: log.Printf}
	a.Supervisor = &capture.Supervisor{
		Bin:  "/usr/bin/open",
		Args: a.recorderArgs(false),
		Logf: log.Printf,
		// Rebuild the arguments on every start so a restart after a
		// settings change picks up the new exclusions.
		Start: func(ctx context.Context, bin string, args []string) (func() error, func(os.Signal) error, error) {
			wait, signal, err := a.launch(bin, a.recorderArgs(slices.Contains(args, "--degraded")))
			if err != nil {
				return nil, nil, err
			}
			// The recorder starts in normal mode; make the disk guard
			// re-send its state on the next check.
			if kerr := st.SetKV(kvDiskState, retention.StateNormal); kerr != nil {
				log.Printf("reset %s: %v", kvDiskState, kerr)
			}
			return wait, signal, nil
		},
	}
	a.Server = &web.Server{
		Store:   st,
		Paths:   p,
		Cfg:     a.Cfg,
		SaveCfg: a.SaveCfg,
		Control: a.Control,
		Jobs:    a.Jobs,
		Voices:  a.Voices,
	}
	return a, nil
}

// recorderArgs launches the recorder bundle through LaunchServices (so its
// own permissions apply) with `run` arguments from the live config.
func (a *App) recorderArgs(degraded bool) []string {
	args := append([]string{"-W", "-n", a.Loc.RecorderApp(), "--args"}, capture.RecorderArgs(a.Cfg(), a.Paths)...)
	if degraded {
		args = append(args, "--degraded")
	}
	return args
}

func startProcess(bin string, args []string) (func() error, func(os.Signal) error, error) {
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return cmd.Wait, cmd.Process.Signal, nil
}
