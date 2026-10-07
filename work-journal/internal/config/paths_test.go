package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPaths(t *testing.T) {
	p := Paths{Root: "/data"}
	if got := p.DerivedHour("2026-10-07", 9); !strings.HasSuffix(got, "derived/2026-10-07/09.json") {
		t.Fatalf("got %s", got)
	}
	if got := p.DailyJournal("2026-10-07"); !strings.HasSuffix(got, "journal/2026-10-07.md") {
		t.Fatalf("got %s", got)
	}
	if got := p.WeeklyJournal("2026-W41"); !strings.HasSuffix(got, "journal/2026-W41.md") {
		t.Fatalf("got %s", got)
	}
	if got := p.Socket(); got != "/data/recorder.sock" {
		t.Fatalf("got %s", got)
	}
	if got := p.Audio("2026-10-07"); got != "/data/raw/2026-10-07/audio" {
		t.Fatalf("got %s", got)
	}
	if got := p.Activity("2026-10-07"); got != "/data/raw/2026-10-07/activity.jsonl" {
		t.Fatalf("got %s", got)
	}
}

func TestKeys(t *testing.T) {
	loc := time.Local
	if got := DayKey(time.Date(2026, 10, 7, 23, 59, 0, 0, loc)); got != "2026-10-07" {
		t.Fatal(got)
	}
	if got := WeekKey(time.Date(2026, 10, 7, 12, 0, 0, 0, loc)); got != "2026-W41" {
		t.Fatal(got)
	}
	if got := WeekKey(time.Date(2027, 1, 1, 12, 0, 0, 0, loc)); got != "2026-W53" {
		t.Fatal(got)
	}
}

func TestEnsure(t *testing.T) {
	p := Paths{Root: filepath.Join(t.TempDir(), "WorkJournal")}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	if p.Logs() != filepath.Join(p.Root, "logs") {
		t.Fatalf("logs = %s", p.Logs())
	}
	for _, d := range []string{p.Root, p.Voices(), p.Models(), p.Logs()} {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			t.Fatalf("missing %s", d)
		}
	}
}
