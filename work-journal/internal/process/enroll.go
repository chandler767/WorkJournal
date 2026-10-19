package process

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"workjournal/internal/activity"
	"workjournal/internal/config"
	"workjournal/internal/diarize"
	"workjournal/internal/media"
	"workjournal/internal/store"
)

// ErrNoRawAudio means none of the meeting's audio chunks on the speaker's
// track is left on disk (retention removed them), so no voice can be learned.
var ErrNoRawAudio = errors.New("no raw audio left for the meeting")

// EnrollSpeaker learns the voice of the meeting participant labelled
// speaker and names it. The speaker's track is where the label appears
// within the meeting: the system track for a call, the mic track for a
// speaker diarized during an in-person meeting (see speakerTrack). The
// meeting's speakers are clustered as the pipeline labels them (speakers.go;
// that track's chunks from the cache or diarized again, the other track's
// from the cache), and in each chunk only the chunk-local speakers clustered
// as speaker contribute: their unit embeddings, weighted by their overlap
// with the speaker's utterances, make the chunk's embedding, so crosstalk is
// left out. A label the clustering does not produce (a doc labelled by an
// older version) falls back to each chunk's best-overlapping local speaker.
// A doc labelled by an older version, or a meeting detected
// differently in another hour, may reuse a label for another voice, so the embeddings are grouped by hour (Ruling 37): the hour with
// the most speech under the label is the anchor, and only hours whose
// averaged embedding has cosine >= SpeakerClusterThreshold with the
// anchor's are kept. The kept embeddings are averaged and enrolled as name,
// and the speaker's utterances on that track within the meeting are
// relabelled name in the kept hours only, whose text is then indexed again.
func (p *Pipeline) EnrollSpeaker(ctx context.Context, m store.MeetingRecord, speaker, name string) error {
	if p.Diarizer == nil || p.Voices == nil {
		return errors.New("enroll: no diarizer or voice store configured")
	}
	track := p.speakerTrack(m, speaker)
	chunks, err := p.meetingChunks(m, track)
	if err != nil {
		return err
	}
	if len(chunks) == 0 {
		return ErrNoRawAudio
	}
	tmp, err := os.MkdirTemp("", "workjournal-enroll-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	// The meeting's speakers, clustered as the pipeline labels them: the
	// speaker's track from the cache or diarized again, the other track
	// (system always, mic for an in-person meeting) from the cache only.
	var scope []diarChunk
	diarized := map[string]diarChunk{}
	for _, c := range chunks {
		turns, cembs, err := p.chunkTurns(ctx, tmp, c)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			p.logf("enroll: %v", err)
			continue
		}
		dc := diarChunk{chunk{path: c.path, start: c.start, track: track}, &chunkEntry{Diarized: true, Turns: turns, Embeddings: cembs}}
		diarized[c.path] = dc
		scope = append(scope, dc)
	}
	other := "system"
	if track == "system" {
		other = "mic"
	}
	if other == "system" || m.App == activity.InPersonApp {
		ocs, err := p.meetingChunks(m, other)
		if err != nil {
			return err
		}
		for _, c := range ocs {
			if key, err := statKey(c.path); err == nil {
				if e, ok := p.loadChunk(config.DayKey(c.start), c.path, key); ok && e.Diarized {
					scope = append(scope, diarChunk{chunk{path: c.path, start: c.start, track: other}, e})
				}
			}
		}
	}
	am := activity.Meeting{ID: m.ID, Start: m.Start, End: m.End, App: m.App, Title: m.Title}
	labels := p.clusterScope(scope, meetingWithin(am), map[string][]float32{}).labels
	// A label the clustering does not produce comes from a doc labelled
	// before it (or by a differently detected meeting): then each chunk's
	// local speaker overlapping the label's lines most stands for it.
	legacy := !slices.Contains(slices.Collect(maps.Values(labels)), speaker)
	if legacy {
		p.logf("enroll: %q is not a current speaker of meeting %s; using the best-overlapping voice per chunk", speaker, m.ID)
	}

	docs := map[HourKey]*HourDoc{}
	byHour := map[HourKey][][]float32{}
	for _, c := range chunks {
		dc, ok := diarized[c.path]
		if !ok {
			continue
		}
		key := HourKey{config.DayKey(c.start), c.start.Hour()}
		doc, ok := docs[key]
		if !ok {
			if doc, err = Load(p.Paths.DerivedHour(key.Day, key.Hour)); err != nil {
				p.logf("enroll: %v", err)
			}
			docs[key] = doc
		}
		if doc == nil {
			continue
		}
		var spans [][2]float64 // the speaker's utterances, seconds from chunk start
		for _, u := range doc.Transcript {
			if u.Track == track && u.Speaker == speaker && inMeeting(m, u) &&
				!u.Start.Before(c.start) && u.Start.Before(c.end) {
				spans = append(spans, [2]float64{u.Start.Sub(c.start).Seconds(), u.End.Sub(c.start).Seconds()})
			}
		}
		// Only the chunk-local speakers clustered as speaker count, each by
		// its overlap with the speaker's lines (summed in id order).
		overlap := map[int]float64{}
		for _, t := range dc.e.Turns {
			if !legacy && labels[itemKey{c.path, t.Speaker}] != speaker {
				continue
			}
			for _, s := range spans {
				if ov := min(s[1], t.End) - max(s[0], t.Start); ov > 0 {
					overlap[t.Speaker] += ov
				}
			}
		}
		if legacy && len(overlap) > 0 {
			best := -1
			for _, id := range slices.Sorted(maps.Keys(overlap)) {
				if best < 0 || overlap[id] > overlap[best] {
					best = id
				}
			}
			overlap = map[int]float64{best: overlap[best]}
		}
		var sum []float32
		for _, id := range slices.Sorted(maps.Keys(overlap)) {
			u := unit(dc.e.Embeddings[id])
			if u == nil || (sum != nil && len(u) != len(sum)) {
				continue
			}
			if sum == nil {
				sum = make([]float32, len(u))
			}
			for k, x := range u {
				sum[k] += float32(overlap[id]) * x
			}
		}
		if e := unit(sum); e != nil {
			byHour[key] = append(byHour[key], e)
		}
	}
	if len(byHour) == 0 {
		return fmt.Errorf("enroll: no voice found for %q in the meeting's remaining audio", speaker)
	}
	kept := p.matchingHours(m, track, speaker, docs, byHour)
	var embs [][]float32
	for _, k := range kept {
		embs = append(embs, byHour[k]...)
	}
	if err := p.Voices.Enroll(name, mean(embs)); err != nil {
		return err
	}
	return p.relabel(m, track, speaker, name, kept)
}

