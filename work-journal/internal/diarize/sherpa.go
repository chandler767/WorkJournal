//go:build darwin

package diarize

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	sherpa "github.com/k2-fsa/sherpa-onnx-go-macos"
)

// This is the only file that imports the sherpa-onnx binding (cgo), which
// only builds on macOS. sherpa_other.go stands in for it elsewhere.

const (
	sampleRate = 16000
	// embedSeconds caps the audio per speaker fed to the embedding model.
	embedSeconds = 30
)

// SherpaDiarizer runs pyannote-3.0 segmentation and titanet-small speaker
// embeddings locally (see docs/spike-diarization.md). Models load once in
// NewSherpaDiarizer. Diarize calls are serialized; Close releases the models.
type SherpaDiarizer struct {
	mu sync.Mutex
	sd *sherpa.OfflineSpeakerDiarization
	ex *sherpa.SpeakerEmbeddingExtractor
}

var _ Diarizer = (*SherpaDiarizer)(nil)

// NewSherpaDiarizer loads the segmentation and embedding models. A missing
// or unloadable model file is an error.
func NewSherpaDiarizer(segModel, embModel string) (*SherpaDiarizer, error) {
	for _, m := range []string{segModel, embModel} {
		if fi, err := os.Stat(m); err != nil || fi.Size() == 0 {
			return nil, fmt.Errorf("sherpa: model %s missing (run `journal setup`)", m)
		}
	}
	cfg := sherpa.OfflineSpeakerDiarizationConfig{
		Segmentation: sherpa.OfflineSpeakerSegmentationModelConfig{
			Pyannote:   sherpa.OfflineSpeakerSegmentationPyannoteModelConfig{Model: segModel},
			NumThreads: 2, Provider: "cpu",
		},
		Embedding:      sherpa.SpeakerEmbeddingExtractorConfig{Model: embModel, NumThreads: 2, Provider: "cpu"},
		Clustering:     sherpa.FastClusteringConfig{NumClusters: -1, Threshold: 0.7},
		MinDurationOn:  0.3,
		MinDurationOff: 0.5,
	}
	sd := sherpa.NewOfflineSpeakerDiarization(&cfg)
	if sd == nil {
		return nil, fmt.Errorf("sherpa: cannot load diarization models %s, %s", segModel, embModel)
	}
	ex := sherpa.NewSpeakerEmbeddingExtractor(&cfg.Embedding)
	if ex == nil {
		sherpa.DeleteOfflineSpeakerDiarization(sd)
		return nil, fmt.Errorf("sherpa: cannot load embedding model %s", embModel)
	}
	return &SherpaDiarizer{sd: sd, ex: ex}, nil
}

// Close releases the models. Diarize fails afterwards.
func (d *SherpaDiarizer) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sd != nil {
		sherpa.DeleteOfflineSpeakerDiarization(d.sd)
		d.sd = nil
	}
	if d.ex != nil {
		sherpa.DeleteSpeakerEmbeddingExtractor(d.ex)
		d.ex = nil
	}
}

// Diarize implements Diarizer for a 16 kHz mono 16-bit WAV. Each speaker's
// embedding comes from up to 30 s of its longest segments.
func (d *SherpaDiarizer) Diarize(ctx context.Context, wav string) ([]Turn, map[int][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	w := sherpa.ReadWave(wav)
	if w == nil {
		return nil, nil, fmt.Errorf("sherpa: cannot read %s", wav)
	}
	if w.SampleRate != sampleRate {
		return nil, nil, fmt.Errorf("sherpa: %s is %d Hz, want %d", wav, w.SampleRate, sampleRate)
	}
	if len(w.Samples) == 0 {
		return nil, nil, nil // Process panics on empty input
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sd == nil {
		return nil, nil, errors.New("sherpa: diarizer closed")
	}
	raw := d.sd.Process(w.Samples) // blocking; cannot be cancelled
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	segs := make([]segment, len(raw))
	for i, s := range raw {
		segs[i] = segment{start: float64(s.Start), end: float64(s.End), speaker: s.Speaker}
	}
	turns, clips := planTurns(segs, len(w.Samples), sampleRate, embedSeconds)
	embs := map[int][]float32{}
	for id, ranges := range clips {
		var audio []float32
		for _, r := range ranges {
			audio = append(audio, w.Samples[r[0]:r[1]]...)
		}
		st := d.ex.CreateStream()
		st.AcceptWaveform(sampleRate, audio)
		st.InputFinished()
		if d.ex.IsReady(st) {
			embs[id] = d.ex.Compute(st)
		}
		sherpa.DeleteOnlineStream(st)
	}
	if len(turns) > 0 && len(embs) == 0 {
		return nil, nil, errors.New("sherpa: no speaker embeddings computed")
	}
	return turns, embs, nil
}
