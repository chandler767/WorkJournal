package media

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"workjournal/internal/activity"
)

// ScreenText is the OCR text of one screenshot.
type ScreenText struct {
	Time       time.Time
	File, Text string
}

// OCR extracts text from screenshots.
type OCR interface {
	OCR(ctx context.Context, files []string) ([]ScreenText, error)
}

// RecorderOCR calls `recorder ocr <file>…` in batches.
type RecorderOCR struct {
	Run   Runner
	Bin   string
	Batch int // files per invocation; defaults to 50
}

type ocrEntry struct {
	File string `json:"file"`
	Text string `json:"text"`
}

// FileOCR is implemented by OCR engines that can report every file's text
// as is (empty and repeated texts included), keyed by the path passed in.
// The pipeline uses it to cache OCR results per screenshot.
type FileOCR interface {
	OCRFiles(ctx context.Context, files []string) (map[string]string, error)
}

// OCR implements OCR. Time comes from raw/<day>/screens/HHMMSS.jpg in the
// day's recording zone; unparseable names are skipped. Empty texts are dropped and
// consecutive identical texts collapse to the first.
func (r RecorderOCR) OCR(ctx context.Context, files []string) ([]ScreenText, error) {
	entries, err := r.run(ctx, files)
	if err != nil {
		return nil, err
	}
	var out []ScreenText
	last := ""
	have := false
	for _, e := range entries {
		t, err := screenTime(e.File)
		if err != nil {
			log.Printf("media: skipping %s: %v", e.File, err)
			continue
		}
		text := strings.TrimSpace(e.Text)
		if text == "" || (have && text == last) {
			continue
		}
		out = append(out, ScreenText{Time: t, File: e.File, Text: text})
		last, have = text, true
	}
	return out, nil
}

// OCRFiles implements FileOCR: each file's trimmed text, "" for none.
func (r RecorderOCR) OCRFiles(ctx context.Context, files []string) (map[string]string, error) {
	entries, err := r.run(ctx, files)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(files))
	for _, e := range entries {
		out[e.File] = strings.TrimSpace(e.Text)
	}
	return out, nil
}

// run calls `recorder ocr` in batches and returns its entries in order.
func (r RecorderOCR) run(ctx context.Context, files []string) ([]ocrEntry, error) {
	batch := r.Batch
	if batch <= 0 {
		batch = 50
	}
	var all []ocrEntry
	for i := 0; i < len(files); i += batch {
		chunk := files[i:min(i+batch, len(files))]
		stdout, err := r.Run(ctx, r.Bin, append([]string{"ocr"}, chunk...)...)
		if err != nil {
			return nil, err
		}
		var entries []ocrEntry
		if err := json.Unmarshal(stdout, &entries); err != nil {
			return nil, fmt.Errorf("parse ocr output: %w", err)
		}
		all = append(all, entries...)
	}
	return all, nil
}

// screenTime is the time of raw/<day>/screens/HHMMSS.jpg, a wall clock in
// the zone the day was recorded in (its activity log; time.Local without).
func screenTime(file string) (time.Time, error) {
	dayDir := filepath.Dir(filepath.Dir(file))
	day := filepath.Base(dayDir)
	stem := strings.TrimSuffix(filepath.Base(file), ".jpg")
	wall, err := time.ParseInLocation("2006-01-02 150405", day+" "+stem, time.UTC)
	if err != nil {
		return time.Time{}, err
	}
	zones := activity.LoadZoneTimeline(filepath.Join(dayDir, "activity.jsonl"), time.Local)
	return time.ParseInLocation("2006-01-02 150405", day+" "+stem, zones.At(wall))
}
