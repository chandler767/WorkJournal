package media

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"
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

// OCR implements OCR. Time comes from raw/<day>/screens/HHMMSS.jpg in the
// local zone; unparseable names are skipped. Empty texts are dropped and
// consecutive identical texts collapse to the first.
func (r RecorderOCR) OCR(ctx context.Context, files []string) ([]ScreenText, error) {
	batch := r.Batch
	if batch <= 0 {
		batch = 50
	}
	var out []ScreenText
	last := ""
	have := false
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
	}
	return out, nil
}

func screenTime(file string) (time.Time, error) {
	day := filepath.Base(filepath.Dir(filepath.Dir(file)))
	stem := strings.TrimSuffix(filepath.Base(file), ".jpg")
	return time.ParseInLocation("2006-01-02 150405", day+" "+stem, time.Local)
}
