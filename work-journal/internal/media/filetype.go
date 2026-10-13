package media

import (
	"context"
	"strings"
)

// FileType returns the MIME type reported by file(1). It goes through run so tests can stub the tool.
func FileType(ctx context.Context, run Runner, path string) (string, error) {
	out, err := run(ctx, "file", "--brief", "--mime-type", path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
