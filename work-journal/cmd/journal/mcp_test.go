package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"workjournal/internal/ask"
	"workjournal/internal/config"
	"workjournal/internal/store"
)

func writeTestConfig(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("data_dir = \""+data+"\"\nlisten_addr = \"127.0.0.1:4999\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMCPCommandServesTheConfiguredJournal(t *testing.T) {
	data := t.TempDir()
	p := config.Paths{Root: data}
	st, err := store.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceText("2026-10-07", 9, []store.TextDoc{{Kind: "note", TS: time.Now(), Text: "deploy the widget", Ref: "hour/2026-10-07/09"}}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	cfgPath := writeTestConfig(t, data)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ct, srv := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- runMCP(ctx, cfgPath, srv) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "search", Arguments: map[string]any{"query": "widget"}})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "http://127.0.0.1:4999/hour/2026-10-07/09") {
		t.Fatalf("search result:\n%s", text)
	}
	cs.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runMCP did not return after the client closed")
	}
}

func TestMCPCommandNeedsNoSourceTree(t *testing.T) {
	// `journal mcp` runs from Claude clients anywhere; a missing database
	// is reported, not a missing go.mod.
	cfgPath := writeTestConfig(t, t.TempDir())
	var out, errb bytes.Buffer
	if code := run([]string{"mcp", "--config", cfgPath, "--repo", t.TempDir()}, &out, &errb); code != 2 {
		t.Fatalf("--repo accepted for mcp (code %d)", code)
	}
	errb.Reset()
	if code := run([]string{"mcp", "--config", cfgPath}, &out, &errb); code != 1 {
		t.Fatalf("code %d, stderr %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "journal serve") || strings.Contains(errb.String(), "go.mod") {
		t.Fatalf("stderr: %s", errb.String())
	}
	if out.Len() != 0 {
		t.Fatalf("wrote to stdout (the MCP channel): %q", out.String())
	}
}

// The dashboard's Ask runs this binary's mcp subcommand with serve's config.
func TestBuildWiresAsk(t *testing.T) {
	a := newTestApp(t)
	r, ok := a.Server.Ask.(*ask.Runner)
	if !ok || r == nil {
		t.Fatalf("Server.Ask = %#v", a.Server.Ask)
	}
	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	if r.Self != exe || !filepath.IsAbs(r.ConfigPath) || r.ConfigPath != a.ConfigPath || r.KV == nil || r.ListenAddr != a.Cfg().ListenAddr {
		t.Fatalf("ask runner = %+v (exe %s)", r, exe)
	}
	if !r.PauseEnabled() {
		t.Error("ask ignores the usage pause")
	}
	cfg := a.Cfg()
	cfg.PauseOnUsageLimit = false
	a.cfg.set(cfg)
	if r.PauseEnabled() {
		t.Error("ask ignores pause_on_usage_limit")
	}
}
