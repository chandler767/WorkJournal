package media

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"
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
// utterances are left without a speaker for later diarization. Non-speech
// tags are dropped; silence phrases are kept, because whether one is
// isolated depends on both tracks (see DropHallucinations).
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
		if nonSpeech(text) {
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

// silencePhrases are whole-segment texts whisper is known to hallucinate on
// silence, normalized by normalizePhrase.
var silencePhrases = map[string]bool{
	"you": true, "thank you": true, "thanks for watching": true, "bye": true,
	"thank you for watching": true, "please subscribe": true,
}

// hallucinationWindow is how close (either side) other speech must be for a
// silence phrase to count as real speech.
const hallucinationWindow = 3 * time.Second

// DropHallucinations removes whisper's silence hallucinations from us, which
// should hold all speech of both tracks over the span (an hour): a segment
// whose whole text (case-insensitive, punctuation stripped) is a known
// silence phrase such as "You" or "Thank you." is dropped when no other
// segment that is not such a phrase, on either track, lies within 3 s of it.
// A phrase next to real speech, such as a "Thank you." right after the other
// side spoke, is kept. Order is preserved.
func DropHallucinations(us []Utterance) []Utterance {
	cand := make([]bool, len(us))
	for i, u := range us {
		cand[i] = silencePhrases[normalizePhrase(u.Text)]
	}
	var out []Utterance
	for i, u := range us {
		if cand[i] && !nearSpeech(us, cand, i) {
			continue
		}
		out = append(out, u)
	}
	return out
}

// nearSpeech reports whether a non-candidate utterance lies within
// hallucinationWindow of us[i].
func nearSpeech(us []Utterance, cand []bool, i int) bool {
	from, to := us[i].Start.Add(-hallucinationWindow), us[i].End.Add(hallucinationWindow)
	for j, o := range us {
		if j != i && !cand[j] && !o.Start.After(to) && !o.End.Before(from) {
			return true
		}
	}
	return false
}

// normalizePhrase lowercases s, drops everything but letters, digits and
// spaces, and collapses whitespace.
func normalizePhrase(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// nonSpeech reports whether a whisper segment holds no speech: it is empty,
// or consists only of bracketed, parenthesized or asterisked tags such as
// "[BLANK_AUDIO]", "[ Silence ]", "(music)" or "*laughs*" (plus
// punctuation). A segment with any real words after its tags is kept whole.
func nonSpeech(text string) bool {
	s := strings.TrimSpace(text)
	for s != "" {
		closer := map[byte]byte{'[': ']', '(': ')', '*': '*'}[s[0]]
		if closer == 0 {
			break
		}
		j := strings.IndexByte(s[1:], closer)
		if j < 0 {
			break
		}
		s = strings.TrimSpace(s[j+2:])
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
