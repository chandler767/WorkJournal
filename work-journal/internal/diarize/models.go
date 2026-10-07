package diarize

import "path/filepath"

// Sherpa model artifacts (docs/spike-diarization.md). The upstream release
// tag really is spelled "recongition".
const (
	SegmentationDir     = "sherpa-onnx-pyannote-segmentation-3-0"
	SegmentationArchive = SegmentationDir + ".tar.bz2"
	SegmentationURL     = "https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-segmentation-models/" + SegmentationArchive
	EmbeddingFile       = "nemo_en_titanet_small.onnx"
	EmbeddingURL        = "https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-recongition-models/" + EmbeddingFile
)

// ModelPaths returns the segmentation and embedding model files under the
// models directory.
func ModelPaths(models string) (segmentation, embedding string) {
	return filepath.Join(models, SegmentationDir, "model.onnx"), filepath.Join(models, EmbeddingFile)
}
