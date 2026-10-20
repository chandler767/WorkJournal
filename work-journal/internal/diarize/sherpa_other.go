//go:build !darwin

package diarize

import (
	"context"
	"errors"
)

// errNeedsMacOS is returned on platforms without the sherpa-onnx binding.
var errNeedsMacOS = errors.New("sherpa: speaker separation needs macOS")

// SherpaDiarizer stands in for the macOS diarizer in sherpa.go so the rest of
// the app builds and tests on other platforms. It cannot be created.
type SherpaDiarizer struct{}

var _ Diarizer = (*SherpaDiarizer)(nil)

// NewSherpaDiarizer always fails: the sherpa-onnx binding is macOS only.
func NewSherpaDiarizer(segModel, embModel string) (*SherpaDiarizer, error) {
	return nil, errNeedsMacOS
}

// Close does nothing.
func (d *SherpaDiarizer) Close() {}

// Diarize always fails.
func (d *SherpaDiarizer) Diarize(context.Context, string) ([]Turn, map[int][]float32, error) {
	return nil, nil, errNeedsMacOS
}
