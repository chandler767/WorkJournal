// Package config holds the work-journal configuration and data-directory layout.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is the user-editable configuration, stored as TOML.
type Config struct {
	DataDir                      string            `toml:"data_dir"`
	RetentionDays                int               `toml:"retention_days"`
	CloseTime                    string            `toml:"close_time"`
	RollupWeekday                string            `toml:"rollup_weekday"`
	IdleMinutes                  int               `toml:"idle_minutes"`
	PauseAutoResumeMinutes       int               `toml:"pause_auto_resume_minutes"`
	ScreenshotIntervalSec        int               `toml:"screenshot_interval_sec"`
	MeetingScreenshotIntervalSec int               `toml:"meeting_screenshot_interval_sec"`
	AudioChunkMinutes            int               `toml:"audio_chunk_minutes"`
	ScreenshotWidth              int               `toml:"screenshot_width"`
	AllDisplays                  bool              `toml:"all_displays"`
	WhisperModel                 string            `toml:"whisper_model"`
	Diarization                  string            `toml:"diarization"`
	VoiceMatchThreshold          float64           `toml:"voice_match_threshold"`
	NameConfidenceThreshold      float64           `toml:"name_confidence_threshold"`
	MinFreeGB                    int               `toml:"min_free_gb"`
	ListenAddr                   string            `toml:"listen_addr"`
	MeetingMinTwoWayMinutes      int               `toml:"meeting_min_two_way_minutes"`
	ExcludedApps                 []string          `toml:"excluded_apps"`
	ExcludedTitleKeywords        []string          `toml:"excluded_title_keywords"`
	MeetingApps                  []string          `toml:"meeting_apps"`
	MeetingTitleKeywords         []string          `toml:"meeting_title_keywords"`
	Categories                   []string          `toml:"categories"`
	Rules                        map[string]string `toml:"rules"`
}

// Default returns the built-in configuration.
func Default() Config {
	home, _ := os.UserHomeDir()
	return Config{
		DataDir:                      filepath.Join(home, "WorkJournal"),
		RetentionDays:                7,
		CloseTime:                    "18:00",
		RollupWeekday:                "Friday",
		IdleMinutes:                  5,
		PauseAutoResumeMinutes:       60,
		ScreenshotIntervalSec:        10,
		MeetingScreenshotIntervalSec: 5,
		AudioChunkMinutes:            5,
		ScreenshotWidth:              1440,
		WhisperModel:                 "base.en",
		Diarization:                  "sherpa",
		VoiceMatchThreshold:          0.75,
		NameConfidenceThreshold:      0.7,
		MinFreeGB:                    5,
		ListenAddr:                   "127.0.0.1:4747",
		MeetingMinTwoWayMinutes:      2,
		ExcludedApps:                 []string{"1Password", "Keychain Access"},
		ExcludedTitleKeywords:        []string{"Private", "Incognito"},
		MeetingApps:                  []string{"zoom.us", "Microsoft Teams", "FaceTime", "Slack"},
		MeetingTitleKeywords:         []string{"Huddle", "Google Meet", "Meet -", "Microsoft Teams", "Zoom Meeting"},
		Categories:                   []string{"Deep work", "Meetings", "Communication", "Admin", "Browsing/Research", "Break/Idle"},
		Rules:                        map[string]string{},
	}
}

// Load reads the config at path. A missing file yields Default();
// fields absent from the file keep their defaults.
func Load(path string) (Config, error) {
	c := Default()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return Config{}, err
	}
	if _, err := toml.Decode(string(b), &c); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.Rules == nil {
		c.Rules = map[string]string{}
	}
	return c, nil
}

// Save validates c and writes it atomically (temp file + rename).
func Save(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := toml.NewEncoder(tmp).Encode(c); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Validate reports the first invalid field.
func (c Config) Validate() error {
	positive := []struct {
		key string
		v   int
	}{
		{"retention_days", c.RetentionDays},
		{"idle_minutes", c.IdleMinutes},
		{"pause_auto_resume_minutes", c.PauseAutoResumeMinutes},
		{"screenshot_interval_sec", c.ScreenshotIntervalSec},
		{"meeting_screenshot_interval_sec", c.MeetingScreenshotIntervalSec},
		{"audio_chunk_minutes", c.AudioChunkMinutes},
		{"screenshot_width", c.ScreenshotWidth},
		{"meeting_min_two_way_minutes", c.MeetingMinTwoWayMinutes},
	}
	for _, p := range positive {
		if p.v < 1 {
			return fmt.Errorf("%s must be at least 1, got %d", p.key, p.v)
		}
	}
	if c.MinFreeGB < 0 {
		return fmt.Errorf("min_free_gb must not be negative, got %d", c.MinFreeGB)
	}
	if _, err := time.Parse("15:04", c.CloseTime); err != nil {
		return fmt.Errorf("close_time must be HH:MM (24h), got %q", c.CloseTime)
	}
	if _, ok := weekdays[strings.ToLower(c.RollupWeekday)]; !ok {
		return fmt.Errorf("rollup_weekday must be a weekday name, got %q", c.RollupWeekday)
	}
	if len(c.Categories) == 0 {
		return errors.New("categories must not be empty")
	}
	if c.VoiceMatchThreshold < 0 || c.VoiceMatchThreshold > 1 {
		return fmt.Errorf("voice_match_threshold must be within [0,1], got %v", c.VoiceMatchThreshold)
	}
	if c.NameConfidenceThreshold < 0 || c.NameConfidenceThreshold > 1 {
		return fmt.Errorf("name_confidence_threshold must be within [0,1], got %v", c.NameConfidenceThreshold)
	}
	if c.Diarization != "sherpa" && c.Diarization != "off" {
		return fmt.Errorf(`diarization must be "sherpa" or "off", got %q`, c.Diarization)
	}
	if c.ListenAddr == "" {
		return errors.New("listen_addr must not be empty")
	}
	if c.DataDir == "" {
		return errors.New("data_dir must not be empty")
	}
	return nil
}

var weekdays = map[string]bool{
	"monday": true, "tuesday": true, "wednesday": true, "thursday": true,
	"friday": true, "saturday": true, "sunday": true,
}

// CloseClock returns the hour and minute of CloseTime.
func (c Config) CloseClock() (hour, minute int) {
	t, err := time.Parse("15:04", c.CloseTime)
	if err != nil {
		return 18, 0
	}
	return t.Hour(), t.Minute()
}
