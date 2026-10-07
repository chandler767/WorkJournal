package media

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Utterance is one transcribed span of speech.
type Utterance struct {
	Start, End time.Time
	Track      string // "mic" or "system"
	Speaker    string
	Text       string
}

// Transcriber turns a 16 kHz WAV into utterances.
type Transcriber interface {
	Transcribe(ctx context.Context, wav string, chunkStart time.Time, track string) ([]Utterance, error)
}

// Whisper runs whisper-cli.
type Whisper struct {
	Run   Runner
	Bin   string
	Model string
}

type whisperOutput struct {
	Transcription []struct {
		Offsets struct {
			From int64 `json:"from"`
			To   int64 `json:"to"`
		} `json:"offsets"`
		Text string `json:"text"`
	} `json:"transcription"`
}

// Transcribe implements Transcriber. Mic utterances get Speaker "me"; system
// utterances are left without a speaker for later diarization.
func (w Whisper) Transcribe(ctx context.Context, wav string, chunkStart time.Time, track string) ([]Utterance, error) {
	prefix := strings.TrimSuffix(wav, ".wav")
	if _, err := w.Run(ctx, w.Bin, "-m", w.Model, "-f", wav, "-oj", "-of", prefix, "-np"); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(prefix + ".json")
	if err != nil {
		return nil, fmt.Errorf("read whisper output: %w", err)
	}
	var out whisperOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse whisper output: %w", err)
	}
	speaker := ""
	if track == "mic" {
		speaker = "me"
	}
	var us []Utterance
	for _, e := range out.Transcription {
		text := strings.TrimSpace(e.Text)
		if text == "" || text == "[BLANK_AUDIO]" {
			continue
		}
		us = append(us, Utterance{
			Start:   chunkStart.Add(time.Duration(e.Offsets.From) * time.Millisecond),
			End:     chunkStart.Add(time.Duration(e.Offsets.To) * time.Millisecond),
			Track:   track,
			Speaker: speaker,
			Text:    text,
		})
	}
	return us, nil
}
