package media

import (
	"context"
	"os/exec"
	"strings"
)

// AudioDuration returns the duration line reported by afinfo for an audio file.
func AudioDuration(ctx context.Context, path string) (string, error) {
	out, err := exec.CommandContext(ctx, "afinfo", path).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
