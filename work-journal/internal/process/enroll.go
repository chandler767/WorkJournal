package process

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"workjournal/internal/config"
	"workjournal/internal/media"
	"workjournal/internal/store"
)

// ErrNoRawAudio means none of a meeting's system audio chunks is left on
// disk (retention removed them), so no voice can be learned.
var ErrNoRawAudio = errors.New("no raw system audio left for the meeting")

// EnrollSpeaker learns the voice of the meeting participant labelled
// speaker and names it. Each system chunk overlapping the meeting is
// diarized again; in each, the cluster whose turns best overlap the
// utterances labelled speaker contributes its embedding. The embeddings,
// each unit-normalized, are averaged and enrolled as name. Then the
// speaker's system utterances within the meeting are relabelled name in the
// derived hour docs, and those hours' text is indexed again.
func (p *Pipeline) EnrollSpeaker(ctx context.Context, m store.MeetingRecord, speaker, name string) error {
	if p.Diarizer == nil || p.Voices == nil {
		return errors.New("enroll: no diarizer or voice store configured")
	}
	chunks, err := p.meetingChunks(m)
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

	docs := map[HourKey]*HourDoc{}
	var embs [][]float32
	for _, c := range chunks {
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
			if u.Track == "system" && u.Speaker == speaker && inMeeting(m, u) &&
				!u.Start.Before(c.start) && u.Start.Before(c.end) {
				spans = append(spans, [2]float64{u.Start.Sub(c.start).Seconds(), u.End.Sub(c.start).Seconds()})
			}
		}
		if len(spans) == 0 {
			continue
		}
		base := filepath.Base(c.path)
		wav := filepath.Join(tmp, strings.TrimSuffix(base, filepath.Ext(base))+".wav")
		if err := media.ToWav16k(ctx, p.Run, c.path, wav); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			p.logf("enroll: skipping unreadable chunk %s: %v", c.path, err)
			continue
		}
		turns, cembs, err := p.Diarizer.Diarize(ctx, wav)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			p.logf("enroll: diarize %s: %v", base, err)
			continue
		}
		overlap := map[int]float64{}
		for _, t := range turns {
			for _, s := range spans {
				if ov := min(s[1], t.End) - max(s[0], t.Start); ov > 0 {
					overlap[t.Speaker] += ov
				}
			}
		}
		best, bestOv := -1, 0.0
		for id, ov := range overlap {
			if ov > bestOv || (ov == bestOv && id < best) {
				best, bestOv = id, ov
			}
		}
		if best < 0 {
			continue
		}
		if e := unit(cembs[best]); e != nil {
			embs = append(embs, e)
		}
	}
	if len(embs) == 0 {
		return fmt.Errorf("enroll: no voice found for %q in the meeting's remaining audio", speaker)
	}
	if err := p.Voices.Enroll(name, mean(embs)); err != nil {
		return err
	}
	return p.relabel(m, speaker, name)
}

type timedChunk struct {
	path       string
	start, end time.Time
}

// meetingChunks returns the system chunks still on disk that overlap the
// meeting. A chunk ends chunk_minutes after its start, or at the next
// system chunk's start if that is earlier.
func (p *Pipeline) meetingChunks(m store.MeetingRecord) ([]timedChunk, error) {
	length := time.Duration(p.Cfg.AudioChunkMinutes) * time.Minute
	var out []timedChunk
	for _, day := range meetingDays(m) {
		all, err := p.chunks(day)
		if err != nil {
			return nil, err
		}
		var sys []chunk
		for _, c := range all {
			if c.track == "system" {
				sys = append(sys, c)
			}
		}
		for i, c := range sys {
			end := c.start.Add(length)
			if i+1 < len(sys) && sys[i+1].start.Before(end) {
				end = sys[i+1].start
			}
			if c.start.Before(m.End) && end.After(m.Start) {
				out = append(out, timedChunk{path: c.path, start: c.start, end: end})
			}
		}
	}
	return out, nil
}

// meetingDays lists the local days from the meeting's start to its end.
func meetingDays(m store.MeetingRecord) []string {
	s, e := m.Start.In(time.Local), m.End.In(time.Local)
	var days []string
	for d := time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, time.Local); !d.After(e); d = d.AddDate(0, 0, 1) {
		days = append(days, config.DayKey(d))
	}
	return days
}

// inMeeting is the meeting page's rule: the utterance starts within
// [Start, End].
func inMeeting(m store.MeetingRecord, u media.Utterance) bool {
	return !u.Start.Before(m.Start) && !u.Start.After(m.End)
}

// relabel renames speaker to name on the meeting's system utterances in
// every hour the meeting touches, saving each changed doc and indexing its
// text again.
func (p *Pipeline) relabel(m store.MeetingRecord, speaker, name string) error {
	s := m.Start.In(time.Local)
	for h := time.Date(s.Year(), s.Month(), s.Day(), s.Hour(), 0, 0, 0, time.Local); !h.After(m.End); h = h.Add(time.Hour) {
		day, hour := config.DayKey(h), h.Hour()
		path := p.Paths.DerivedHour(day, hour)
		doc, err := Load(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		changed := false
		for i, u := range doc.Transcript {
			if u.Track == "system" && u.Speaker == speaker && inMeeting(m, u) {
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
		if err := p.IndexText(doc); err != nil {
			return err
		}
	}
	return nil
}

// IndexText replaces the hour's indexed transcript and OCR text with doc's.
func (p *Pipeline) IndexText(doc *HourDoc) error {
	start, _, err := hourBounds(doc.Day, doc.Hour)
	if err != nil {
		return err
	}
	if err := p.Store.ReplaceText(doc.Day, doc.Hour, textDocs(doc, start)); err != nil {
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
