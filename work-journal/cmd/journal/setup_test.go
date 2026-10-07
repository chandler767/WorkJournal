package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"workjournal/internal/config"
)

func TestWhisperModelURL(t *testing.T) {
	got := whisperModelURL(defaultModelBase, "base.en")
	if got != "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-base.en.bin" {
		t.Fatalf("url = %q", got)
	}
}

func TestDownloadWritesFileAtomically(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("model-bytes"))
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "ggml-base.en.bin")
	d := downloader{Client: srv.Client()}
	skipped, err := d.fetch(context.Background(), srv.URL+"/m.bin", dest)
	if err != nil || skipped {
		t.Fatalf("fetch = %v, %v", skipped, err)
	}
	b, err := os.ReadFile(dest)
	if err != nil || string(b) != "model-bytes" {
		t.Fatalf("file = %q, %v", b, err)
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Fatalf(".part left behind: %v", err)
	}
}

func TestDownloadNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()
	dir := t.TempDir()
	dest := filepath.Join(dir, "ggml-base.en.bin")
	_, err := downloader{Client: srv.Client()}.fetch(context.Background(), srv.URL+"/m.bin", dest)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v", err)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("files left: %v", ents)
	}
}

func TestDownloadEmptyBodyIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	dir := t.TempDir()
	dest := filepath.Join(dir, "m.bin")
	if _, err := (downloader{Client: srv.Client()}).fetch(context.Background(), srv.URL, dest); err == nil {
		t.Fatal("empty download accepted")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("files left: %v", ents)
	}
}

func TestDownloadSkipsExisting(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte("new"))
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "m.bin")
	mkfile(t, dest, "old")
	skipped, err := downloader{Client: srv.Client()}.fetch(context.Background(), srv.URL, dest)
	if err != nil || !skipped {
		t.Fatalf("fetch = %v, %v", skipped, err)
	}
	if hits.Load() != 0 {
		t.Fatal("downloaded an existing file")
	}
	if b, _ := os.ReadFile(dest); string(b) != "old" {
		t.Fatalf("file = %q", b)
	}
	// An empty leftover is not "existing".
	mkfile(t, dest, "")
	if skipped, err := (downloader{Client: srv.Client()}).fetch(context.Background(), srv.URL, dest); err != nil || skipped {
		t.Fatalf("empty file: %v, %v", skipped, err)
	}
}

type runCall struct {
	dir  string
	argv []string
}

func TestRunSetup(t *testing.T) {
	repo, mod := fakeRepo(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ggerganov/whisper.cpp/resolve/main/ggml-tiny.en.bin" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("weights"))
	}))
	defer srv.Close()
	cfg := config.Default()
	cfg.DataDir = filepath.Join(t.TempDir(), "data")
	cfg.WhisperModel = "tiny.en"
	cfgPath := filepath.Join(cfg.DataDir, "config.toml")
	var calls []runCall
	deps := setupDeps{
		Run: func(_ context.Context, dir, name string, args ...string) error {
			calls = append(calls, runCall{dir, append([]string{name}, args...)})
			return nil
		},
		Download:  downloader{Client: srv.Client()}.fetch,
		ModelBase: srv.URL,
	}
	var out bytes.Buffer
	loc := Locations{ModuleRoot: mod, RepoRoot: repo, SkillsDir: repo}
	if err := runSetup(context.Background(), cfgPath, cfg, loc, false, deps, &out); err != nil {
		t.Fatalf("setup: %v\n%s", err, out.String())
	}
	p := config.Paths{Root: cfg.DataDir}
	if b, err := os.ReadFile(filepath.Join(p.Models(), "ggml-tiny.en.bin")); err != nil || string(b) != "weights" {
		t.Fatalf("model = %q, %v", b, err)
	}
	if got, err := config.Load(cfgPath); err != nil || got.WhisperModel != "tiny.en" {
		t.Fatalf("config = %+v, %v", got, err)
	}
	want := []runCall{
		{mod, []string{filepath.Join(mod, "recorder", "build.sh")}},
		{repo, []string{"tessl", "install", "file:./work-journal/skills/work-journal", "--yes"}},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %+v", calls)
	}
	for i := range want {
		if calls[i].dir != want[i].dir || strings.Join(calls[i].argv, " ") != strings.Join(want[i].argv, " ") {
			t.Fatalf("call %d = %+v, want %+v", i, calls[i], want[i])
		}
	}

	// A second run keeps an edited config and skips the model.
	mkfile(t, cfgPath, "retention_days = 3\n")
	calls = nil
	if err := runSetup(context.Background(), cfgPath, cfg, loc, false, deps, &out); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(cfgPath); string(b) != "retention_days = 3\n" {
		t.Fatalf("config overwritten: %q", b)
	}
	if !strings.Contains(out.String(), "already present") {
		t.Fatalf("output:\n%s", out.String())
	}
}

func TestRunSetupStopsOnFailure(t *testing.T) {
	repo, mod := fakeRepo(t)
	cfg := config.Default()
	cfg.DataDir = filepath.Join(t.TempDir(), "data")
	deps := setupDeps{
		Run: func(_ context.Context, dir, name string, args ...string) error {
			return os.ErrPermission
		},
		Download: func(context.Context, string, string) (bool, error) {
			t.Fatal("downloaded after a failed build")
			return false, nil
		},
	}
	err := runSetup(context.Background(), filepath.Join(cfg.DataDir, "config.toml"), cfg,
		Locations{ModuleRoot: mod, RepoRoot: repo}, false, deps, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "build recorder") {
		t.Fatalf("err = %v", err)
	}
}

// M12: an existing recorder is not rebuilt (that would reset its macOS
// permissions) unless --rebuild is given.
func TestRunSetupSkipsBuiltRecorderUnlessRebuild(t *testing.T) {
	repo, mod := fakeRepo(t)
	cfg := config.Default()
	cfg.DataDir = filepath.Join(t.TempDir(), "data")
	cfgPath := filepath.Join(cfg.DataDir, "config.toml")
	loc := Locations{ModuleRoot: mod, RepoRoot: repo, SkillsDir: repo}
	mkfile(t, loc.RecorderBin(), "binary")
	var calls []runCall
	deps := setupDeps{
		Run: func(_ context.Context, dir, name string, args ...string) error {
			calls = append(calls, runCall{dir, append([]string{name}, args...)})
			return nil
		},
		Download: func(context.Context, string, string) (bool, error) { return true, nil },
	}
	var out bytes.Buffer
	if err := runSetup(context.Background(), cfgPath, cfg, loc, false, deps, &out); err != nil {
		t.Fatal(err)
	}
	for _, c := range calls {
		if strings.HasSuffix(c.argv[0], "build.sh") {
			t.Fatalf("rebuilt an existing recorder: %+v", calls)
		}
	}
	if !strings.Contains(out.String(), "--rebuild") || !strings.Contains(out.String(), "resets") {
		t.Fatalf("output lacks the rebuild note:\n%s", out.String())
	}
	calls, out = nil, bytes.Buffer{}
	if err := runSetup(context.Background(), cfgPath, cfg, loc, true, deps, &out); err != nil {
		t.Fatal(err)
	}
	if len(calls) == 0 || !strings.HasSuffix(calls[0].argv[0], "build.sh") {
		t.Fatalf("--rebuild did not build: %+v", calls)
	}
	if !strings.Contains(out.String(), "resets") {
		t.Fatalf("output lacks the permissions note:\n%s", out.String())
	}
}