// speakerTrack is the track ("system" or "mic") with the most speech
// labelled speaker within the meeting, in the derived hours the meeting
// touches; "system" when the label is found on neither.
func (p *Pipeline) speakerTrack(m store.MeetingRecord, speaker string) string {
	per := map[string]time.Duration{}
	s := m.Start // its recorded wall clock names the derived hours
	for t := time.Date(s.Year(), s.Month(), s.Day(), s.Hour(), 0, 0, 0, s.Location()); t.Before(m.End) || t.Equal(m.End); t = t.Add(time.Hour) {
		doc, err := Load(p.Paths.DerivedHour(config.DayKey(t), t.Hour()))
		if err != nil {
			continue
		}
		for _, u := range doc.Transcript {
			if u.Speaker == speaker && inMeeting(m, u) {
				per[u.Track] += u.End.Sub(u.Start)
			}
		}
	}
	if per["mic"] > per["system"] {
		return "mic"
	}
	return "system"
}

// matchingHours picks the anchor hour (the most speech under speaker within
// the meeting; the earliest on a tie) and returns, in time order, the hours
// whose averaged embedding matches the anchor's at SpeakerClusterThreshold.
func (p *Pipeline) matchingHours(m store.MeetingRecord, track, speaker string, docs map[HourKey]*HourDoc, byHour map[HourKey][][]float32) []HourKey {
	var keys []HourKey
	for k := range byHour {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b HourKey) int {
		return cmp.Or(cmp.Compare(a.Day, b.Day), cmp.Compare(a.Hour, b.Hour))
	})
	speech := func(k HourKey) time.Duration {
		var d time.Duration
		for _, u := range docs[k].Transcript {
			if u.Track == track && u.Speaker == speaker && inMeeting(m, u) {
				d += u.End.Sub(u.Start)
			}
		}
		return d
	}
	anchor := keys[0]
	for _, k := range keys[1:] {
		if speech(k) > speech(anchor) {
			anchor = k
		}
	}
	ref := mean(byHour[anchor])
	var kept []HourKey
	for _, k := range keys {
		if k == anchor || diarize.Cosine(mean(byHour[k]), ref) >= p.Cfg.SpeakerClusterThreshold {
			kept = append(kept, k)
		} else {
			p.logf("enroll: %q in %s %02d sounds like someone else; left unchanged", speaker, k.Day, k.Hour)
		}
	}
	return kept
}

