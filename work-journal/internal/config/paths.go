package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Paths resolves locations under the data directory.
type Paths struct{ Root string }

func (p Paths) Config() string { return filepath.Join(p.Root, "config.toml") }
func (p Paths) DB() string     { return filepath.Join(p.Root, "journal.db") }
func (p Paths) Socket() string { return filepath.Join(p.Root, "recorder.sock") }

func (p Paths) RawDay(day string) string   { return filepath.Join(p.Root, "raw", day) }
func (p Paths) Audio(day string) string    { return filepath.Join(p.RawDay(day), "audio") }
func (p Paths) Screens(day string) string  { return filepath.Join(p.RawDay(day), "screens") }
func (p Paths) Activity(day string) string { return filepath.Join(p.RawDay(day), "activity.jsonl") }

func (p Paths) DerivedHour(day string, hour int) string {
	return filepath.Join(p.Root, "derived", day, fmt.Sprintf("%02d.json", hour))
}

func (p Paths) DailyJournal(day string) string {
	return filepath.Join(p.Root, "journal", day+".md")
}

func (p Paths) WeeklyJournal(week string) string {
	return filepath.Join(p.Root, "journal", week+".md")
}

func (p Paths) Voices() string { return filepath.Join(p.Root, "voices") }
func (p Paths) Models() string { return filepath.Join(p.Root, "models") }
func (p Paths) Logs() string   { return filepath.Join(p.Root, "logs") }

// RecorderLog is where the recorder's stdout and stderr go.
func (p Paths) RecorderLog() string { return filepath.Join(p.Logs(), "recorder.log") }

// Ensure creates the root and the long-lived subdirectories.
func (p Paths) Ensure() error {
	for _, d := range []string{
		p.Root, p.Voices(), p.Models(), p.Logs(),
		filepath.Join(p.Root, "raw"), filepath.Join(p.Root, "derived"),
		filepath.Join(p.Root, "journal"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// DayKey is the local-time YYYY-MM-DD key.
func DayKey(t time.Time) string { return t.Format("2006-01-02") }

// WeekKey is the ISO week key, e.g. 2026-W41.
func WeekKey(t time.Time) string {
	y, w := t.ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, w)
}
