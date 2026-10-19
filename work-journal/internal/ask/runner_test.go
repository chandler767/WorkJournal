package ask

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"workjournal/internal/mcpserver"
	"workjournal/internal/skills"
)

type memKV map[string]string

func (m memKV) GetKV(k string) (string, bool, error) { v, ok := m[k]; return v, ok, nil }
func (m memKV) SetKV(k, v string) error              { m[k] = v; return nil }

type askCall struct {
	dir, bin string
	args     []string
	mcp      map[string]any
	dirFiles []string
	deadline time.Duration
}

func fakeAsk(t *testing.T, stdout string, err error, calls *[]askCall) func(ctx context.Context, dir, bin string, args []string) ([]byte, error) {
	return func(ctx context.Context, dir, bin string, args []string) ([]byte, error) {
		c := askCall{dir: dir, bin: bin, args: args}
		if dl, ok := ctx.Deadline(); ok {
			c.deadline = time.Until(dl)
		}
		if i := slices.Index(args, "--mcp-config"); i >= 0 && i+1 < len(args) {
			b, rerr := os.ReadFile(args[i+1])
			if rerr != nil {
				t.Fatalf("mcp config: %v", rerr)
			}
			if jerr := json.Unmarshal(b, &c.mcp); jerr != nil {
				t.Fatalf("mcp config JSON: %v", jerr)
			}
		}
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			c.dirFiles = append(c.dirFiles, e.Name())
		}
		*calls = append(*calls, c)
		return []byte(stdout), err
	}
}

func newAsk(t *testing.T, stdout string, err error, calls *[]askCall, kv memKV) *Runner {
	return &Runner{
		Self:       "/opt/wj/bin/journal",
		ConfigPath: "/Users/me/WorkJournal/config.toml",
		KV:         kv,
		Now:        func() time.Time { return time.Date(2026, 10, 19, 15, 4, 0, 0, time.Local) },
		Exec:       fakeAsk(t, stdout, err, calls),
	}
}

const okEnvelope = `{"type":"result","is_error":false,"result":"You decided to **ship on Friday** ([Standup](http://127.0.0.1:4747/meeting/x))."}`

func argAfter(args []string, flag string) (string, bool) {
	i := slices.Index(args, flag)
	if i < 0 || i+1 >= len(args) {
		return "", false
	}
	return args[i+1], true
}

func TestAskRunsClaudeWithOnlyJournalTools(t *testing.T) {
	var calls []askCall
	r := newAsk(t, okEnvelope, nil, &calls, memKV{})
	ans, err := r.Ask(context.Background(), "What did we decide in standup?", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ans, "ship on Friday") {
		t.Fatalf("answer %q", ans)
	}
	if len(calls) != 1 {
		t.Fatalf("%d calls", len(calls))
	}
	c := calls[0]
	if c.bin != "claude" {
		t.Errorf("bin %q", c.bin)
	}
	if got, ok := argAfter(c.args, "--tools"); !ok || got != "" {
		t.Errorf("built-in tools not disabled: --tools %q (%v)", got, ok)
	}
	if !slices.Contains(c.args, "--strict-mcp-config") {
		t.Error("no --strict-mcp-config")
	}
	allowed, _ := argAfter(c.args, "--allowedTools")
	var want []string
	for _, n := range mcpserver.ToolNames {
		want = append(want, "mcp__work-journal__"+n)
	}
	if allowed != strings.Join(want, ",") {
		t.Errorf("--allowedTools %q, want %q", allowed, strings.Join(want, ","))
	}
	for _, bad := range []string{"--add-dir", "--dangerously-skip-permissions", "Read", "Bash", "Skill"} {
		for _, a := range c.args {
			if a == bad || strings.Contains(allowed, bad) {
				t.Errorf("unexpected %q in args %q", bad, c.args)
			}
		}
	}
	if mode, _ := argAfter(c.args, "--permission-mode"); mode != "dontAsk" {
		t.Errorf("--permission-mode %q", mode)
	}

	// The generated MCP config launches this binary's mcp subcommand with
	// the same config, and nothing else.
	servers, _ := c.mcp["mcpServers"].(map[string]any)
	if len(servers) != 1 {
		t.Fatalf("mcp config %v", c.mcp)
	}
	wj, _ := servers["work-journal"].(map[string]any)
	if wj["command"] != "/opt/wj/bin/journal" || strings.Join(toStrings(wj["args"]), " ") != "mcp --config /Users/me/WorkJournal/config.toml" {
		t.Errorf("mcp server %v", wj)
	}

	// cwd is a fresh empty directory outside the repo, removed afterwards.
	if len(c.dirFiles) != 0 {
		t.Errorf("cwd not empty: %v", c.dirFiles)
	}
	repo, _ := filepath.Abs(filepath.Join("..", "..", ".."))
	if c.dir == "" || strings.HasPrefix(c.dir, repo) {
		t.Errorf("cwd %q is not a neutral directory (repo %s)", c.dir, repo)
	}
	for _, a := range c.args {
		if strings.Contains(a, repo) {
			t.Errorf("arg names the repo: %q", a)
		}
	}
	if _, err := os.Stat(c.dir); !os.IsNotExist(err) {
		t.Errorf("cwd %s left behind (%v)", c.dir, err)
	}
	cfg, _ := argAfter(c.args, "--mcp-config")
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Errorf("mcp config %s left behind", cfg)
	}

	// System prompt: journal-only, cite links, admit uncertainty, today's date and zone.
	sys, _ := argAfter(c.args, "--append-system-prompt")
	zone, _ := time.Date(2026, 10, 19, 15, 4, 0, 0, time.Local).Zone()
	for _, s := range []string{"only", "work-journal", "link", "unsure", "2026-10-19", "Monday", zone} {
		if !strings.Contains(sys, s) {
			t.Errorf("system prompt lacks %q: %s", s, sys)
		}
	}
	if c.deadline <= 2*time.Minute || c.deadline > 3*time.Minute {
		t.Errorf("deadline %v, want 3 min", c.deadline)
	}
}

