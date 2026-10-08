package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"workjournal/internal/activity"
	"workjournal/internal/diarize"
	"workjournal/internal/media"
)

// ErrChunkNotReady means an audio chunk could not be read while the recorder
// may still be finishing it; the hour stays pending and is retried.
var ErrChunkNotReady = errors.New("audio chunk not finished yet")

// audioChunk is one hour chunk with its (cached or fresh) processing result.
type audioChunk struct {
	c chunk
	e *chunkEntry
}

// wavDir converts chunks to WAV in one temp directory, removed by close.
type wavDir struct{ dir string }

// ensure creates the directory on first use.
func (w *wavDir) ensure() error {
	if w.dir != "" {
		return nil
	}
	dir, err := os.MkdirTemp("", "workjournal-hour-*")
	if err != nil {
		return err
	}
	w.dir = dir
	return nil
}

// convert writes c as a 16 kHz WAV into the directory (see ensure) and
// returns its path; an error is afconvert's.
func (w *wavDir) convert(ctx context.Context, p *Pipeline, c chunk) (string, error) {
	name := filepath.Base(c.path)
	wav := filepath.Join(w.dir, strings.TrimSuffix(name, filepath.Ext(name))+".wav")
	return wav, media.ToWav16k(ctx, p.Run, c.path, wav)
}

func (w *wavDir) close() {
	if w.dir != "" {
		os.RemoveAll(w.dir)
	}
}

// diarizer is the configured diarizer, or nil when diarization is off.
func (p *Pipeline) diarizer() diarize.Diarizer {
	if p.Cfg.Diarization == "off" {
		return nil
	}
	return p.Diarizer
}

// loadAudio returns the processed chunks of day, using the processing
// cache: a chunk is converted to WAV, transcribed and, on the system track,
// diarized only when its cache entry is missing or stale. Isolated whisper
// silence hallucinations are dropped (also from entries cached before that
// filter existed). Speakers are not labelled yet; see labelAudio.
//
// A chunk afconvert cannot read fails the call with ErrChunkNotReady while it
// is younger than chunk_minutes+2m (the recorder finalizes a silent chunk only
// at its boundary) unless skipOpen is set, when it is skipped like an older
// unreadable chunk. A transcription error fails the call.
func (p *Pipeline) loadAudio(ctx context.Context, day string, chunks []chunk, skipOpen bool, w *wavDir) ([]audioChunk, error) {
	d := p.diarizer()
	var out []audioChunk
	for _, c := range chunks {
		name := filepath.Base(c.path)
		key, err := statKey(c.path)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", name, err)
		}
		e, ok := p.loadChunk(day, c.path, key)
		needText := !ok
		needTurns := c.track == "system" && d != nil && (!ok || !e.Diarized)
		if needText || needTurns {
			if err := w.ensure(); err != nil {
				return nil, err
			}
			wav, err := w.convert(ctx, p, c)
			if err != nil {
				switch {
				case ctx.Err() != nil:
					return nil, ctx.Err()
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
					return nil, fmt.Errorf("%w: %s: %v", ErrChunkNotReady, name, err)
				}
			} else if needText {
				us, err := p.Transcriber.Transcribe(ctx, wav, c.start, c.track)
				if err != nil {
					return nil, fmt.Errorf("transcribe %s: %w", name, err)
				}
				e = &chunkEntry{Version: cacheVersion, Key: key, Model: p.Cfg.WhisperModel, Utterances: us}
			}
			if needTurns {
				p.diarizeChunk(ctx, d, wav, name, e)
			}
			if needText || e.Diarized {
				p.saveChunk(day, c.path, e)
			}
		}
		e.Utterances = media.DropHallucinations(e.Utterances)
		out = append(out, audioChunk{c: c, e: e})
	}
	return out, nil
}

// diarizeChunk stores wav's turns and embeddings in e; a failure is logged
// and leaves e undiarized.
func (p *Pipeline) diarizeChunk(ctx context.Context, d diarize.Diarizer, wav, name string, e *chunkEntry) {
	turns, embs, err := d.Diarize(ctx, wav)
	if err != nil {
		p.logf("process: diarize %s: %v; speakers unknown", name, err)
		return
	}
	e.Diarized, e.Turns, e.Embeddings = true, turns, embs
}

