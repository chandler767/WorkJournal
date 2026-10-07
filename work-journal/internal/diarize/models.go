package diarize

import "path/filepath"

// Sherpa model artifacts (docs/spike-diarization.md). Download URLs are
// ReleaseBase + *RelURL; the upstream release tag really is spelled
// "recongition".
const (
	ReleaseBase         = "https://github.com/k2-fsa/sherpa-onnx/releases/download"
	SegmentationDir     = "sherpa-onnx-pyannote-segmentation-3-0"
	SegmentationArchive = SegmentationDir + ".tar.bz2"
	SegmentationRelURL  = "/speaker-segmentation-models/" + SegmentationArchive
	EmbeddingFile       = "nemo_en_titanet_small.onnx"
	EmbeddingRelURL     = "/speaker-recongition-models/" + EmbeddingFile
)

// ModelPaths returns the segmentation and embedding model files under the
// models directory.
func ModelPaths(models string) (segmentation, embedding string) {
	return filepath.Join(models, SegmentationDir, "model.onnx"), filepath.Join(models, EmbeddingFile)
}
