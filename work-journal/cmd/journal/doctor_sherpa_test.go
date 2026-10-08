//go:build sherpa

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"workjournal/internal/diarize"
)

// The doctor's speaker check runs the real recording path with the real
// models ($WORKJOURNAL_MODELS, else ~/WorkJournal/models; read only).
func TestSpeakerSeparationWorksForReal(t *testing.T) {
	models := os.Getenv("WORKJOURNAL_MODELS")
	if models == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip(err)
		}
		models = filepath.Join(home, "WorkJournal", "models")
	}
	seg, emb := diarize.ModelPaths(models)
	for _, m := range []string{seg, emb} {
		if _, err := os.Stat(m); err != nil {
			t.Skipf("model missing: %v", err)
		}
	}
	for _, tool := range []string{"say", "afconvert"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	if err := speakerSeparationWorks(seg, emb); err != nil {
		t.Fatal(err)
	}
}
