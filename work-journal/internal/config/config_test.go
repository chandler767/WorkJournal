package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaults(t *testing.T) {
	c := Default()
	if c.RetentionDays != 7 || c.CloseTime != "18:00" || c.ListenAddr != "127.0.0.1:4747" {
		t.Fatalf("bad defaults: %+v", c)
	}
	if len(c.Categories) != 6 {
		t.Fatalf("categories = %v", c.Categories)
	}
	found := false
	for _, a := range c.ExcludedApps {
		if a == "1Password" {
			found = true
		}
	}
	if !found {
		t.Fatalf("1Password not excluded: %v", c.ExcludedApps)
	}
	if c.Diarization != "off" {
		t.Fatalf("diarization = %q", c.Diarization)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c, Default()) {
		t.Fatalf("got %+v", c)
	}
}

func TestLoadPartialFileKeepsDefaults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("retention_days = 14\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.RetentionDays != 14 || c.CloseTime != "18:00" {
		t.Fatalf("got %+v", c)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.toml")
	c := Default()
	c.RetentionDays = 3
	c.CloseTime = "17:30"
	c.Rules = map[string]string{"Xcode": "Deep work"}
	c.AllDisplays = true
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, c) {
		t.Fatalf("round trip mismatch:\n%+v\n%+v", got, c)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*Config)
		want string
	}{
		{"retention", func(c *Config) { c.RetentionDays = 0 }, "retention_days"},
		{"close time", func(c *Config) { c.CloseTime = "25:00" }, "close_time"},
		{"categories", func(c *Config) { c.Categories = nil }, "categories"},
		{"voice threshold", func(c *Config) { c.VoiceMatchThreshold = 1.5 }, "voice_match_threshold"},
		{"diarization", func(c *Config) { c.Diarization = "bogus" }, "diarization"},
		{"relative data dir", func(c *Config) { c.DataDir = "WorkJournal" }, "data_dir"},
		{"unexpanded tilde", func(c *Config) { c.DataDir = "~/WorkJournal" }, "data_dir"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Default()
			tt.mod(&c)
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestSaveInvalidLeavesFileUnchanged(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := Save(p, Default()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(p)
	bad := Default()
	bad.RetentionDays = 0
	if err := Save(p, bad); err == nil {
		t.Fatal("expected error")
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Fatal("file changed")
	}
}

func TestCloseClock(t *testing.T) {
	c := Default()
	c.CloseTime = "07:05"
	if h, m := c.CloseClock(); h != 7 || m != 5 {
		t.Fatalf("got %d:%d", h, m)
	}
}

// M10: a leading "~/" in data_dir means the home directory.
func TestLoadExpandsTildeInDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("data_dir = \"~/Journal\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != filepath.Join(home, "Journal") {
		t.Fatalf("data_dir = %q", c.DataDir)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}
