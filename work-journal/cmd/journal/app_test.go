package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"workjournal/internal/config"
	"workjournal/internal/diarize"
	"workjournal/internal/skills"
)

func mkfile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeRepo lays out <repo>/.claude/skills/tessl__summarize-hour and
// <repo>/work-journal/go.mod, and returns (repo, module root).
func fakeRepo(t *testing.T) (string, string) {
	t.Helper()
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	mod := filepath.Join(repo, "work-journal")
	mkfile(t, filepath.Join(mod, "go.mod"), "module workjournal\n\ngo 1.26.0\n")
	mkfile(t, filepath.Join(repo, ".claude", "skills", "tessl__summarize-hour", "SKILL.md"), "x")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return repo, mod
}

func TestFindModuleRootWalksUp(t *testing.T) {
	_, mod := fakeRepo(t)
	bin := filepath.Join(mod, "bin")
	os.MkdirAll(bin, 0o755)
	got, err := findModuleRoot(bin)
	if err != nil || got != mod {
		t.Fatalf("findModuleRoot = %q, %v; want %q", got, err, mod)
	}
}

func TestFindModuleRootSkipsOtherModulesAndFallsBack(t *testing.T) {
	_, mod := fakeRepo(t)
	other := t.TempDir()
	mkfile(t, filepath.Join(other, "go.mod"), "module example.com/other\n")
	sub := filepath.Join(other, "x")
	os.MkdirAll(sub, 0o755)
	got, err := findModuleRoot(sub, filepath.Join(mod, "internal"))
	if err != nil || got != mod {
		t.Fatalf("findModuleRoot = %q, %v; want %q", got, err, mod)
	}
	if _, err := findModuleRoot(sub); err == nil {
		t.Fatal("found a module root in an unrelated module")
	}
}

func TestFindSkillsDir(t *testing.T) {
	repo, mod := fakeRepo(t)
	got, ok := findSkillsDir(mod)
	if !ok || got != repo {
		t.Fatalf("findSkillsDir = %q, %v; want %q", got, ok, repo)
	}
	if _, ok := findSkillsDir(t.TempDir()); ok {
		t.Fatal("found skills in an empty dir")
	}
}

func TestResolveLocations(t *testing.T) {
	repo, mod := fakeRepo(t)
	loc, err := resolveLocations("", filepath.Join(mod, "bin"))
	if err != nil {
		t.Fatal(err)
	}
	if loc.ModuleRoot != mod || loc.SkillsDir != repo || loc.RepoRoot != repo {
		t.Fatalf("loc = %+v", loc)
	}
	if want := filepath.Join(mod, "recorder", "build", "WorkJournalRecorder.app"); loc.RecorderApp() != want {
		t.Fatalf("app = %q", loc.RecorderApp())
	}
	if want := filepath.Join(loc.RecorderApp(), "Contents", "MacOS", "recorder"); loc.RecorderBin() != want {
		t.Fatalf("bin = %q", loc.RecorderBin())
	}
}

func TestResolveLocationsRepoOverride(t *testing.T) {
	_, mod := fakeRepo(t)
	alt, _ := filepath.EvalSymlinks(t.TempDir())
	loc, err := resolveLocations(alt, filepath.Join(mod, "bin"))
	if err != nil {
		t.Fatal(err)
	}
	if loc.SkillsDir != alt || loc.RepoRoot != alt || loc.ModuleRoot != mod {
		t.Fatalf("loc = %+v", loc)
	}
	// --repo alone is enough to find the module beneath it.
	repo, _ := fakeRepo(t)
	loc, err = resolveLocations(repo, t.TempDir())
	if err != nil || loc.ModuleRoot != filepath.Join(repo, "work-journal") {
		t.Fatalf("loc = %+v, %v", loc, err)
	}
}

