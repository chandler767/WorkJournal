// Package diarize splits system audio into speaker turns and matches speaker
// embeddings against locally stored voice profiles.
package diarize

import "context"

// Turn is a span of one speaker, in seconds from the start of the WAV.
// Speaker is a 0-based id local to one Diarize call.
type Turn struct {
	Start, End float64
	Speaker    int
}

// Diarizer finds speaker turns in a 16 kHz mono WAV and returns one
// embedding per local speaker id.
type Diarizer interface {
	Diarize(ctx context.Context, wav string) (turns []Turn, embeddings map[int][]float32, err error)
}

// NoopDiarizer finds no turns; callers label all system speech "others".
type NoopDiarizer struct{}

// Diarize implements Diarizer.
func (NoopDiarizer) Diarize(context.Context, string) ([]Turn, map[int][]float32, error) {
	return nil, nil, nil
}
