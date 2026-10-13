package media

import (
	"context"
	"os/exec"
	"strings"
)

// FileType returns the MIME type reported by file(1).
func FileType(ctx context.Context, path string) (string, error) {
	out, err := exec.CommandContext(ctx, "file", "--brief", "--mime-type", path).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
