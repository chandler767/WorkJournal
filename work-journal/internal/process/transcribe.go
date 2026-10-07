package process

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"workjournal/internal/diarize"
	"workjournal/internal/media"
)

// transcribeAudio converts each chunk to WAV in a per-hour temp dir,
// transcribes it and labels system speakers. Chunks afconvert cannot read
// are logged and skipped; a transcription error fails the hour. Returned
// embeddings are keyed by speaker label.
func (p *Pipeline) transcribeAudio(ctx context.Context, chunks []chunk) ([]media.Utterance, map[string][]float32, error) {
	if len(chunks) == 0 {
		return nil, nil, nil
	}
	tmp, err := os.MkdirTemp("", "workjournal-hour-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(tmp)

	var d diarize.Diarizer
	if p.Cfg.Diarization != "off" {
		d = p.Diarizer
	}
	lb := &labeler{p: p, d: d, embs: map[string][]float32{}}
	var all []media.Utterance
	for _, c := range chunks {
		name := filepath.Base(c.path)
		wav := filepath.Join(tmp, strings.TrimSuffix(name, filepath.Ext(name))+".wav")
		if err := media.ToWav16k(ctx, p.Run, c.path, wav); err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			p.logf("process: skipping unreadable chunk %s: %v", c.path, err)
			continue
		}
		us, err := p.Transcriber.Transcribe(ctx, wav, c.start, c.track)
		if err != nil {
			return nil, nil, fmt.Errorf("transcribe %s: %w", name, err)
		}
		if c.track == "system" {
			lb.label(ctx, wav, c.start, us)
		}
		all = append(all, us...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Start.Before(all[j].Start) })
	if len(lb.embs) == 0 {
		return all, nil, nil
	}
	return all, lb.embs, nil
}

// labeler assigns speaker labels to system utterances across one hour.
type labeler struct {
	p      *Pipeline
	d      diarize.Diarizer // nil: everyone is "others"
	embs   map[string][]float32
	labels []string // labels with embeddings, in first-seen order
	anon   int
}

// label sets Speaker on each utterance of one system chunk: the label of the
// turn with the largest time overlap, or "others" without usable turns.
func (l *labeler) label(ctx context.Context, wav string, chunkStart time.Time, us []media.Utterance) {
	var turns []diarize.Turn
	var embs map[int][]float32
	if l.d != nil {
		var err error
		turns, embs, err = l.d.Diarize(ctx, wav)
		if err != nil {
			l.p.logf("process: diarize %s: %v; labelling speech as others", filepath.Base(wav), err)
			turns = nil
		}
	}
	ordered := append([]diarize.Turn(nil), turns...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Start < ordered[j].Start })
	names := map[int]string{}
	for _, t := range ordered {
		if _, ok := names[t.Speaker]; !ok {
			names[t.Speaker] = l.resolve(embs[t.Speaker])
		}
	}
	for i := range us {
		from := us[i].Start.Sub(chunkStart).Seconds()
		to := us[i].End.Sub(chunkStart).Seconds()
		best, bestOv := -1, 0.0
		for j, t := range turns {
			if ov := min(to, t.End) - max(from, t.Start); ov > bestOv {
				best, bestOv = j, ov
			}
		}
		if best < 0 {
			us[i].Speaker = "others"
		} else {
			us[i].Speaker = names[turns[best].Speaker]
		}
	}
}

// resolve names one chunk-local speaker: a known voice, else a speaker
// already heard this hour (cosine >= VoiceMatchThreshold), else the next
// "Speaker N".
func (l *labeler) resolve(emb []float32) string {
	if len(emb) > 0 {
		if l.p.Voices != nil {
			if name, _, ok := l.p.Voices.Match(emb); ok {
				l.remember(name, emb)
				return name
			}
		}
		best, bestScore := "", l.p.Cfg.VoiceMatchThreshold
		for _, lab := range l.labels {
			if s := diarize.Cosine(emb, l.embs[lab]); s >= bestScore {
				best, bestScore = lab, s
			}
		}
		if best != "" {
			return best
		}
	}
	l.anon++
	lab := fmt.Sprintf("Speaker %d", l.anon)
	l.remember(lab, emb)
	return lab
}

func (l *labeler) remember(label string, emb []float32) {
	if len(emb) == 0 {
		return
	}
	if _, ok := l.embs[label]; !ok {
		l.embs[label] = emb
		l.labels = append(l.labels, label)
	}
}
