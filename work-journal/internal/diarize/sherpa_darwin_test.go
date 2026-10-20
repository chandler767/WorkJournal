//go:build darwin

package diarize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewSherpaDiarizerMissingModel(t *testing.T) {
	dir := t.TempDir()
	seg := filepath.Join(dir, "seg.onnx")
	emb := filepath.Join(dir, "emb.onnx")
	if _, err := NewSherpaDiarizer(seg, emb); err == nil || !strings.Contains(err.Error(), seg) {
		t.Fatalf("missing segmentation model: err = %v", err)
	}
	if err := os.WriteFile(seg, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSherpaDiarizer(seg, emb); err == nil || !strings.Contains(err.Error(), emb) {
		t.Fatalf("missing embedding model: err = %v", err)
	}
}
