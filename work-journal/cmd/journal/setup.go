package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"workjournal/internal/config"
)

const defaultModelBase = "https://huggingface.co"

func whisperModelURL(base, model string) string {
	return base + "/ggerganov/whisper.cpp/resolve/main/ggml-" + model + ".bin"
}

// downloader fetches model files.
type downloader struct{ Client *http.Client }

// fetch downloads url to dest unless dest already exists and is non-empty
// (skipped=true). It writes dest+".part" and renames it into place only after
// a 200 response with a non-empty body, so dest is never partial.
func (d downloader) fetch(ctx context.Context, url, dest string) (skipped bool, err error) {
	if fi, err := os.Stat(dest); err == nil && fi.Size() > 0 {
		return true, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	client := d.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	part := dest + ".part"
	f, err := os.Create(part)
	if err != nil {
		return false, err
	}
	defer os.Remove(part) // no-op after a successful rename
	n, err := io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return false, fmt.Errorf("download %s: %w", url, err)
	}
	if n == 0 {
		return false, fmt.Errorf("download %s: empty body", url)
	}
	return false, os.Rename(part, dest)
}

// setupDeps are the side effects of setup, injectable for tests.
type setupDeps struct {
	Run       func(ctx context.Context, dir, name string, args ...string) error
	Download  func(ctx context.Context, url, dest string) (skipped bool, err error)
	ModelBase string // "" means defaultModelBase
}

func defaultSetupDeps(stdout, stderr io.Writer) setupDeps {
	return setupDeps{
		Run: func(ctx context.Context, dir, name string, args ...string) error {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Dir, cmd.Stdout, cmd.Stderr = dir, stdout, stderr
			return cmd.Run()
		},
		Download: downloader{Client: http.DefaultClient}.fetch,
	}
}

// runSetup prepares the data directory, writes a default config if none
// exists, builds the recorder (only when missing, or always with rebuild),
// downloads the whisper model and installs the skills. It stops at the first
// failure.
func runSetup(ctx context.Context, cfgPath string, cfg config.Config, loc Locations, rebuild bool, deps setupDeps, out io.Writer) error {
	p := config.Paths{Root: cfg.DataDir}
	if err := p.Ensure(); err != nil {
		return fmt.Errorf("data dir: %w", err)
	}
	fmt.Fprintf(out, "data directory %s ready\n", p.Root)

	if _, err := os.Stat(cfgPath); errors.Is(err, fs.ErrNotExist) {
		if err := config.Save(cfgPath, cfg); err != nil {
			return fmt.Errorf("write config: %w", err)
		}
		fmt.Fprintf(out, "wrote %s\n", cfgPath)
	} else if err != nil {
		return err
	} else {
		fmt.Fprintf(out, "kept existing %s\n", cfgPath)
	}

	const permNote = "rebuilding resets the recorder's macOS permissions (grant them again, then run `journal doctor`)"
	if fi, err := os.Stat(loc.RecorderBin()); err == nil && fi.Size() > 0 && !rebuild {
		fmt.Fprintf(out, "recorder already built; pass --rebuild to rebuild it (%s)\n", permNote)
	} else {
		fmt.Fprintf(out, "building the recorder… note: %s\n", permNote)
		if err := deps.Run(ctx, loc.ModuleRoot, filepath.Join(loc.ModuleRoot, "recorder", "build.sh")); err != nil {
			return fmt.Errorf("build recorder: %w", err)
		}
	}

	base := deps.ModelBase
	if base == "" {
		base = defaultModelBase
	}
	dest := modelPath(p, cfg.WhisperModel)
	url := whisperModelURL(base, cfg.WhisperModel)
	fmt.Fprintf(out, "whisper model %s: ", filepath.Base(dest))
	skipped, err := deps.Download(ctx, url, dest)
	if err != nil {
		fmt.Fprintln(out, "failed")
		return fmt.Errorf("whisper model: %w", err)
	}
	if skipped {
		fmt.Fprintln(out, "already present")
	} else {
		fmt.Fprintln(out, "downloaded")
	}

	rel, err := filepath.Rel(loc.RepoRoot, filepath.Join(loc.ModuleRoot, "skills", "work-journal"))
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "installing skills…")
	if err := deps.Run(ctx, loc.RepoRoot, "tessl", "install", "file:./"+filepath.ToSlash(rel), "--yes"); err != nil {
		return fmt.Errorf("tessl install: %w", err)
	}
	fmt.Fprintln(out, "setup done; run `journal doctor` next")
	return nil
}
