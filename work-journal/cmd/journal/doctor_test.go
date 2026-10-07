package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workjournal/internal/config"
)

type fakeInfo struct{ size int64 }

func (f fakeInfo) Name() string       { return "f" }
func (f fakeInfo) Size() int64        { return f.size }
func (f fakeInfo) Mode() fs.FileMode  { return 0o644 }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return false }
func (f fakeInfo) Sys() any           { return nil }

// okDeps returns deps under which every check passes.
func okDeps() DoctorDeps {
	cfg := config.Default()
	cfg.DataDir = "/data"
	return DoctorDeps{
		Cfg:         cfg,
		RecorderApp: "/repo/work-journal/recorder/build/WorkJournalRecorder.app",
		SkillsDir:   "/repo",
		LookPath:    func(name string) (string, error) { return "/usr/local/bin/" + name, nil },
		Stat:        func(string) (fs.FileInfo, error) { return fakeInfo{size: 100}, nil },
		Glob: func(pattern string) ([]string, error) {
			return []string{strings.Replace(pattern, "*summarize-hour*", "tessl__summarize-hour", 1)}, nil
		},
		FreeGB:         func(string) (float64, error) { return 50, nil },
		SelfTest:       func(string) error { return nil },
		ClaudeResponds: func() error { return nil },
	}
}

func find(t *testing.T, cs []Check, name string) Check {
	t.Helper()
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check %q in %+v", name, cs)
	return Check{}
}

func TestDoctorReportsMissingWhisper(t *testing.T) {
	d := okDeps()
	d.LookPath = func(name string) (string, error) {
		if name == "whisper-cli" {
			return "", errors.New("not found")
		}
		return "/usr/local/bin/" + name, nil
	}
	c := find(t, RunChecks(d), "whisper-cli")
	if c.OK {
		t.Fatal("whisper-cli reported OK")
	}
	if !strings.Contains(c.Fix, "brew install whisper-cpp") {
		t.Fatalf("fix = %q", c.Fix)
	}
}

func TestDoctorAllOK(t *testing.T) {
	cs := RunChecks(okDeps())
	want := []string{"whisper-cli", "whisper model", "claude", "claude responds", "skills", "recorder app", "recorder selftest", "free disk"}
	if len(cs) != len(want) {
		t.Fatalf("checks = %+v, want names %v", cs, want)
	}
	for i, c := range cs {
		if c.Name != want[i] || !c.OK {
			t.Fatalf("check %d = %+v, want %q OK", i, c, want[i])
		}
	}
	var out bytes.Buffer
	if !printChecks(&out, cs) {
		t.Fatal("printChecks reported failure")
	}
	if strings.Count(out.String(), "✓") != len(want) || strings.Contains(out.String(), "✗") {
		t.Fatalf("output:\n%s", out.String())
	}
}

func TestDoctorWhisperModelPathAndEmptyFile(t *testing.T) {
	d := okDeps()
	var statted []string
	d.Stat = func(p string) (fs.FileInfo, error) {
		statted = append(statted, p)
		if strings.HasSuffix(p, ".bin") {
			return fakeInfo{size: 0}, nil
		}
		return fakeInfo{size: 1}, nil
	}
	c := find(t, RunChecks(d), "whisper model")
	if c.OK || !strings.Contains(c.Fix, "journal setup") {
		t.Fatalf("check = %+v", c)
	}
	want := filepath.Join("/data", "models", "ggml-base.en.bin")
	found := false
	for _, p := range statted {
		found = found || p == want
	}
	if !found {
		t.Fatalf("stat calls %v, want %s", statted, want)
	}
}

func TestDoctorMissingClaudeAndSkills(t *testing.T) {
	d := okDeps()
	d.LookPath = func(name string) (string, error) {
		if name == "claude" {
			return "", errors.New("not found")
		}
		return "/bin/" + name, nil
	}
	d.Glob = func(string) ([]string, error) { return nil, nil }
	cs := RunChecks(d)
	if c := find(t, cs, "claude"); c.OK || c.Fix == "" {
		t.Fatalf("claude = %+v", c)
	}
	if c := find(t, cs, "skills"); c.OK || !strings.Contains(c.Fix, "tessl install") {
		t.Fatalf("skills = %+v", c)
	}
}

func TestDoctorSkillsDirNotFound(t *testing.T) {
	d := okDeps()
	d.SkillsDir = ""
	globbed := false
	d.Glob = func(string) ([]string, error) { globbed = true; return []string{"x"}, nil }
	c := find(t, RunChecks(d), "skills")
	if c.OK || !strings.Contains(c.Fix, "--repo") {
		t.Fatalf("skills = %+v", c)
	}
	if globbed {
		t.Fatal("globbed without a skills dir")
	}
}

func TestDoctorRecorderNotBuiltSkipsSelftest(t *testing.T) {
	d := okDeps()
	d.Stat = func(p string) (fs.FileInfo, error) {
		if strings.Contains(p, "WorkJournalRecorder.app") {
			return nil, fs.ErrNotExist
		}
		return fakeInfo{size: 1}, nil
	}
	ran := false
	d.SelfTest = func(string) error { ran = true; return nil }
	cs := RunChecks(d)
	if c := find(t, cs, "recorder app"); c.OK || !strings.Contains(c.Fix, "recorder/build.sh") {
		t.Fatalf("recorder app = %+v", c)
	}
	if c := find(t, cs, "recorder selftest"); c.OK {
		t.Fatalf("selftest = %+v", c)
	}
	if ran {
		t.Fatal("selftest ran without a built recorder")
	}
}

