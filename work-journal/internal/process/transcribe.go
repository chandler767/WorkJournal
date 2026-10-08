package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"workjournal/internal/diarize"
	"workjournal/internal/media"
)

// ErrChunkNotReady means an audio chunk could not be read while the recorder
// may still be finishing it; the hour stays pending and is retried.
var ErrChunkNotReady = errors.New("audio chunk not finished yet")

// transcribeAudio returns the speech of the given chunks of day, using the
// processing cache: a chunk is converted to WAV (in a per-call temp dir),
// transcribed and, on the system track, diarized only when its cache entry
// is missing or stale. Speaker labels are assigned afresh on every call from
// the cached turns and embeddings, so they stay stable as chunks are added.
//
// A chunk afconvert cannot read fails the call with ErrChunkNotReady while it
// is younger than chunk_minutes+2m (the recorder finalizes a silent chunk only
// at its boundary) unless skipOpen is set, when it is skipped like an older
// unreadable chunk. A transcription error fails the call. Returned
// embeddings are keyed by speaker label.
func (p *Pipeline) transcribeAudio(ctx context.Context, day string, chunks []chunk, skipOpen bool) ([]media.Utterance, map[string][]float32, error) {
	if len(chunks) == 0 {
		return nil, nil, nil
	}
	var tmp string
	defer func() {
		if tmp != "" {
			os.RemoveAll(tmp)
		}
	}()

	var d diarize.Diarizer
	if p.Cfg.Diarization != "off" {
		d = p.Diarizer
	}
	lb := &labeler{p: p, embs: map[string][]float32{}}
	var all []media.Utterance
	for _, c := range chunks {
		name := filepath.Base(c.path)
		key, err := statKey(c.path)
		if err != nil {
			return nil, nil, fmt.Errorf("stat %s: %w", name, err)
		}
		e, ok := p.loadChunk(day, c.path, key)
		needText := !ok
		needTurns := c.track == "system" && d != nil && (!ok || !e.Diarized)
		if needText || needTurns {
			if tmp == "" {
				if tmp, err = os.MkdirTemp("", "workjournal-hour-*"); err != nil {
					return nil, nil, err
				}
			}
			wav := filepath.Join(tmp, strings.TrimSuffix(name, filepath.Ext(name))+".wav")
			if err := media.ToWav16k(ctx, p.Run, c.path, wav); err != nil {
				switch {
				case ctx.Err() != nil:
					return nil, nil, ctx.Err()
				case !needText:
					// Cached speech stays usable; only the speakers are unknown.
					p.logf("process: cannot read %s to diarize: %v; labelling speech as others", c.path, err)
					needTurns = false
				case !p.chunkMayBeOpen(c.path):
					p.logf("process: skipping unreadable chunk %s: %v", c.path, err)
					continue
				case skipOpen:
					p.logf("process: chunk %s not readable yet; leaving it for a later run", c.path)
					continue
				default:
					return nil, nil, fmt.Errorf("%w: %s: %v", ErrChunkNotReady, name, err)
				}
			} else if needText {
				us, err := p.Transcriber.Transcribe(ctx, wav, c.start, c.track)
				if err != nil {
					return nil, nil, fmt.Errorf("transcribe %s: %w", name, err)
				}
				e = &chunkEntry{Version: cacheVersion, Key: key, Model: p.Cfg.WhisperModel, Utterances: us}
			}
			if needTurns {
				turns, embs, err := d.Diarize(ctx, wav)
				if err != nil {
					p.logf("process: diarize %s: %v; labelling speech as others", name, err)
				} else {
					e.Diarized, e.Turns, e.Embeddings = true, turns, embs
				}
			}
			if needText || e.Diarized {
				p.saveChunk(day, c.path, e)
			}
		}
		us := append([]media.Utterance(nil), e.Utterances...)
		if c.track == "system" {
			var turns []diarize.Turn
			if d != nil && e.Diarized {
				turns = e.Turns
			}
			lb.label(turns, e.Embeddings, c.start, us)
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
	embs   map[string][]float32
	labels []string // labels with embeddings, in first-seen order
	anon   int
}

// label sets Speaker on each utterance of one system chunk: the label of the
// turn with the largest time overlap, or "others" without usable turns.
func (l *labeler) label(turns []diarize.Turn, embs map[int][]float32, chunkStart time.Time, us []media.Utterance) {
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

// chunkMayBeOpen reports whether the chunk was modified within
// chunk_minutes+2m, so the recorder may not have finalized it yet.
func (p *Pipeline) chunkMayBeOpen(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	limit := time.Duration(p.Cfg.AudioChunkMinutes)*time.Minute + 2*time.Minute
	return p.now().Sub(fi.ModTime()) < limit
}
