package media

import (
	"context"
	"os/exec"
	"strings"
)

// ImageSize returns the pixel width and height reported by sips for an image.
func ImageSize(ctx context.Context, path string) (string, error) {
	out, err := exec.CommandContext(ctx, "sips", "-g", "pixelWidth", "-g", "pixelHeight", path).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
