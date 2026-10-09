package process

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"workjournal/internal/activity"
	"workjournal/internal/config"
	"workjournal/internal/diarize"
	"workjournal/internal/media"
)

// The diarizer runs on one chunk at a time, and on room audio it splits one
// voice into many chunk-local speakers. Labels therefore come from
// clustering every chunk-local speaker within a scope: each meeting (over
// all its chunks, in every hour it spans, so a voice keeps its number
// across hours) and the hour's speech outside meetings.
//
// In a scope, each (chunk, local speaker) is an item weighted by its seconds
// of turns in the scope; diarize.Cluster groups them at
// speaker_cluster_threshold and turns clusters with less than
// speaker_min_seconds into "Speaker ?" or part of their nearest voice. A
// cluster whose duration-weighted mean embedding matches an enrolled voice
// takes its name ("me" for the user's own); the others are "Speaker N", N
// being the cluster's position in order of first appearance among all the
// scope's clusters, so enrolling one voice never renumbers the rest.

// diarChunk is a chunk with a successful diarization.
type diarChunk struct {
	c chunk
	e *chunkEntry
}

// itemKey is one chunk-local speaker.
type itemKey struct {
	chunk string // chunk path
	local int
}

// speakerScope labels the chunk-local speakers of one scope.
type speakerScope struct {
	labels map[itemKey]string
	chunks map[string]diarChunk // the scope's chunks, by path
	// Seconds of mic turns in the scope by label, counting only speakers
	// outside fragments (see diarize.ClusterFragments), and in fragments.
	micSeconds  map[string]float64
	micFragment float64
}

// speakerSet holds the labels of every scope of one hour.
type speakerSet struct {
	meetings []activity.Meeting
	scopes   []*speakerScope // scopes[i] is meetings[i]'s
	hour     *speakerScope   // system speech outside meetings
	embs     map[string][]float32
}

// newSpeakers clusters the speakers of the hour [start, end): turns are its
// diarized chunks (system ones, and mic ones of in-person meetings); a
// meeting's scope adds the cached diarization of its chunks in other hours.
func (p *Pipeline) newSpeakers(day string, start, end time.Time, meetings []activity.Meeting, turns []diarChunk) *speakerSet {
	sp := &speakerSet{meetings: meetings, embs: map[string][]float32{}}
	for _, m := range meetings {
		var cs []diarChunk
		for _, dc := range turns {
			if dc.c.track == "system" || m.App == activity.InPersonApp {
				cs = append(cs, dc)
			}
		}
		cs = append(cs, p.otherHourChunks(m, start, end)...)
		sp.scopes = append(sp.scopes, p.clusterScope(cs, meetingWithin(m), sp.embs))
	}
	var sys []diarChunk
	for _, dc := range turns {
		if dc.c.track == "system" {
			sys = append(sys, dc)
		}
	}
	outside := func(from, to time.Time) float64 {
		sec := to.Sub(from).Seconds()
		for _, m := range meetings {
			sec -= overlap(from, to, m.Start, m.End)
		}
		return sec
	}
	sp.hour = p.clusterScope(sys, outside, sp.embs)
	return sp
}

// meetingWithin measures a span's seconds within meeting m.
func meetingWithin(m activity.Meeting) func(from, to time.Time) float64 {
	return func(from, to time.Time) float64 { return overlap(from, to, m.Start, m.End) }
}

// scopeOf returns the scope of the meeting with id, or nil.
func (sp *speakerSet) scopeOf(id string) *speakerScope {
	for i, m := range sp.meetings {
		if m.ID == id {
			return sp.scopes[i]
		}
	}
	return nil
}

// overlap is the seconds [from, to) shares with [s, e].
func overlap(from, to, s, e time.Time) float64 {
	if from.Before(s) {
		from = s
	}
	if to.After(e) {
		to = e
	}
	return max(0, to.Sub(from).Seconds())
}

