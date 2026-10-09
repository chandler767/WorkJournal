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
	c  chunk
	e  *chunkEntry
	us []media.Utterance // e.Utterances less silence hallucinations (see dropHallucinations)
}

// wavDir converts chunks to WAV in one temp directory, removed by close. It
// also tallies the hour's diarization results (see warnDiarization).
type wavDir struct {
	dir string
	// eligible counts chunks whose speakers come from diarization (cached
	// or diarized now); failed those whose Diarize call failed, the first
	// with firstErr.
	eligible, failed int
	firstErr         error
}

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
// returns its path; err is afconvert's. normErr is set when the WAV's header
// cannot be rewritten as canonical PCM (media.NormalizeWav16k): whisper can
// still read the file, the diarizer cannot.
func (w *wavDir) convert(ctx context.Context, p *Pipeline, c chunk) (wav string, normErr, err error) {
	name := filepath.Base(c.path)
	wav = filepath.Join(w.dir, strings.TrimSuffix(name, filepath.Ext(name))+".wav")
	if err := media.ConvertToWav16k(ctx, p.Run, c.path, wav); err != nil {
		return wav, nil, err
	}
	return wav, media.NormalizeWav16k(wav), nil
}

// fail tallies a chunk whose speakers could not be separated.
func (w *wavDir) fail(err error) {
	if w.failed++; w.firstErr == nil {
		w.firstErr = err
	}
}

func (w *wavDir) close() {
	if w.dir != "" {
		os.RemoveAll(w.dir)
	}
}

// diarizer is the configured diarizer, or nil when diarization is off or
// only the NoopDiarizer is available (speaker models missing): then system
// speech is "others", mic speech "me", and no cache entry is marked
// diarized, so installing the models later diarizes the cached chunks.
func (p *Pipeline) diarizer() diarize.Diarizer {
	if _, noop := p.Diarizer.(diarize.NoopDiarizer); noop || p.Cfg.Diarization == "off" {
		return nil
	}
	return p.Diarizer
}

// loadAudio returns the processed chunks of day, using the processing
// cache: a chunk is converted to WAV, transcribed and, on the system track,
// diarized only when its cache entry is missing or stale. Isolated whisper
// silence hallucinations are dropped across both tracks (dropHallucinations;
// the cache keeps whisper's output). Speakers are not labelled yet; see
// labelAudio.
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
			wav, normErr, err := w.convert(ctx, p, c)
			if err == nil && normErr != nil && needTurns {
				p.logf("process: %v; transcribing %s as is, speakers unknown", normErr, name)
				w.fail(normErr)
				needTurns = false
			}
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
				p.diarizeChunk(ctx, d, wav, name, e, w)
			}
			if needText || e.Diarized {
				p.saveChunk(day, c.path, e)
			}
		}
		if c.track == "system" && d != nil {
			w.eligible++
		}
		out = append(out, audioChunk{c: c, e: e})
	}
	dropHallucinations(out)
	return out, nil
}

// dropHallucinations sets each chunk's us to its utterances less the silence
// hallucinations found among all the chunks' speech, both tracks together,
// so a short reply right after the other track spoke is kept.
func dropHallucinations(cs []audioChunk) {
	var all []media.Utterance
	for _, ac := range cs {
		all = append(all, ac.e.Utterances...)
	}
	kept := map[media.Utterance]int{}
	for _, u := range media.DropHallucinations(all) {
		kept[u]++
	}
	for i := range cs {
		cs[i].us = nil
		for _, u := range cs[i].e.Utterances {
			if kept[u] > 0 {
				kept[u]--
				cs[i].us = append(cs[i].us, u)
			}
		}
	}
}

// diarizeChunk stores wav's turns and embeddings in e; a failure is logged,
// tallied in w and leaves e undiarized (so the next pass tries again).
func (p *Pipeline) diarizeChunk(ctx context.Context, d diarize.Diarizer, wav, name string, e *chunkEntry, w *wavDir) {
	turns, embs, err := d.Diarize(ctx, wav)
	if err != nil {
		p.logf("process: diarize %s: %v; speakers unknown", name, err)
		if ctx.Err() == nil {
			w.fail(err)
		}
		return
	}
	e.Diarized, e.Turns, e.Embeddings = true, turns, embs
}

// speechOf returns the chunks' utterances, unlabelled, in time order.
func speechOf(cs []audioChunk) []media.Utterance {
	var all []media.Utterance
	for _, ac := range cs {
		all = append(all, ac.us...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Start.Before(all[j].Start) })
	return all
}