func toStrings(v any) []string {
	var out []string
	if l, ok := v.([]any); ok {
		for _, x := range l {
			s, _ := x.(string)
			out = append(out, s)
		}
	}
	return out
}

func TestAskQuestionCannotBecomeAFlag(t *testing.T) {
	var calls []askCall
	r := newAsk(t, okEnvelope, nil, &calls, memKV{})
	if _, err := r.Ask(context.Background(), "--dangerously-skip-permissions", false); err != nil {
		t.Fatal(err)
	}
	for _, a := range calls[0].args {
		if a == "--dangerously-skip-permissions" {
			t.Fatalf("question passed as a flag: %q", calls[0].args)
		}
	}
	p, _ := argAfter(calls[0].args, "-p")
	if !strings.Contains(p, "--dangerously-skip-permissions") || strings.HasPrefix(p, "-") {
		t.Fatalf("prompt %q", p)
	}
}

func TestAskErrors(t *testing.T) {
	var calls []askCall
	r := newAsk(t, `{"type":"result","is_error":true,"result":"boom"}`, nil, &calls, memKV{})
	if _, err := r.Ask(context.Background(), "q", false); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err %v", err)
	}
	r = newAsk(t, `not json`, nil, &calls, memKV{})
	if _, err := r.Ask(context.Background(), "q", false); err == nil {
		t.Fatal("unparseable output accepted")
	}
	r = newAsk(t, `{"type":"result","is_error":false,"result":"  "}`, nil, &calls, memKV{})
	if _, err := r.Ask(context.Background(), "q", false); err == nil {
		t.Fatal("empty answer accepted")
	}
}

func TestAskUsageLimitSetsPauseAndPauseGates(t *testing.T) {
	kv := memKV{}
	var calls []askCall
	r := newAsk(t, `{"type":"result","is_error":true,"result":"Claude usage limit reached|1760900000"}`, nil, &calls, kv)
	now := time.Unix(1760890000, 0)
	r.Now = func() time.Time { return now }
	_, err := r.Ask(context.Background(), "q", false)
	var ul skills.ErrUsageLimited
	if !errors.As(err, &ul) {
		t.Fatalf("err %v", err)
	}
	if st, _ := skills.ReadPause(kv, now); !st.Active {
		t.Fatal("pause not stored")
	}
	// While paused: no claude run.
	r.Exec = fakeAsk(t, okEnvelope, nil, &calls)
	n := len(calls)
	if _, err := r.Ask(context.Background(), "q", false); !errors.As(err, &ul) {
		t.Fatalf("paused ask: %v", err)
	}
	if len(calls) != n {
		t.Fatal("claude ran while paused")
	}
	// Ask anyway: runs once, the pause stays for everything else.
	if ans, err := r.Ask(context.Background(), "q", true); err != nil || ans == "" {
		t.Fatalf("bypass: %q %v", ans, err)
	}
	if len(calls) != n+1 {
		t.Fatal("bypass did not run claude")
	}
	if st, _ := skills.ReadPause(kv, now); !st.Active {
		t.Fatal("bypass cleared the pause")
	}
	// Pausing off: never gated.
	r.PauseEnabled = func() bool { return false }
	if _, err := r.Ask(context.Background(), "q", false); err != nil {
		t.Fatalf("pause off: %v", err)
	}
}
