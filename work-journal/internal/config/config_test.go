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
	if c.Diarization != "sherpa" || c.VoiceMatchThreshold != 0.60 {
		t.Fatalf("diarization = %q, voice_match_threshold = %v", c.Diarization, c.VoiceMatchThreshold)
	}
	if c.SpeakerClusterThreshold != 0.45 || c.SpeakerMinSeconds != 20 {
		t.Fatalf("speaker_cluster_threshold = %v, speaker_min_seconds = %v", c.SpeakerClusterThreshold, c.SpeakerMinSeconds)
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
		{"cluster threshold low", func(c *Config) { c.SpeakerClusterThreshold = 0.1 }, "speaker_cluster_threshold"},
		{"cluster threshold high", func(c *Config) { c.SpeakerClusterThreshold = 0.95 }, "speaker_cluster_threshold"},
		{"speaker min seconds", func(c *Config) { c.SpeakerMinSeconds = -1 }, "speaker_min_seconds"},
		{"speaker min seconds high", func(c *Config) { c.SpeakerMinSeconds = 601 }, "speaker_min_seconds"},
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

func TestProcessIntervalDefaultAndValidation(t *testing.T) {
	c := Default()
	if c.ProcessIntervalMinutes != 15 {
		t.Fatalf("process_interval_minutes default = %d, want 15", c.ProcessIntervalMinutes)
	}
	for _, v := range []int{5, 10, 15, 20, 30, 60} {
		c.ProcessIntervalMinutes = v
		if err := c.Validate(); err != nil {
			t.Errorf("%d rejected: %v", v, err)
		}
	}
	for _, v := range []int{0, 1, 7, 45, 90, -5} {
		c.ProcessIntervalMinutes = v
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "process_interval_minutes") {
			t.Errorf("%d accepted (err %v)", v, err)
		}
	}
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("process_interval_minutes = 30\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil || got.ProcessIntervalMinutes != 30 {
		t.Fatalf("Load = %d, %v", got.ProcessIntervalMinutes, err)
	}
}

func TestScreenCaptureDefaultsAndValidation(t *testing.T) {
	c := Default()
	if c.ScreenshotMinGapSec != 3 || c.ScreenshotIdleBackstopSec != 60 ||
		c.ScreenChangeSensitivity != "medium" || !c.CaptureOnAppSwitch {
		t.Fatalf("defaults: gap %d backstop %d sensitivity %q switch %v", c.ScreenshotMinGapSec,
			c.ScreenshotIdleBackstopSec, c.ScreenChangeSensitivity, c.CaptureOnAppSwitch)
	}
	ok := []func(*Config){
		func(c *Config) { c.ScreenshotMinGapSec = 1 },
		func(c *Config) { c.ScreenshotMinGapSec = 60 },
		func(c *Config) { c.ScreenshotIdleBackstopSec = 10 },
		func(c *Config) { c.ScreenshotIdleBackstopSec = 600 },
		func(c *Config) { c.ScreenChangeSensitivity = "low" },
		func(c *Config) { c.ScreenChangeSensitivity = "high" },
		func(c *Config) { c.CaptureOnAppSwitch = false },
	}
	for i, f := range ok {
		c := Default()
		f(&c)
		if err := c.Validate(); err != nil {
			t.Errorf("ok[%d] rejected: %v", i, err)
		}
	}
	bad := []struct {
		key string
		f   func(*Config)
	}{
		{"screenshot_min_gap_sec", func(c *Config) { c.ScreenshotMinGapSec = 0 }},
		{"screenshot_min_gap_sec", func(c *Config) { c.ScreenshotMinGapSec = 61 }},
		{"screenshot_idle_backstop_sec", func(c *Config) { c.ScreenshotIdleBackstopSec = 9 }},
		{"screenshot_idle_backstop_sec", func(c *Config) { c.ScreenshotIdleBackstopSec = 601 }},
		{"screen_change_sensitivity", func(c *Config) { c.ScreenChangeSensitivity = "max" }},
		{"screen_change_sensitivity", func(c *Config) { c.ScreenChangeSensitivity = "" }},
	}
	for _, b := range bad {
		c := Default()
		b.f(&c)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), b.key) {
			t.Errorf("%s: accepted (err %v)", b.key, err)
		}
	}
	p := filepath.Join(t.TempDir(), "config.toml")
	body := "screenshot_min_gap_sec = 5\nscreenshot_idle_backstop_sec = 120\nscreen_change_sensitivity = \"low\"\ncapture_on_app_switch = false\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil || got.ScreenshotMinGapSec != 5 || got.ScreenshotIdleBackstopSec != 120 ||
		got.ScreenChangeSensitivity != "low" || got.CaptureOnAppSwitch {
		t.Fatalf("Load = %+v, %v", got, err)
	}
	// An older config with only screenshot_interval_sec keeps the new defaults.
	if err := os.WriteFile(p, []byte("screenshot_interval_sec = 10\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err = Load(p); err != nil || got.ScreenshotIdleBackstopSec != 60 || !got.CaptureOnAppSwitch {
		t.Fatalf("legacy Load = %+v, %v", got, err)
	}
}