func TestResolveLocationsWithoutSkills(t *testing.T) {
	_, mod := fakeRepo(t)
	os.RemoveAll(filepath.Join(filepath.Dir(mod), ".claude"))
	loc, err := resolveLocations("", mod)
	if err != nil {
		t.Fatal(err)
	}
	if loc.SkillsDir != "" {
		t.Fatalf("skills dir = %q", loc.SkillsDir)
	}
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	repo, mod := fakeRepo(t)
	cfg := config.Default()
	cfg.DataDir = filepath.Join(t.TempDir(), "data")
	a, err := Build(cfg, Options{
		ConfigPath: filepath.Join(cfg.DataDir, "config.toml"),
		Loc:        Locations{ModuleRoot: mod, RepoRoot: repo, SkillsDir: repo},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

func TestBuildWiresComponents(t *testing.T) {
	a := newTestApp(t)
	if a.Store == nil || a.Pipeline == nil || a.Scheduler == nil || a.Supervisor == nil || a.Server == nil || a.Jobs == nil {
		t.Fatalf("unwired app: %+v", a)
	}
	if a.Supervisor.Bin != "/usr/bin/open" {
		t.Fatalf("supervisor bin = %q", a.Supervisor.Bin)
	}
	if a.Runner.WorkDir != a.Loc.SkillsDir || a.Runner.DataDir != filepath.Join(a.Paths.Root, "derived") {
		t.Fatalf("runner = %+v", a.Runner)
	}
	if _, err := os.Stat(a.Paths.Models()); err != nil {
		t.Fatalf("data dirs not ensured: %v", err)
	}
}

func TestRecorderArgsFollowSavedConfig(t *testing.T) {
	a := newTestApp(t)
	args := a.recorderArgs(false)
	logPath := filepath.Join(a.Paths.Root, "logs", "recorder.log")
	head := []string{"--stderr", logPath, "--stdout", logPath, "-W", "-n", a.Loc.RecorderApp(), "--args", "run"}
	if !slices.Equal(args[:9], head) {
		t.Fatalf("args = %v", args)
	}
	if slices.Contains(args, "--degraded") {
		t.Fatal("degraded without asking")
	}
	cfg := a.Cfg()
	cfg.ExcludedApps = append(cfg.ExcludedApps, "Signal")
	if err := a.SaveCfg(cfg); err != nil {
		t.Fatal(err)
	}
	args = a.recorderArgs(true)
	if !slices.Contains(args, "Signal") || args[len(args)-1] != "--degraded" {
		t.Fatalf("args after save = %v", args)
	}
	saved, err := config.Load(a.ConfigPath)
	if err != nil || !slices.Contains(saved.ExcludedApps, "Signal") {
		t.Fatalf("saved config = %+v, %v", saved.ExcludedApps, err)
	}
	if !slices.Contains(a.Jobs.Cfg().ExcludedApps, "Signal") {
		t.Fatal("jobs see a stale config")
	}
}

func TestRunnerReadsOnlyDerived(t *testing.T) {
	a := newTestApp(t)
	var argv []string
	a.Runner.Exec = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		argv = args
		return nil, errors.New("stop")
	}
	a.Runner.Run(context.Background(), "summarize-hour", "/x.json", &struct{}{})
	i := slices.Index(argv, "--add-dir")
	if i < 0 || i+1 >= len(argv) {
		t.Fatalf("argv = %v", argv)
	}
	want := filepath.Join(a.Paths.Root, "derived")
	if argv[i+1] != want || slices.Index(argv[i+1:], "--add-dir") > 0 {
		t.Fatalf("--add-dir %q, want exactly %q (argv %v)", argv[i+1], want, argv)
	}
	if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
		t.Fatalf("derived dir missing: %v", err)
	}
}

func TestDiskGuardResendsAfterRecorderRestart(t *testing.T) {
	a := newTestApp(t)
	ctl := &fakeControl{}
	a.Jobs.Control = ctl
	a.Jobs.FreeGB = func(string) (float64, error) { return 3, nil } // below min_free_gb 5, above 2.5
	launches := 0
	a.launch = func(string, []string) (func() error, func(os.Signal) error, error) {
		launches++
		return func() error { return nil }, func(os.Signal) error { return nil }, nil
	}
	ctx := context.Background()
	if err := a.Jobs.DiskCheck(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Jobs.DiskCheck(ctx); err != nil {
		t.Fatal(err)
	}
	if want := []string{"screens_off"}; !equal(ctl.sentCmds(), want) {
		t.Fatalf("sent %v, want %v", ctl.sentCmds(), want)
	}
	// The recorder restarts and comes back in normal mode.
	if _, _, err := a.Supervisor.Start(ctx, "/usr/bin/open", a.recorderArgs(false)); err != nil {
		t.Fatal(err)
	}
	if launches != 1 {
		t.Fatalf("launches = %d", launches)
	}
	if err := a.Jobs.DiskCheck(ctx); err != nil {
		t.Fatal(err)
	}
	if want := []string{"screens_off", "screens_off"}; !equal(ctl.sentCmds(), want) {
		t.Fatalf("sent %v, want a re-send after restart %v", ctl.sentCmds(), want)
	}
}

func TestFailedRecorderStartKeepsDiskState(t *testing.T) {
	a := newTestApp(t)
	a.Store.SetKV("disk_state", "audio_off")
	a.launch = func(string, []string) (func() error, func(os.Signal) error, error) {
		return nil, nil, errors.New("no such app")
	}
	if _, _, err := a.Supervisor.Start(context.Background(), "/usr/bin/open", nil); err == nil {
		t.Fatal("start error swallowed")
	}
	if v, _, _ := a.Store.GetKV("disk_state"); v != "audio_off" {
		t.Fatalf("disk_state = %q", v)
	}
}

// I4: every recorder start first quits a recorder still serving the socket.
func TestRecorderStartQuitsRunningRecorderFirst(t *testing.T) {
	a := newTestApp(t)
	var order []string
	a.quitRunning = func(sock string, probe, gone time.Duration) (bool, error) {
		if sock != a.Paths.Socket() || probe != time.Second || gone != 5*time.Second {
			t.Errorf("quitRunning(%q, %s, %s)", sock, probe, gone)
		}
		order = append(order, "quit")
		return true, nil
	}
	a.launch = func(string, []string) (func() error, func(os.Signal) error, error) {
		order = append(order, "launch")
		return func() error { return nil }, func(os.Signal) error { return nil }, nil
	}
	if _, _, err := a.Supervisor.Start(context.Background(), "/usr/bin/open", nil); err != nil {
		t.Fatal(err)
	}
	if !equal(order, []string{"quit", "launch"}) {
		t.Fatalf("order = %v", order)
	}
	// A recorder that will not quit is logged; the launch still goes ahead
	// (the new recorder refuses to start while the old one is serving).
	order = nil
	a.quitRunning = func(string, time.Duration, time.Duration) (bool, error) {
		order = append(order, "quit")
		return true, errors.New("did not exit")
	}
	if _, _, err := a.Supervisor.Start(context.Background(), "/usr/bin/open", nil); err != nil {
		t.Fatal(err)
	}
	if !equal(order, []string{"quit", "launch"}) {
		t.Fatalf("order = %v", order)
	}
}

// I4: closing the terminal (SIGHUP) shuts serve down cleanly, so the
// recorder gets its quit instead of being orphaned.
func TestShutdownSignalsIncludeHangup(t *testing.T) {
	for _, s := range []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		if !slices.Contains(shutdownSignals, s) {
			t.Errorf("%v not handled", s)
		}
	}
}

type fakeSherpa struct {
	diarize.NoopDiarizer
	closed bool
}

func (f *fakeSherpa) Close() { f.closed = true }

func TestSpeakerDiarizerChoice(t *testing.T) {
	p := config.Paths{Root: t.TempDir()}
	seg, emb := diarize.ModelPaths(p.Models())
	var loaded []string
	fake := &fakeSherpa{}
	var loadErr error
	load := func(s, e string) (closingDiarizer, error) {
		loaded = append(loaded, s, e)
		if loadErr != nil {
			return nil, loadErr
		}
		return fake, nil
	}
	var logs []string
	logf := func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	cfg := config.Default()

	cfg.Diarization = "off"
	if d, c := speakerDiarizer(cfg, p, load, logf); d != (diarize.NoopDiarizer{}) || c != nil || len(loaded) != 0 {
		t.Fatalf("off: %v close=%v loaded %v", d, c != nil, loaded)
	}

	cfg.Diarization = "sherpa"
	if d, c := speakerDiarizer(cfg, p, load, logf); d != (diarize.NoopDiarizer{}) || c != nil || len(loaded) != 0 {
		t.Fatalf("missing models: %v close=%v loaded %v", d, c != nil, loaded)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "journal setup") {
		t.Fatalf("missing models not logged: %q", logs)
	}

	mkfile(t, seg, "s")
	mkfile(t, emb, "e")
	d, c := speakerDiarizer(cfg, p, load, logf)
	if d != fake || c == nil || strings.Join(loaded, " ") != seg+" "+emb {
		t.Fatalf("models present: %v close=%v loaded %v", d, c != nil, loaded)
	}
	c()
	if !fake.closed {
		t.Fatal("close func did not close the diarizer")
	}

	logs = nil
	loadErr = errors.New("bad model")
	if d, c := speakerDiarizer(cfg, p, load, logf); d != (diarize.NoopDiarizer{}) || c != nil {
		t.Fatalf("load error: %v close=%v", d, c != nil)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "bad model") {
		t.Fatalf("load error not logged: %q", logs)
	}
}

func TestBuildFallsBackToNoopWithoutModels(t *testing.T) {
	a := newTestApp(t) // default config: diarization "sherpa", no models
	if _, ok := a.Pipeline.Diarizer.(diarize.NoopDiarizer); !ok {
		t.Fatalf("diarizer = %T", a.Pipeline.Diarizer)
	}
}

func TestBuildWiresPrivacyFilter(t *testing.T) {
	a := newTestApp(t)
	if a.Pipeline.Privacy == nil || a.Pipeline.Privacy != skills.Runner(a.Gate) {
		t.Fatalf("privacy runner = %v, want the paused-aware skills runner", a.Pipeline.Privacy)
	}
}

// Every Claude run goes through the usage-limit pause gate.
func TestBuildGatesClaudeRunsBehindUsagePause(t *testing.T) {
	a := newTestApp(t)
	if a.Gate == nil || a.Gate.Runner != skills.Runner(a.Runner) || a.Gate.KV == nil {
		t.Fatalf("gate = %+v", a.Gate)
	}
	for name, r := range map[string]skills.Runner{"summarizer": a.Summarizer.Runner, "privacy": a.Pipeline.Privacy, "jobs": a.Jobs.Runner} {
		if r != skills.Runner(a.Gate) {
			t.Errorf("%s runner is not the gate", name)
		}
	}
	if !a.Gate.Enabled() {
		t.Error("gate disabled with the default config")
	}
	cfg := a.Cfg()
	cfg.PauseOnUsageLimit = false
	a.cfg.set(cfg)
	if a.Gate.Enabled() {
		t.Error("gate ignores pause_on_usage_limit")
	}
}
