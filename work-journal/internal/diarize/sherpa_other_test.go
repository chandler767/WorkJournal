//go:build !darwin

package diarize

import (
	"context"
	"strings"
	"testing"
)

func TestNewSherpaDiarizerNeedsMacOS(t *testing.T) {
	if _, err := NewSherpaDiarizer("seg.onnx", "emb.onnx"); err == nil || !strings.Contains(err.Error(), "macOS") {
		t.Fatalf("NewSherpaDiarizer err = %v, want a macOS error", err)
	}
	var d SherpaDiarizer
	if _, _, err := d.Diarize(context.Background(), "x.wav"); err == nil {
		t.Fatal("Diarize succeeded off macOS")
	}
	d.Close()
}