func TestInPersonDefaultsAndValidation(t *testing.T) {
	c := Default()
	if c.InPersonMinMinutes != 5 || c.InPersonSpeechRatio != 0.4 {
		t.Fatalf("defaults: min %d ratio %v", c.InPersonMinMinutes, c.InPersonSpeechRatio)
	}
	ok := []func(*Config){
		func(c *Config) { c.InPersonMinMinutes = 1 },
		func(c *Config) { c.InPersonMinMinutes = 120 },
		func(c *Config) { c.InPersonSpeechRatio = 0.05 },
		func(c *Config) { c.InPersonSpeechRatio = 1 },
	}
	for i, f := range ok {
		c := Default()
		f(&c)
		if err := c.Validate(); err != nil {
			t.Errorf("ok[%d] rejected: %v", i, err)
		}
	}
	bad := []struct {
		key string
		f   func(*Config)
	}{
		{"in_person_min_minutes", func(c *Config) { c.InPersonMinMinutes = 0 }},
		{"in_person_min_minutes", func(c *Config) { c.InPersonMinMinutes = 121 }},
		{"in_person_speech_ratio", func(c *Config) { c.InPersonSpeechRatio = 0.04 }},
		{"in_person_speech_ratio", func(c *Config) { c.InPersonSpeechRatio = 1.01 }},
	}
	for _, b := range bad {
		c := Default()
		b.f(&c)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), b.key) {
			t.Errorf("%s: accepted (err %v)", b.key, err)
		}
	}
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("in_person_min_minutes = 10\nin_person_speech_ratio = 0.5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(p); err != nil || got.InPersonMinMinutes != 10 || got.InPersonSpeechRatio != 0.5 {
		t.Fatalf("Load = %+v, %v", got, err)
	}
}

func TestPrivacyDefaultsAndValidation(t *testing.T) {
	c := Default()
	if !c.PrivacyFilter || !c.RedactIdentifiers || c.PrivacyDropConfidence != 0.7 {
		t.Fatalf("defaults: filter %v redact %v confidence %v", c.PrivacyFilter, c.RedactIdentifiers, c.PrivacyDropConfidence)
	}
	for _, v := range []float64{0.5, 0.95} {
		c := Default()
		c.PrivacyDropConfidence = v
		if err := c.Validate(); err != nil {
			t.Errorf("%v rejected: %v", v, err)
		}
	}
	for _, v := range []float64{0.49, 0.96, 0} {
		c := Default()
		c.PrivacyDropConfidence = v
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "privacy_drop_confidence") {
			t.Errorf("%v accepted (err %v)", v, err)
		}
	}
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("privacy_filter = false\nredact_identifiers = false\nprivacy_drop_confidence = 0.8\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil || got.PrivacyFilter || got.RedactIdentifiers || got.PrivacyDropConfidence != 0.8 {
		t.Fatalf("Load = %+v, %v", got, err)
	}
}