// otherHourChunks returns the cached diarization of meeting m's chunks
// outside the hour [start, end): the system track, and for an in-person
// meeting the mic track too. A chunk without a current, diarized cache entry
// is left out.
func (p *Pipeline) otherHourChunks(m activity.Meeting, start, end time.Time) []diarChunk {
	length := time.Duration(p.Cfg.AudioChunkMinutes) * time.Minute
	var out []diarChunk
	s, e := m.Start.In(time.Local), m.End.In(time.Local)
	for d := time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, time.Local); !d.After(e); d = d.AddDate(0, 0, 1) {
		day := config.DayKey(d)
		all, err := p.chunks(day)
		if err != nil {
			p.logf("process: meeting %s chunks of %s: %v", m.ID, day, err)
			continue
		}
		for i, c := range all {
			if c.track == "mic" && m.App != activity.InPersonApp {
				continue
			}
			if !c.start.Before(start) && c.start.Before(end) {
				continue // this hour's: the caller has them
			}
			cend := c.start.Add(length)
			for _, n := range all[i+1:] {
				if n.track == c.track && n.start.After(c.start) {
					if n.start.Before(cend) {
						cend = n.start
					}
					break
				}
			}
			if !c.start.Before(m.End) || !cend.After(m.Start) {
				continue
			}
			key, err := statKey(c.path)
			if err != nil {
				continue
			}
			if ce, ok := p.loadChunk(day, c.path, key); ok && ce.Diarized {
				out = append(out, diarChunk{c, ce})
			}
		}
	}
	return out
}

// clusterScope clusters the chunk-local speakers of cs, each weighted by the
// seconds of its turns within the scope (within), and labels them. Named
// clusters' mean embeddings are added to embs under their label unless the
// label is already there.
func (p *Pipeline) clusterScope(cs []diarChunk, within func(from, to time.Time) float64, embs map[string][]float32) *speakerScope {
	type item struct {
		key     itemKey
		track   string
		emb     []float32
		seconds float64
		first   time.Time
	}
	byKey := map[itemKey]*item{}
	var items []*item
	for _, dc := range cs {
		for _, t := range dc.e.Turns {
			from := dc.c.start.Add(secs(t.Start))
			to := dc.c.start.Add(secs(t.End))
			sec := within(from, to)
			if sec <= 0 {
				continue
			}
			k := itemKey{dc.c.path, t.Speaker}
			it, ok := byKey[k]
			if !ok {
				it = &item{key: k, track: dc.c.track, emb: dc.e.Embeddings[t.Speaker], first: from}
				byKey[k] = it
				items = append(items, it)
			}
			it.seconds += sec
			if from.Before(it.first) {
				it.first = from
			}
		}
	}
	slices.SortStableFunc(items, func(a, b *item) int {
		return cmp.Or(a.first.Compare(b.first), cmp.Compare(a.key.chunk, b.key.chunk), cmp.Compare(a.key.local, b.key.local))
	})
	in := make([]diarize.ClusterItem, len(items))
	for i, it := range items {
		in[i] = diarize.ClusterItem{Emb: it.emb, Seconds: it.seconds}
	}
	ids, fragment := diarize.ClusterFragments(in, p.Cfg.SpeakerClusterThreshold, float64(p.Cfg.SpeakerMinSeconds))

	// Duration-weighted mean of each cluster's unit embeddings.
	sums := map[int][]float32{}
	for i, id := range ids {
		u := unit(items[i].emb)
		if id < 0 || u == nil {
			continue
		}
		sum, ok := sums[id]
		if !ok {
			sum = make([]float32, len(u))
			sums[id] = sum
		}
		if len(u) != len(sum) {
			continue
		}
		for k, x := range u {
			sum[k] += float32(items[i].seconds) * x
		}
	}
	names := map[int]string{}
	sc := &speakerScope{labels: map[itemKey]string{}, chunks: map[string]diarChunk{}, micSeconds: map[string]float64{}}
	for _, dc := range cs {
		sc.chunks[dc.c.path] = dc
	}
	for i, id := range ids {
		if items[i].track == "mic" && fragment[i] {
			sc.micFragment += items[i].seconds
		}
		if id < 0 {
			sc.labels[items[i].key] = unknownSpeaker
			continue
		}
		name, ok := names[id]
		if !ok {
			centroid := unit(sums[id])
			name = fmt.Sprintf("Speaker %d", id+1)
			if p.Voices != nil && centroid != nil {
				if known, _, ok := p.Voices.Match(centroid); ok {
					name = known
				}
			}
			names[id] = name
			if _, ok := embs[name]; !ok && centroid != nil {
				embs[name] = centroid
			}
		}
		sc.labels[items[i].key] = name
		if items[i].track == "mic" && !fragment[i] {
			sc.micSeconds[name] += items[i].seconds
		}
	}
	return sc
}

