package main

import (
	"archive/tar"
	"compress/bzip2"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"workjournal/internal/diarize"
)

// setupSpeakerModels downloads the sherpa embedding model and the
// segmentation archive into models (base replaces diarize.ReleaseBase), and
// extracts the archive. Files already present are skipped.
func setupSpeakerModels(ctx context.Context, models, base string, download func(ctx context.Context, url, dest string) (bool, error), out io.Writer) error {
	seg, emb := diarize.ModelPaths(models)

	fmt.Fprintf(out, "speaker embedding model: ")
	skipped, err := download(ctx, base+diarize.EmbeddingRelURL, emb)
	if err != nil {
		fmt.Fprintln(out, "failed")
		return err
	}
	fmt.Fprintln(out, presence(skipped))

	fmt.Fprintf(out, "speaker segmentation model: ")
	if fi, err := os.Stat(seg); err == nil && fi.Size() > 0 {
		fmt.Fprintln(out, presence(true))
		return nil
	}
	archive := filepath.Join(models, diarize.SegmentationArchive)
	if _, err := download(ctx, base+diarize.SegmentationRelURL, archive); err != nil {
		fmt.Fprintln(out, "failed")
		return err
	}
	if err := extractTarBz2(archive, models, diarize.SegmentationDir); err != nil {
		fmt.Fprintln(out, "failed")
		return err
	}
	if fi, err := os.Stat(seg); err != nil || fi.Size() == 0 {
		fmt.Fprintln(out, "failed")
		return fmt.Errorf("%s has no %s", diarize.SegmentationArchive, filepath.Base(seg))
	}
	fmt.Fprintln(out, presence(false))
	return os.Remove(archive)
}

func presence(skipped bool) string {
	if skipped {
		return "already present"
	}
	return "downloaded"
}

// extractTarBz2 unpacks archive's top directory into destDir/top. It extracts
// into a temporary directory first and moves top into place only when every
// entry was safe and written, replacing any earlier destDir/top. Entries
// that are absolute or escape the archive root are rejected; entries other
// than regular files and directories are ignored.
func extractTarBz2(archive, destDir, top string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	tmp, err := os.MkdirTemp(destDir, ".extract-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	tr := tar.NewReader(bzip2.NewReader(f))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", filepath.Base(archive), err)
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if !filepath.IsLocal(filepath.FromSlash(name)) {
			return fmt.Errorf("%s: unsafe entry %q", filepath.Base(archive), h.Name)
		}
		target := filepath.Join(tmp, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeEntry(target, tr); err != nil {
				return err
			}
		}
	}
	src := filepath.Join(tmp, top)
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s has no %s/ directory", filepath.Base(archive), top)
	}
	dest := filepath.Join(destDir, top)
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	return os.Rename(src, dest)
}

func writeEntry(target string, r io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	w, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, r)
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	return err
}
