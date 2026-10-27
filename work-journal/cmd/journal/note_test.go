package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workjournal/internal/config"
	"workjournal/internal/store"
)

func TestRunNoteAddAndList(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("data_dir = \""+filepath.Join(dir, "data")+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := filepath.Join(dir, "plan.md")
	os.WriteFile(doc, []byte("# Cloudflare planning\nQ4 goals"), 0o600)
	var out, errb bytes.Buffer
	run := func(args ...string) int {
		out.Reset()
		errb.Reset()
		return run(append(args, "--config", cfgPath, "--repo", dir), &out, &errb)
	}
	if code := run("note", "add", "--date", "2026-10-07", "--kind", "correction", "--from", "13:10", "--to", "13:20",
		"--text", "people next to me, not me; disregard it"); code != 0 {
		t.Fatalf("add: %d %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "saved correction note 1 on 2026-10-07 (13:10–13:20)") || !strings.Contains(out.String(), "raw recordings were already removed") {
		t.Errorf("add output: %s", out.String())
	}
	if code := run("note", "add", "--date", "2026-10-07", "--kind", "context", "--file", doc); code != 0 {
		t.Fatalf("add file: %d %s", code, errb.String())
	}
	if code := run("note", "add", "--date", "2026-10-07", "--kind", "context", "--file", doc, "--text", "x"); code != 1 {
		t.Errorf("--file and --text: %d", code)
	}
	if code := run("note", "add", "--date", "2026-10-07", "--kind", "memo", "--text", "x"); code != 1 || !strings.Contains(errb.String(), "kind") {
		t.Errorf("bad kind: %d %s", code, errb.String())
	}
	if code := run("note", "list", "--date", "2026-10-07"); code != 0 || !strings.Contains(out.String(), "Q4 goals") || !strings.Contains(out.String(), "(cli)") {
		t.Errorf("list: %d %s %s", code, out.String(), errb.String())
	}
	st, err := store.Open(config.Paths{Root: filepath.Join(dir, "data")}.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if q, _ := st.NoteQueue(); len(q) != 2 || q[0].Hour != 13 || q[0].Redo != store.RedoPrivacy {
		t.Errorf("queue %+v", q)
	}
}
