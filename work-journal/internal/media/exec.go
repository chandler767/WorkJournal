// Package media wraps the external tools used by the hourly pipeline:
// afconvert, whisper-cli and the recorder's OCR subcommand.
package media

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Runner runs an external command and returns its stdout.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner is the default Runner. Errors include the command name and
// trimmed stderr.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
