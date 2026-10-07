package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"workjournal/internal/config"
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
	head := []string{"-W", "-n", a.Loc.RecorderApp(), "--args", "run"}
	if !slices.Equal(args[:5], head) {
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
