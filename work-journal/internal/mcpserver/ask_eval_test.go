//go:build ask

package mcpserver_test

// Ask eval: the real claude CLI answers questions over the fixture day
// through a freshly built `journal mcp` (go test -tags=ask -run TestAskEval
// ./internal/mcpserver/). It needs a logged-in claude and takes minutes.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workjournal/internal/ask"
	"workjournal/internal/mcpserver"
)

func TestAskEval(t *testing.T) {
	tmp := t.TempDir()
	data := filepath.Join(tmp, "data")
	mcpserver.SeedFixture(t, data)
	cfg := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(cfg, []byte("data_dir = \""+data+"\"\nlisten_addr = \"127.0.0.1:4747\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(tmp, "journal")
	build := exec.Command("go", "build", "-o", bin, "./cmd/journal")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build journal: %v\n%s", err, out)
	}
	r := &ask.Runner{Self: bin, ConfigPath: cfg, ListenAddr: "127.0.0.1:4747"}
	cases := []struct {
		q    string
		want [][]string // each group: at least one must appear (any case)
	}{
		{"What did we decide in the Standup meeting on 2026-10-07?",
			[][]string{{"friday"}, {"127.0.0.1:4747/meeting/2026-10-07-0910-zoom"}}},
		{"How many minutes was I in meetings on 2026-10-07?",
			[][]string{{"20 min", "20 minutes", "**20**", " 20 "}}},
		{"What was I doing at 09:05 on 2026-10-07?",
			[][]string{{"scheduler"}, {"retry"}, {"127.0.0.1:4747/hour/2026-10-07/09"}}},
	}
	for _, c := range cases {
		began := time.Now()
		ans, err := r.Ask(context.Background(), c.q, false)
		t.Logf("Q: %s (%s)\nA: %s", c.q, time.Since(began).Round(time.Second), ans)
		if err != nil {
			t.Errorf("%s: %v", c.q, err)
			continue
		}
		low := strings.ToLower(ans)
		for _, group := range c.want {
			found := false
			for _, w := range group {
				if strings.Contains(low, strings.ToLower(w)) {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: answer lacks any of %q", c.q, group)
			}
		}
		for _, s := range []string{"CACHESECRET", "WITHHELDSECRET", "PRIVACYSECRET", "VOICESECRET", data} {
			if strings.Contains(ans, s) {
				t.Errorf("%s: answer leaks %q", c.q, s)
			}
		}
	}
}
