package main

import (
	"archive/tar"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"workjournal/internal/config"
	"workjournal/internal/diarize"
)

type tarEntry struct {
	name, body string
	dir        bool
}

// tarBz2 builds a .tar.bz2 in memory (Go has no bzip2 writer; macOS ships bzip2).
func tarBz2(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var tb bytes.Buffer
	tw := tar.NewWriter(&tb)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if e.dir {
			h = &tar.Header{Name: e.name, Mode: 0o755, Typeflag: tar.TypeDir}
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bzip2", "-c")
	cmd.Stdin = &tb
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("bzip2: %v", err)
	}
	return out
}

func segArchive(t *testing.T) []byte {
	return tarBz2(t,
		tarEntry{name: diarize.SegmentationDir + "/", dir: true},
		tarEntry{name: diarize.SegmentationDir + "/model.onnx", body: "segmodel"},
		tarEntry{name: diarize.SegmentationDir + "/README.md", body: "readme"},
	)
}

func TestExtractTarBz2(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "a.tar.bz2")
	mkfile(t, archive, string(segArchive(t)))
	// A stale partial directory is replaced.
	mkfile(t, filepath.Join(dir, diarize.SegmentationDir, "stale"), "x")
	if err := extractTarBz2(archive, dir, diarize.SegmentationDir); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, diarize.SegmentationDir, "model.onnx")); err != nil || string(b) != "segmodel" {
		t.Fatalf("model.onnx = %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, diarize.SegmentationDir, "stale")); !os.IsNotExist(err) {
		t.Fatalf("stale file kept: %v", err)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".extract") {
			t.Fatalf("temp dir left: %s", e.Name())
		}
	}
}

func TestExtractTarBz2RejectsPathTraversal(t *testing.T) {
	for _, name := range []string{"../evil", diarize.SegmentationDir + "/../../evil", "/tmp/evil"} {
		root := t.TempDir()
		dir := filepath.Join(root, "models")
		archive := filepath.Join(root, "a.tar.bz2")
		mkfile(t, archive, string(tarBz2(t,
			tarEntry{name: diarize.SegmentationDir + "/model.onnx", body: "segmodel"},
			tarEntry{name: name, body: "pwned"},
		)))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		err := extractTarBz2(archive, dir, diarize.SegmentationDir)
		if err == nil || !strings.Contains(err.Error(), "unsafe") {
			t.Fatalf("%s: err = %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(root, "evil")); !os.IsNotExist(err) {
			t.Fatalf("%s: wrote outside models dir", name)
		}
		if _, err := os.Stat(filepath.Join(dir, diarize.SegmentationDir)); !os.IsNotExist(err) {
			t.Fatalf("%s: model dir installed from a rejected archive", name)
		}
	}
}

func TestRunSetupDownloadsSpeakerModels(t *testing.T) {
	repo, mod := fakeRepo(t)
	archive := segArchive(t)
	var sherpaHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ggerganov/whisper.cpp/resolve/main/ggml-base.en.bin":
			w.Write([]byte("weights"))
		case diarize.SegmentationRelURL:
			sherpaHits.Add(1)
			w.Write(archive)
		case diarize.EmbeddingRelURL:
			sherpaHits.Add(1)
			w.Write([]byte("embmodel"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cfg := config.Default()
	cfg.DataDir = filepath.Join(t.TempDir(), "data")
	cfg.Diarization = "sherpa"
	deps := setupDeps{
		Run:        func(context.Context, string, string, ...string) error { return nil },
		Download:   downloader{Client: srv.Client()}.fetch,
		ModelBase:  srv.URL,
		SherpaBase: srv.URL,
	}
	loc := Locations{ModuleRoot: mod, RepoRoot: repo, SkillsDir: repo}
	cfgPath := filepath.Join(cfg.DataDir, "config.toml")
	var out bytes.Buffer
	if err := runSetup(context.Background(), cfgPath, cfg, loc, false, deps, &out); err != nil {
		t.Fatalf("setup: %v\n%s", err, out.String())
	}
	models := config.Paths{Root: cfg.DataDir}.Models()
	seg, emb := diarize.ModelPaths(models)
	if b, err := os.ReadFile(seg); err != nil || string(b) != "segmodel" {
		t.Fatalf("segmentation model = %q, %v", b, err)
	}
	if b, err := os.ReadFile(emb); err != nil || string(b) != "embmodel" {
		t.Fatalf("embedding model = %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(models, diarize.SegmentationArchive)); !os.IsNotExist(err) {
		t.Fatalf("archive kept after extraction: %v", err)
	}
	if sherpaHits.Load() != 2 {
		t.Fatalf("sherpa downloads = %d", sherpaHits.Load())
	}

	out.Reset()
	if err := runSetup(context.Background(), cfgPath, cfg, loc, false, deps, &out); err != nil {
		t.Fatal(err)
	}
	if sherpaHits.Load() != 2 {
		t.Fatalf("models downloaded again: %d hits", sherpaHits.Load())
	}
	if !strings.Contains(out.String(), "speaker segmentation model: already present") {
		t.Fatalf("output:\n%s", out.String())
	}
}

func TestRunSetupDiarizationOffSkipsSpeakerModels(t *testing.T) {
	repo, mod := fakeRepo(t)
	cfg := config.Default()
	cfg.DataDir = filepath.Join(t.TempDir(), "data")
	cfg.Diarization = "off"
	var urls []string
	deps := setupDeps{
		Run: func(context.Context, string, string, ...string) error { return nil },
		Download: func(_ context.Context, url, _ string) (bool, error) {
			urls = append(urls, url)
			return true, nil
		},
	}
	loc := Locations{ModuleRoot: mod, RepoRoot: repo, SkillsDir: repo}
	if err := runSetup(context.Background(), filepath.Join(cfg.DataDir, "config.toml"), cfg, loc, false, deps, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(urls) != 1 || !strings.Contains(urls[0], "whisper") {
		t.Fatalf("downloads = %v, want only the whisper model", urls)
	}
}

// Ruling 38: a corrupt archive is removed so the next setup downloads it again.
func TestExtractFailureRemovesArchive(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, diarize.SegmentationArchive)
	mkfile(t, archive, "not a bzip2 stream")
	var calls int
	download := func(_ context.Context, url, dest string) (bool, error) {
		calls++
		if strings.HasSuffix(dest, diarize.EmbeddingFile) {
			return false, os.WriteFile(dest, []byte("emb"), 0o644)
		}
		return true, nil // the corrupt archive is already there
	}
	err := setupSpeakerModels(context.Background(), dir, "http://x", download, &bytes.Buffer{})
	if err == nil {
		t.Fatal("corrupt archive accepted")
	}
	if _, serr := os.Stat(archive); !os.IsNotExist(serr) {
		t.Fatalf("corrupt archive kept (err %v): %v", err, serr)
	}
}