func secs(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

// label sets Speaker on the utterances of chunk c that only accepts (all
// when only is nil): the label, in the scope the utterance starts in, of
// the chunk-local speaker whose turn overlaps it most; fallback when no turn
// overlaps it.
func (sp *speakerSet) label(c chunk, e *chunkEntry, us []media.Utterance, only func(media.Utterance) bool, fallback string) {
	for i, u := range us {
		if only != nil && !only(u) {
			continue
		}
		from := u.Start.Sub(c.start).Seconds()
		to := u.End.Sub(c.start).Seconds()
		best, bestOv := -1, 0.0
		for j, t := range e.Turns {
			if ov := min(to, t.End) - max(from, t.Start); ov > bestOv {
				best, bestOv = j, ov
			}
		}
		if best < 0 {
			us[i].Speaker = fallback
			continue
		}
		sc := sp.hour
		for k, m := range sp.meetings {
			if inMeetingSpan(m, u) && (c.track == "system" || m.App == activity.InPersonApp) {
				sc = sp.scopes[k]
				break
			}
		}
		lab, ok := sc.labels[itemKey{c.path, e.Turns[best].Speaker}]
		if !ok {
			lab = unknownSpeaker
		}
		us[i].Speaker = lab
	}
}

// relabelMeetings applies doc's meeting labels (doc.speakers) to each of
// its meetings' utterances in the earlier hours' derived docs: a pass that
// sees more of a meeting may cluster it differently, and the meeting must
// read with one numbering (its summary input spans those hours). Changed
// docs are saved and their transcript indexed again. Then the meeting's
// unconfirmed participants whose label is heard in none of its hours are
// removed.
func (p *Pipeline) relabelMeetings(doc *HourDoc) error {
	sp := doc.speakers
	if sp == nil {
		return nil
	}
	hourStart, _, err := hourBounds(doc.Day, doc.Hour)
	if err != nil {
		return err
	}
	chunksOf := map[string][]chunk{}
	for k, m := range sp.meetings {
		heard := map[string]bool{}
		note := func(d *HourDoc) {
			for _, u := range visibleSpeech(d) {
				if inMeetingSpan(m, u) {
					heard[speaker(u)] = true
				}
			}
		}
		note(doc)
		s := m.Start.In(time.Local)
		for t := time.Date(s.Year(), s.Month(), s.Day(), s.Hour(), 0, 0, 0, time.Local); !t.After(m.End); t = t.Add(time.Hour) {
			if t.Equal(hourStart) {
				continue
			}
			day := config.DayKey(t)
			path := p.Paths.DerivedHour(day, t.Hour())
			d, err := Load(path)
			if err != nil {
				continue
			}
			if t.Before(hourStart) {
				all, ok := chunksOf[day]
				if !ok {
					if all, err = p.chunks(day); err != nil {
						return err
					}
					chunksOf[day] = all
				}
				if p.relabelDoc(d, m, sp, sp.scopes[k], all) {
					if err := d.Save(path); err != nil {
						return fmt.Errorf("save %s: %w", path, err)
					}
					if err := p.IndexTranscript(d); err != nil {
						return err
					}
				}
			}
			note(d)
		}
		parts, err := p.Store.Participants(m.ID)
		if err != nil {
			return err
		}
		for _, r := range parts {
			if !r.Confirmed && !heard[r.Speaker] {
				if err := p.Store.DeleteParticipant(m.ID, r.Speaker); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// relabelDoc labels d's utterances in meeting m (mic ones only for an
// in-person meeting) from their chunk's turns in sc, reporting whether any
// label changed. all is the day's chunks; an utterance whose chunk is not in
// the scope keeps its label.
func (p *Pipeline) relabelDoc(d *HourDoc, m activity.Meeting, sp *speakerSet, sc *speakerScope, all []chunk) bool {
	changed := false
	for i, u := range d.Transcript {
		if !inMeetingSpan(m, u) || (u.Track == "mic" && m.App != activity.InPersonApp) {
			continue
		}
		var c *chunk
		for j := range all {
			if all[j].track == u.Track && !all[j].start.After(u.Start) && (c == nil || all[j].start.After(c.start)) {
				c = &all[j]
			}
		}
		if c == nil {
			continue
		}
		dc, ok := sc.chunks[c.path]
		if !ok {
			continue
		}
		fallback := "others"
		if u.Track == "mic" {
			fallback = unknownSpeaker
		}
		us := []media.Utterance{u}
		sp.label(dc.c, dc.e, us, nil, fallback)
		if us[0].Speaker != u.Speaker {
			d.Transcript[i].Speaker = us[0].Speaker
			changed = true
		}
	}
	return changed
}
