package diarize

import (
	"os"
	"path/filepath"
	"reflect"
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

func TestModelPaths(t *testing.T) {
	seg, emb := ModelPaths("/d/models")
	if seg != "/d/models/sherpa-onnx-pyannote-segmentation-3-0/model.onnx" || emb != "/d/models/nemo_en_titanet_small.onnx" {
		t.Fatalf("ModelPaths = %q, %q", seg, emb)
	}
}

func TestPlanTurnsRenumbersAndPicksLongestSegments(t *testing.T) {
	const rate = 10 // samples per second, to keep numbers small
	segs := []segment{
		{start: 0, end: 2, speaker: 7},   // A, 2 s
		{start: 2, end: 3, speaker: 4},   // B, 1 s
		{start: 3, end: 8, speaker: 7},   // A, 5 s (longest)
		{start: 8, end: 12, speaker: 4},  // B, 4 s
		{start: 12, end: 15, speaker: 7}, // A, 3 s
		{start: 15, end: 30, speaker: 9}, // C, runs past the 20 s of audio
	}
	turns, clips := planTurns(segs, 20*rate, rate, 9) // cap 9 s per speaker

	want := []Turn{{0, 2, 0}, {2, 3, 1}, {3, 8, 0}, {8, 12, 1}, {12, 15, 0}, {15, 30, 2}}
	if !reflect.DeepEqual(turns, want) {
		t.Fatalf("turns = %v, want %v", turns, want)
	}
	wantClips := map[int][][2]int{
		0: {{30, 80}, {120, 150}, {0, 10}}, // 5 s + 3 s + first 1 s of the 2 s segment
		1: {{80, 120}, {20, 30}},           // 4 s + 1 s
		2: {{150, 200}},                    // clamped to the audio
	}
	if !reflect.DeepEqual(clips, wantClips) {
		t.Fatalf("clips = %v, want %v", clips, wantClips)
	}
}

func TestModelURLs(t *testing.T) {
	// Exactly as in docs/spike-diarization.md ("recongition" is upstream's spelling).
	if got := ReleaseBase + SegmentationRelURL; got != "https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-segmentation-models/sherpa-onnx-pyannote-segmentation-3-0.tar.bz2" {
		t.Errorf("segmentation URL = %s", got)
	}
	if got := ReleaseBase + EmbeddingRelURL; got != "https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-recongition-models/nemo_en_titanet_small.onnx" {
		t.Errorf("embedding URL = %s", got)
	}
}
