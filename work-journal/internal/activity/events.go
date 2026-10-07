// Package activity turns the recorder's activity.jsonl into time segments and
// detected meetings.
package activity

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Event is one line of raw/<day>/activity.jsonl.
type Event struct {
	TS     time.Time
	Type   string
	App    string
	Bundle string
	Title  string
}

type rawEvent struct {
	TS     string `json:"ts"`
	Type   string `json:"type"`
	App    string `json:"app"`
	Bundle string `json:"bundle"`
	Title  string `json:"title"`
}

// ReadEvents parses an activity.jsonl file. Malformed lines (bad JSON, missing
// or unparseable ts, missing type) are skipped and counted; blank lines are
// ignored. A missing file yields an error satisfying errors.Is(err, fs.ErrNotExist).
func ReadEvents(path string) ([]Event, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open activity file: %w", err)
	}
	defer f.Close()

	var events []Event
	skipped := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(trimSpace(line)) == 0 {
			continue
		}
		var r rawEvent
		if err := json.Unmarshal(line, &r); err != nil || r.Type == "" {
			skipped++
			continue
		}
		ts, err := time.Parse(time.RFC3339, r.TS)
		if err != nil {
			skipped++
			continue
		}
		events = append(events, Event{TS: ts, Type: r.Type, App: r.App, Bundle: r.Bundle, Title: r.Title})
	}
	if err := sc.Err(); err != nil {
		return events, skipped, fmt.Errorf("read activity file: %w", err)
	}
	return events, skipped, nil
}

func trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\r' || b[0] == '\n') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t' || b[len(b)-1] == '\r' || b[len(b)-1] == '\n') {
		b = b[:len(b)-1]
	}
	return b
}