// speechOf returns the chunks' utterances, unlabelled, in time order.
func speechOf(cs []audioChunk) []media.Utterance {
	var all []media.Utterance
	for _, ac := range cs {
		all = append(all, ac.e.Utterances...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Start.Before(all[j].Start) })
	return all
}

// labelAudio returns the chunks' speech with speakers labelled. Speaker
// labels are assigned afresh on every call from the cached turns and
// embeddings, so they stay stable as chunks are added. System speech is
// labelled by diarization ("others" without it). Mic speech is "me", except
// during an in-person meeting (one of inPerson; the utterance starts within
// it): there the mic chunk is diarized too (and cached) and its speakers are
// labelled like system ones, sharing the hour's "Speaker N" numbering. The
// user's own voice is "me" there only through an enrolled voice named "me".
// Returned embeddings are keyed by speaker label.
func (p *Pipeline) labelAudio(ctx context.Context, day string, cs []audioChunk, inPerson []activity.Meeting, w *wavDir) ([]media.Utterance, map[string][]float32, error) {
	d := p.diarizer()
	inside := func(u media.Utterance) bool {
		return slices.ContainsFunc(inPerson, func(m activity.Meeting) bool {
			return !u.Start.Before(m.Start) && !u.Start.After(m.End)
		})
	}
	lb := &labeler{p: p, embs: map[string][]float32{}}
	var all []media.Utterance
	for _, ac := range cs {
		c, e := ac.c, ac.e
		us := append([]media.Utterance(nil), e.Utterances...)
		switch c.track {
		case "system":
			var turns []diarize.Turn
			if d != nil && e.Diarized {
				turns = e.Turns
			}
			lb.label(turns, e.Embeddings, c.start, us, nil, "others")
		case "mic":
			if d == nil || !slices.ContainsFunc(us, inside) {
				break
			}
			if !e.Diarized {
				if err := w.ensure(); err != nil {
					return nil, nil, err
				}
				name := filepath.Base(c.path)
				wav, err := w.convert(ctx, p, c)
				if err != nil {
					if ctx.Err() != nil {
						return nil, nil, ctx.Err()
					}
					p.logf("process: cannot read %s to diarize: %v; mic speech stays me", c.path, err)
					break
				}
				p.diarizeChunk(ctx, d, wav, name, e)
				if !e.Diarized {
					break
				}
				p.saveChunk(day, c.path, e)
			}
			lb.label(e.Turns, e.Embeddings, c.start, us, inside, "me")
		}
		all = append(all, us...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Start.Before(all[j].Start) })
	if len(lb.embs) == 0 {
		return all, nil, nil
	}
	return all, lb.embs, nil
}

// labeler assigns speaker labels to diarized utterances across one hour.
type labeler struct {
	p      *Pipeline
	embs   map[string][]float32
	labels []string // labels with embeddings, in first-seen order
	anon   int
}

// label sets Speaker on the utterances of one chunk that only accepts (all
// when only is nil): the label of the turn with the largest time overlap, or
// fallback without one. Without a filter every turn's speaker is named, in
// turn order; with one, only the speakers given to accepted utterances are.
func (l *labeler) label(turns []diarize.Turn, embs map[int][]float32, chunkStart time.Time, us []media.Utterance, only func(media.Utterance) bool, fallback string) {
	best := make([]int, len(us))
	used := map[int]bool{}
	for i := range us {
		best[i] = -1
		if only != nil && !only(us[i]) {
			continue
		}
		from := us[i].Start.Sub(chunkStart).Seconds()
		to := us[i].End.Sub(chunkStart).Seconds()
		bestOv := 0.0
		for j, t := range turns {
			if ov := min(to, t.End) - max(from, t.Start); ov > bestOv {
				best[i], bestOv = j, ov
			}
		}
		if best[i] >= 0 {
			used[turns[best[i]].Speaker] = true
		}
	}
	ordered := append([]diarize.Turn(nil), turns...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Start < ordered[j].Start })
	names := map[int]string{}
	for _, t := range ordered {
		if _, ok := names[t.Speaker]; !ok && (only == nil || used[t.Speaker]) {
			names[t.Speaker] = l.resolve(embs[t.Speaker])
		}
	}
	for i := range us {
		switch {
		case only != nil && !only(us[i]):
		case best[i] < 0:
			us[i].Speaker = fallback
		default:
			us[i].Speaker = names[turns[best[i]].Speaker]
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
