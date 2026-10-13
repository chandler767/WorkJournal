package media

import (
	"context"
	"strings"
)

// WhisperVersion returns the version string reported by whisper-cli. It goes through run so tests can stub the tool.
func WhisperVersion(ctx context.Context, run Runner) (string, error) {
	out, err := run(ctx, "whisper-cli", "--version")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
