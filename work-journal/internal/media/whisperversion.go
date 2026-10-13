package media

import (
	"context"
	"os/exec"
	"strings"
)

// WhisperVersion returns the version string reported by whisper-cli.
func WhisperVersion(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "whisper-cli", "--version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
