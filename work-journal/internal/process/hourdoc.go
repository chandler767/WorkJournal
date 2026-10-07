// Package process turns each finished hour of raw recordings into a derived
// HourDoc, indexes it in the store and hands it to the summarizer.
package process

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"workjournal/internal/activity"
	"workjournal/internal/media"
)

// HourDoc is the derived record of one hour (derived/<day>/<HH>.json).
// Embeddings are kept only in memory; they are never serialized.
type HourDoc struct {
	Day           string               `json:"day"`
	Hour          int                  `json:"hour"`
	Status        string               `json:"status"`
	Segments      []activity.Segment   `json:"segments"`
	Transcript    []media.Utterance    `json:"transcript"`
	Screens       []media.ScreenText   `json:"screens"`
	Meetings      []activity.Meeting   `json:"meetings"`
	EndedMeetings []string             `json:"ended_meetings"`
	Embeddings    map[string][]float32 `json:"-"`
}

// Load reads a derived HourDoc.
func Load(path string) (*HourDoc, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load hour doc: %w", err)
	}
	var d HourDoc
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("parse hour doc %s: %w", path, err)
	}
	return &d, nil
}

// Save writes d to path atomically (temp file + rename), creating parent
// directories as needed.
func (d *HourDoc) Save(path string) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".hour-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
