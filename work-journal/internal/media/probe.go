package media

import (
	"context"
	"strings"
)

// AudioDuration returns the duration line reported by afinfo for an audio file. It goes through run so tests can stub the tool.
func AudioDuration(ctx context.Context, run Runner, path string) (string, error) {
	out, err := run(ctx, "afinfo", path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
