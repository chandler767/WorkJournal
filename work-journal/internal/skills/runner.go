// Package skills runs the work-journal Claude Code skills headlessly with
// `claude -p` and validates their JSON output against embedded schemas.
package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"workjournal/internal/media"
)

// ErrInvalidOutput means a skill twice returned output that is not a JSON
// object matching its schema (or failing the output's Validator).
var ErrInvalidOutput = errors.New("invalid skill output")

// Validator is implemented by outputs with checks beyond the schema (for
// example, categories limited to the configured list). A Validate error is
// treated like a schema violation: retry once, then ErrInvalidOutput.
type Validator interface{ Validate() error }

// Runner runs one skill on an input file and decodes its output into out.
type Runner interface {
	Run(ctx context.Context, skill, inputPath string, out any) error
}

// DefaultTimeout bounds one claude invocation when ClaudeRunner.Timeout is 0.
const DefaultTimeout = 5 * time.Minute

// installedPrefix is how `tessl install` names the plugin's skills under
// .claude/skills; the prompt names the installed skill.
const installedPrefix = "tessl__"

// ClaudeRunner runs skills through the claude CLI. It only ever grants the
// Read tool, plus read access to DataDir where the input files live.
type ClaudeRunner struct {
	Bin     string // claude executable; "" means "claude"
	WorkDir string // directory whose .claude/skills holds the installed skills
	DataDir string // passed to --add-dir so Claude can read input files
	// Exec overrides process execution (tests). When nil, the command runs
	// in WorkDir and is killed when the context ends.
	Exec    media.Runner
	Timeout time.Duration // per attempt; 0 means DefaultTimeout
}

// resultEnvelope is the `--output-format json` result object.
type resultEnvelope struct {
	Type    string `json:"type"`
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
}

// Run invokes the skill, retrying once when the output is invalid. Exec
// failures and is_error envelopes are returned as plain errors (the caller
// may retry later); two invalid outputs yield ErrInvalidOutput.
func (r *ClaudeRunner) Run(ctx context.Context, skill, inputPath string, out any) error {
	sch, err := schema(skill)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		stdout, err := r.exec(ctx, skill, inputPath)
		if err != nil {
			return fmt.Errorf("skill %s: %w", skill, err)
		}
		var env resultEnvelope
		if err := json.Unmarshal(stdout, &env); err != nil {
			lastErr = fmt.Errorf("unparseable claude output: %v", err)
			continue
		}
		if env.IsError {
			return fmt.Errorf("skill %s: claude reported an error: %s", skill, env.Result)
		}
		if lastErr = decode(sch, env.Result, out); lastErr == nil {
			return nil
		}
	}
	return fmt.Errorf("%w: %s: %v", ErrInvalidOutput, skill, lastErr)
}

// decode extracts, validates and unmarshals a skill result into out.
func decode(sch *jsonschema.Schema, result string, out any) error {
	raw, err := ExtractJSON(result)
	if err != nil {
		return err
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if err := sch.Validate(inst); err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return err
	}
	if v, ok := out.(Validator); ok {
		return v.Validate()
	}
	return nil
}

func (r *ClaudeRunner) exec(ctx context.Context, skill, inputPath string) ([]byte, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	bin := r.Bin
	if bin == "" {
		bin = "claude"
	}
	args := r.args(skill, inputPath)
	if r.Exec != nil {
		return r.Exec(ctx, bin, args...)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = r.WorkDir
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s: %w", bin, ctx.Err())
		}
		return nil, fmt.Errorf("%s: %w: %s", bin, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// args never grants tools beyond Read and never bypasses permissions.
func (r *ClaudeRunner) args(skill, inputPath string) []string {
	prompt := fmt.Sprintf("Use the %s%s skill. Input file: %s. Reply with only the JSON object.", installedPrefix, skill, inputPath)
	return []string{"-p", prompt, "--output-format", "json", "--allowedTools", "Read", "--add-dir", r.DataDir}
}

// ExtractJSON returns the first ```json fenced block in result, or else the
// first balanced {...} object (string- and escape-aware).
func ExtractJSON(result string) ([]byte, error) {
	if i := strings.Index(result, "```json"); i >= 0 {
		body := result[i+len("```json"):]
		if j := strings.Index(body, "```"); j >= 0 {
			if obj := strings.TrimSpace(body[:j]); strings.HasPrefix(obj, "{") {
				return []byte(obj), nil
			}
		}
	}
	start := strings.IndexByte(result, '{')
	if start < 0 {
		return nil, errors.New("no JSON object in result")
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(result); i++ {
		c := result[i]
		switch {
		case esc:
			esc = false
		case inStr && c == '\\':
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '{':
			depth++
		case c == '}':
			if depth--; depth == 0 {
				return []byte(result[start : i+1]), nil
			}
		}
	}
	return nil, errors.New("unbalanced JSON object in result")
}