// chunkTurns returns the diarization of c: its cache entry's when current
// and diarized, else the diarizer's on a WAV written into dir.
func (p *Pipeline) chunkTurns(ctx context.Context, dir string, c timedChunk) ([]diarize.Turn, map[int][]float32, error) {
	day := config.DayKey(c.start)
	if key, err := statKey(c.path); err == nil {
		if e, ok := p.loadChunk(day, c.path, key); ok && e.Diarized {
			return e.Turns, e.Embeddings, nil
		}
	}
	base := filepath.Base(c.path)
	wav := filepath.Join(dir, strings.TrimSuffix(base, filepath.Ext(base))+".wav")
	if err := media.ToWav16k(ctx, p.Run, c.path, wav); err != nil {
		return nil, nil, fmt.Errorf("skipping unreadable chunk %s: %w", c.path, err)
	}
	turns, embs, err := p.Diarizer.Diarize(ctx, wav)
	if err != nil {
		return nil, nil, fmt.Errorf("diarize %s: %w", base, err)
	}
	return turns, embs, nil
}

type timedChunk struct {
	path       string
	start, end time.Time
}

// meetingChunks returns the chunks of track still on disk that overlap the
// meeting. A chunk ends chunk_minutes after its start, or at the next
// chunk's start on the track if that is earlier.
func (p *Pipeline) meetingChunks(m store.MeetingRecord, track string) ([]timedChunk, error) {
	length := time.Duration(p.Cfg.AudioChunkMinutes) * time.Minute
	var out []timedChunk
	for _, day := range meetingDays(m) {
		all, err := p.chunks(day)
		if err != nil {
			return nil, err
		}
		var onTrack []chunk
		for _, c := range all {
			if c.track == track {
				onTrack = append(onTrack, c)
			}
		}
		for i, c := range onTrack {
			end := c.start.Add(length)
			if i+1 < len(onTrack) && onTrack[i+1].start.Before(end) {
				end = onTrack[i+1].start
			}
			if c.start.Before(m.End) && end.After(m.Start) {
				out = append(out, timedChunk{path: c.path, start: c.start, end: end})
			}
		}
	}
	return out, nil
}

// meetingDays lists the recorded days from the meeting's start to its end.
func meetingDays(m store.MeetingRecord) []string {
	loc := m.Start.Location()
	s, e := m.Start, m.End.In(loc)
	var days []string
	for d := time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, loc); !d.After(e); d = d.AddDate(0, 0, 1) {
		days = append(days, config.DayKey(d))
	}
	return days
}

// inMeeting is the meeting page's rule: the utterance starts within
// [Start, End].
func inMeeting(m store.MeetingRecord, u media.Utterance) bool {
	return !u.Start.Before(m.Start) && !u.Start.After(m.End)
}

// relabel renames speaker to name on the meeting's utterances on track in
// the given hours, saving each changed doc and indexing its text again.
func (p *Pipeline) relabel(m store.MeetingRecord, track, speaker, name string, hours []HourKey) error {
	for _, k := range hours {
		path := p.Paths.DerivedHour(k.Day, k.Hour)
		doc, err := Load(path)
		if err != nil {
			return err
		}
		changed := false
		for i, u := range doc.Transcript {
			if u.Track == track && u.Speaker == speaker && inMeeting(m, u) {
				doc.Transcript[i].Speaker = name
				changed = true
			}
		}
		if !changed {
			continue
		}
		if err := doc.Save(path); err != nil {
			return fmt.Errorf("save %s: %w", path, err)
		}
		if err := p.IndexTranscript(doc); err != nil {
			return err
		}
	}
	return nil
}

// IndexTranscript replaces the hour's indexed transcript and screen text
// with doc's, leaving the notes and meeting summaries of its summary
// indexed (unlike IndexText).
func (p *Pipeline) IndexTranscript(doc *HourDoc) error {
	if err := p.Store.ReplaceTextKinds(doc.Day, doc.Hour, []string{"transcript", "ocr"}, textDocs(doc)); err != nil {
		return fmt.Errorf("index text: %w", err)
	}
	return nil
}

// IndexText replaces the hour's indexed transcript and OCR text with doc's.
func (p *Pipeline) IndexText(doc *HourDoc) error {
	if err := p.Store.ReplaceText(doc.Day, doc.Hour, textDocs(doc)); err != nil {
		return fmt.Errorf("index text: %w", err)
	}
	return nil
}

// unit returns v scaled to length 1, or nil for an empty or zero vector.
func unit(v []float32) []float32 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	if n == 0 {
		return nil
	}
	n = math.Sqrt(n)
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / n)
	}
	return out
}

// mean averages equal-length vectors element-wise; vectors whose length
// differs from the first are ignored.
func mean(vs [][]float32) []float32 {
	out := make([]float32, len(vs[0]))
	n := 0
	for _, v := range vs {
		if len(v) != len(out) {
			continue
		}
		for i, x := range v {
			out[i] += x
		}
		n++
	}
	for i := range out {
		out[i] /= float32(n)
	}
	return out
}
