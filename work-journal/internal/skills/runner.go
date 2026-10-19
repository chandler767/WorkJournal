// Package skills runs the work-journal Claude Code skills headlessly with
// `claude -p` and validates their JSON output against embedded schemas.
package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"workjournal/internal/media"
)

// ErrInvalidOutput means claude returned a result, but not a JSON object
// matching the skill's schema (or passing the output's Validator), and the
// retry did not fix it.
var ErrInvalidOutput = errors.New("invalid skill output")

// ErrUnavailable means claude produced no usable result twice (non-zero exit,
// an is_error envelope, a per-run timeout or an unparseable envelope). It is
// transient: the caller retries later (Ruling 34). ErrUsageLimited (after
// one attempt) also matches it.
var ErrUnavailable = errors.New("claude unavailable")

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

// ClaudeRunner runs skills through the claude CLI. The session has only the
// Skill and Read tools, no MCP servers, no user settings or hooks, and read
// access to DataDir.
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

// Run invokes the skill with up to two attempts. An attempt fails on a
// non-zero exit, a per-attempt timeout, an is_error or unparseable envelope
// (no usable result), or a result that is not a schema-valid JSON object
// (invalid output). A run claude refuses for a usage or rate limit returns
// ErrUsageLimited at once, without a second attempt. When both attempts fail
// and either produced invalid output, Run returns ErrInvalidOutput (the hour
// goes to review); otherwise ErrUnavailable (transient; retried later,
// Ruling 34). A missing claude
// binary or an ended ctx returns a plain transient error at once.
func (r *ClaudeRunner) Run(ctx context.Context, skill, inputPath string, out any) error {
	sch, err := schema(skill)
	if err != nil {
		return err
	}
	var lastErr error
	invalid := false
	for attempt := 0; attempt < 2; attempt++ {
		stdout, err := r.exec(ctx, skill, inputPath)
		var env resultEnvelope
		envErr := json.Unmarshal(stdout, &env)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("skill %s: %w", skill, err)
			}
			// A non-zero exit may still print the error envelope.
			msg := err.Error()
			if envErr == nil && env.IsError {
				msg = env.Result + "\n" + msg
			}
			if ul, ok := ParseUsageLimit(msg, time.Now()); ok {
				return fmt.Errorf("skill %s: %w", skill, ul)
			}
			lastErr = err // non-zero exit or per-attempt timeout
			continue
		}
		if envErr != nil {
			lastErr = fmt.Errorf("unparseable claude output: %v", envErr)
			continue
		}
		if env.IsError {
			if ul, ok := ParseUsageLimit(env.Result, time.Now()); ok {
				return fmt.Errorf("skill %s: %w", skill, ul)
			}
			lastErr = fmt.Errorf("claude reported an error: %s", env.Result)
			continue
		}
		if lastErr = decode(sch, env.Result, out); lastErr == nil {
			return nil
		}
		invalid = true
	}
	if invalid {
		return fmt.Errorf("%w: %s: %v", ErrInvalidOutput, skill, lastErr)
	}
	return fmt.Errorf("%w: %s: %v", ErrUnavailable, skill, lastErr)
}

// resultSnippetMax is how much of a result without JSON an error quotes.
const resultSnippetMax = 300

// decode extracts, validates and unmarshals a skill result into out. A
// result without a JSON object (Claude answered in prose, for example that
// it could not read the input) is quoted, on one line, in the error.
func decode(sch *jsonschema.Schema, result string, out any) error {
	raw, err := ExtractJSON(result)
	if err != nil {
		return fmt.Errorf("%w; result began: %s", err, snippet(result, resultSnippetMax))
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

// snippet is s with whitespace runs collapsed to one space, cut to max runes.
func snippet(s string, max int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return string(r)
}

// InputLineBudget is the longest compact JSON value MarshalInput keeps on
// one line; longer arrays and objects are spread one element per line.
// Claude's Read tool pages by line and cuts lines over 2000 characters.
const InputLineBudget = 1000

// MarshalInput encodes a skill input so Claude's Read tool, which pages by
// line, can read an input of any size: arrays and objects whose compact
// form is over InputLineBudget have one element (member) per line, nested as
// deep as needed; shorter values, and every string, stay on one line. HTML
// characters are not escaped.
func MarshalInput(v any) ([]byte, error) {
	raw, err := compactJSON(v)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := layoutJSON(&b, raw, ""); err != nil {
		return nil, err
	}
	b.WriteByte('\n')
	return b.Bytes(), nil
}

func compactJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

func layoutJSON(b *bytes.Buffer, raw []byte, indent string) error {
	if len(raw) <= InputLineBudget || (raw[0] != '[' && raw[0] != '{') {
		b.Write(raw)
		return nil
	}
	inner := indent + " "
	if raw[0] == '[' {
		var elems []json.RawMessage
		if err := json.Unmarshal(raw, &elems); err != nil {
			return err
		}
		b.WriteString("[\n")
		for i, e := range elems {
			b.WriteString(inner)
			if err := layoutJSON(b, e, inner); err != nil {
				return err
			}
			if i < len(elems)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + "]")
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil { // {
		return err
	}
	b.WriteString("{\n")
	first := true
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return err
		}
		key, err := compactJSON(tok)
		if err != nil {
			return err
		}
		if !first {
			b.WriteString(",\n")
		}
		first = false
		b.WriteString(inner)
		b.Write(key)
		b.WriteString(": ")
		if err := layoutJSON(b, v, inner); err != nil {
			return err
		}
	}
	b.WriteString("\n" + indent + "}")
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
		// stdout too: claude prints its error envelope before exiting non-zero.
		return stdout.Bytes(), fmt.Errorf("%s: %w: %s", bin, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// args makes only Skill (needed to load the skill) and Read available,
// pre-approves Read, loads no MCP servers and never bypasses permissions.
// Only project settings load (they make .claude/skills available): user
// settings, and so user hooks that would see the journal text, do not; the
// session is not saved.
func (r *ClaudeRunner) args(skill, inputPath string) []string {
	prompt := fmt.Sprintf("Use the %s%s skill. Input file: %s (one record per line). If the file is long, read it in parts (offset/limit) until you have read all of it, then answer. Reply with only the JSON object.", installedPrefix, skill, inputPath)
	return []string{"-p", prompt, "--output-format", "json",
		"--tools", "Skill,Read", // the only tools that exist in the session
		"--allowedTools", "Read", // pre-approved, so headless reads work
		"--strict-mcp-config", // no MCP servers (none passed via --mcp-config)
		"--setting-sources", "project",
		"--no-session-persistence",
		"--add-dir", r.DataDir}
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