// labelAudio returns the chunks' speech with speakers labelled (see
// speakers.go). System speech is labelled by diarization ("others" without it).
// Mic speech is "me", except during an in-person meeting (one of meetings;
// the utterance starts within it): there the mic chunk is diarized too (and
// cached) and its speakers are labelled like system ones, sharing the
// meeting's "Speaker N" numbering. The user's own voice is "me" there only
// through an enrolled voice named "me"; an in-meeting mic utterance no
// diarizer turn covers is "Speaker ?". If the mic chunk cannot be diarized
// its speech stays "me". start and end bound the hour cs belongs to.
func (p *Pipeline) labelAudio(ctx context.Context, day string, start, end time.Time, cs []audioChunk, meetings []activity.Meeting, w *wavDir) (*labelled, error) {
	d := p.diarizer()
	var inPerson []activity.Meeting
	for _, m := range meetings {
		if m.App == activity.InPersonApp {
			inPerson = append(inPerson, m)
		}
	}
	inside := func(u media.Utterance) bool {
		return slices.ContainsFunc(inPerson, func(m activity.Meeting) bool { return inMeetingSpan(m, u) })
	}
	diarizedMic := func(ac audioChunk) bool {
		return ac.c.track == "mic" && d != nil && ac.e.Diarized && slices.ContainsFunc(ac.us, inside)
	}
	var turns []diarChunk // this hour's chunks whose turns label speech
	for _, ac := range cs {
		c, e := ac.c, ac.e
		switch {
		case c.track == "system":
			if d != nil && e.Diarized {
				turns = append(turns, diarChunk{c, e})
			}
			continue
		case d == nil || !slices.ContainsFunc(ac.us, inside):
			continue
		}
		w.eligible++
		if !e.Diarized {
			if err := w.ensure(); err != nil {
				return nil, err
			}
			name := filepath.Base(c.path)
			wav, normErr, err := w.convert(ctx, p, c)
			switch {
			case err != nil && ctx.Err() != nil:
				return nil, ctx.Err()
			case err != nil:
				p.logf("process: cannot read %s to diarize: %v; mic speech stays me", c.path, err)
			case normErr != nil:
				p.logf("process: %v; mic speech of %s stays me", normErr, name)
				w.fail(normErr)
			default:
				p.diarizeChunk(ctx, d, wav, name, e, w)
				if e.Diarized {
					p.saveChunk(day, c.path, e)
				}
			}
		}
		if e.Diarized {
			turns = append(turns, diarChunk{c, e})
		}
	}

	sp := p.newSpeakers(day, start, end, meetings, turns)
	res := &labelled{}
	for _, ac := range cs {
		us := append([]media.Utterance(nil), ac.us...)
		switch {
		case ac.c.track == "system" && d != nil && ac.e.Diarized:
			sp.label(ac.c, ac.e, us, nil, "others")
		case ac.c.track == "system":
			for i := range us {
				us[i].Speaker = "others"
			}
		case ac.c.track == "mic" && d != nil && slices.ContainsFunc(ac.us, inside):
			ok := diarizedMic(ac)
			if ok {
				sp.label(ac.c, ac.e, us, inside, unknownSpeaker)
			}
			for _, u := range us {
				if inside(u) {
					if ok {
						res.diarizedMic = append(res.diarizedMic, u)
					} else {
						res.undiarizedMic = append(res.undiarizedMic, u)
					}
				}
			}
		}
		res.utts = append(res.utts, us...)
	}
	sort.SliceStable(res.utts, func(i, j int) bool { return res.utts[i].Start.Before(res.utts[j].Start) })
	if len(sp.embs) > 0 {
		res.embs = sp.embs
	}
	return res, nil
}

// warnDiarization logs one WARNING line when diarization failed on more
// than half of the hour's eligible chunks: speaker separation is then
// probably broken rather than unlucky (see `journal doctor`).
func (p *Pipeline) warnDiarization(day string, hour int, w *wavDir) {
	if w.failed == 0 || 2*w.failed <= w.eligible {
		return
	}
	p.logf("process: WARNING: speaker separation failed on %d of %d chunks in %s %02d (first error: %v); speakers unknown, run `journal doctor`",
		w.failed, w.eligible, day, hour, w.firstErr)
}

// unknownSpeaker labels diarized speech of no known voice: in-meeting mic
// speech no diarizer turn covers, or a voice with too little speech that
// sounds like no other in its meeting (see speakers.go).
const unknownSpeaker = "Speaker ?"

// labelled is labelAudio's result.
type labelled struct {
	utts []media.Utterance
	embs map[string][]float32 // by speaker label; nil when none
	// In-person meeting mic speech from chunks that were (or could not be)
	// diarized, for the two-voice check (see keepInPerson).
	diarizedMic, undiarizedMic []media.Utterance
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
