package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"workjournal/internal/config"
	"workjournal/internal/process"
	"workjournal/internal/store"
)

func TestInterspersedFlags(t *testing.T) {
	for _, c := range []struct{ in, want []string }{
		{[]string{"memo.m4a", "--start", "T", "--title", "x y"}, []string{"--start", "T", "--title", "x y", "memo.m4a"}},
		{[]string{"--start=T", "memo.m4a"}, []string{"--start=T", "memo.m4a"}},
		{[]string{"--rebuild"}, []string{"--rebuild"}},
		{[]string{"on"}, []string{"on"}},
		{[]string{"a.m4a", "--", "-b.m4a"}, []string{"--", "-b.m4a", "a.m4a"}},
	} {
		if got := interspersed(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("interspersed(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestImportCommandArguments(t *testing.T) {
	cfgPath := writeTestConfig(t, t.TempDir())
	var out, errb bytes.Buffer
	for _, args := range [][]string{
		{"import", "--config", cfgPath, "memo.m4a"},                              // no --start
		{"import", "--config", cfgPath, "--start", "2026-10-07T18:30:00-04:00"},  // no file
		{"import", "--config", cfgPath, "memo.m4a", "--start", "yesterday 6pm"},  // not RFC 3339
		{"process", "--config", cfgPath, "--start", "2026-10-07T18:30:00-04:00"}, // --start is import's
	} {
		errb.Reset()
		if code := run(args, &out, &errb); code != 2 {
			t.Errorf("%q: code %d, stderr %s", args, code, errb.String())
		}
	}
}

// TestRunImportStoresAndProcesses runs `journal import` with afconvert
// failing (no macOS here): the recording is stored as an imported meeting,
// processing fails and is reported, and the file is kept.
func TestRunImportStoresAndProcesses(t *testing.T) {
	a := newTestApp(t)
	src := filepath.Join(t.TempDir(), "memo.m4a")
	if err := os.WriteFile(src, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 7, 18, 30, 0, 0, time.FixedZone("EDT", -4*3600))
	var out bytes.Buffer
	cfg := a.Cfg()
	a.Close()
	err := runImport(t.Context(), cfg, a.ConfigPath, a.Loc, process.ImportRequest{Path: src, Start: start, Title: "Meetup", Context: "note"}, &out)
	if err == nil {
		t.Fatal("processing without afconvert succeeded")
	}
	if !strings.Contains(out.String(), "as meeting 2026-10-07-1830-imported") {
		t.Errorf("stdout: %s", out.String())
	}
	st, err := store.Open(config.Paths{Root: cfg.DataDir}.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m, ok, err := st.Meeting("2026-10-07-1830-imported")
	if err != nil || !ok || !m.Imported || m.Title != "Meetup" || m.ImportError == "" {
		t.Fatalf("meeting = %+v %v %v", m, ok, err)
	}
	if _, err := os.Stat(filepath.Join(config.Paths{Root: cfg.DataDir}.Import(m.ID), "audio.m4a")); err != nil {
		t.Errorf("recording not kept: %v", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("source file removed: %v", err)
	}
}
