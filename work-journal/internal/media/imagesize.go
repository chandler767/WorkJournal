package media

import (
	"context"
	"strings"
)

// ImageSize returns the pixel width and height reported by sips for an image. It goes through run so tests can stub the tool.
func ImageSize(ctx context.Context, run Runner, path string) (string, error) {
	out, err := run(ctx, "sips", "-g", "pixelWidth", "-g", "pixelHeight", path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