func TestDoctorSelftestFailureNamesPermissions(t *testing.T) {
	d := okDeps()
	var app string
	d.SelfTest = func(a string) error { app = a; return errors.New("selftest failed: screen capture denied") }
	var out bytes.Buffer
	cs := RunChecks(d)
	c := find(t, cs, "recorder selftest")
	if c.OK {
		t.Fatal("selftest OK")
	}
	for _, want := range []string{"Privacy & Security", "Screen & System Audio Recording", "Microphone", "Accessibility", "rebuild", "screen capture denied"} {
		if !strings.Contains(c.Fix, want) {
			t.Fatalf("fix %q lacks %q", c.Fix, want)
		}
	}
	if app != d.RecorderApp {
		t.Fatalf("selftest app = %q", app)
	}
	if printChecks(&out, cs) {
		t.Fatal("printChecks reported success")
	}
	if !strings.Contains(out.String(), "✗ recorder selftest") {
		t.Fatalf("output:\n%s", out.String())
	}
}

func TestDoctorLowDisk(t *testing.T) {
	d := okDeps()
	d.Cfg.MinFreeGB = 10
	var path string
	d.FreeGB = func(p string) (float64, error) { path = p; return 3.2, nil }
	c := find(t, RunChecks(d), "free disk")
	if c.OK || !strings.Contains(c.Fix, "3.2") || !strings.Contains(c.Fix, "10") {
		t.Fatalf("free disk = %+v", c)
	}
	if path != "/data" {
		t.Fatalf("free space of %q", path)
	}
}

func TestDoctorSherpaUnavailable(t *testing.T) {
	d := okDeps()
	d.Cfg.Diarization = "sherpa"
	c := find(t, RunChecks(d), "diarization")
	if c.OK || !strings.Contains(c.Fix, `diarization = "off"`) {
		t.Fatalf("diarization = %+v", c)
	}
}

func TestNearestExisting(t *testing.T) {
	dir := t.TempDir()
	got := nearestExisting(filepath.Join(dir, "a", "b", "c"))
	if got != dir {
		t.Fatalf("nearestExisting = %q, want %q", got, dir)
	}
	if got := nearestExisting(dir); got != dir {
		t.Fatalf("existing dir = %q", got)
	}
}

func TestSelftestReadsResultFile(t *testing.T) {
	ok := func(_ string, args []string) error {
		dd := args[len(args)-1]
		return os.WriteFile(filepath.Join(dd, "selftest.result"), []byte("selftest ok\n"), 0o644)
	}
	if err := recorderSelfTest("/x.app", ok); err != nil {
		t.Fatalf("ok selftest: %v", err)
	}
	bad := func(_ string, args []string) error {
		if want := []string{"-W", "-n", "/x.app", "--args", "selftest", "--data-dir"}; strings.Join(args[:6], " ") != strings.Join(want, " ") {
			t.Errorf("args = %v", args)
		}
		dd := args[len(args)-1]
		os.WriteFile(filepath.Join(dd, "selftest.result"), []byte("selftest failed: microphone denied\n"), 0o644)
		return errors.New("exit status 1")
	}
	if err := recorderSelfTest("/x.app", bad); err == nil || !strings.Contains(err.Error(), "microphone denied") {
		t.Fatalf("bad selftest err = %v", err)
	}
	none := func(string, []string) error { return nil }
	if err := recorderSelfTest("/x.app", none); err == nil {
		t.Fatal("missing result file accepted")
	}
}

// I3: a claude that is installed but signed out fails every skill run.
func TestDoctorClaudeResponds(t *testing.T) {
	d := okDeps()
	d.ClaudeResponds = func() error { return errors.New("Invalid API key · Please run /login") }
	c := find(t, RunChecks(d), "claude responds")
	if c.OK || !strings.Contains(c.Fix, "run `claude` and sign in") || !strings.Contains(c.Fix, "Invalid API key") {
		t.Fatalf("check = %+v", c)
	}
	d = okDeps()
	d.LookPath = func(name string) (string, error) {
		if name == "claude" {
			return "", errors.New("not found")
		}
		return "/usr/local/bin/" + name, nil
	}
	d.ClaudeResponds = func() error { t.Fatal("probed a missing claude"); return nil }
	if c := find(t, RunChecks(d), "claude responds"); c.OK {
		t.Fatalf("check = %+v", c)
	}
}

func TestClaudeRespondsProbe(t *testing.T) {
	var gotArgs []string
	var gotDeadline bool
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		_, gotDeadline = ctx.Deadline()
		gotArgs = append([]string{name}, args...)
		return []byte(`{"type":"result","is_error":false,"result":"OK"}`), nil
	}
	if err := claudeResponds(run); err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "-p", "Reply with OK", "--output-format", "json", "--tools", "", "--strict-mcp-config"}
	if !equal(gotArgs, want) || !gotDeadline {
		t.Fatalf("args = %q deadline %v", gotArgs, gotDeadline)
	}
	for _, out := range []string{`{"type":"result","is_error":true,"result":"Invalid API key"}`, "Error: not logged in"} {
		run := func(context.Context, string, ...string) ([]byte, error) { return []byte(out), nil }
		if err := claudeResponds(run); err == nil {
			t.Fatalf("output %q accepted", out)
		}
	}
	failing := func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("exit status 1") }
	if err := claudeResponds(failing); err == nil {
		t.Fatal("exec error accepted")
	}
}
