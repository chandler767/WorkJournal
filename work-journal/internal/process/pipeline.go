package process

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"workjournal/internal/activity"
	"workjournal/internal/config"
	"workjournal/internal/diarize"
	"workjournal/internal/media"
	"workjournal/internal/skills"
	"workjournal/internal/store"
)

// HourKey identifies one local hour.
type HourKey struct {
	Day  string
	Hour int
}

// HourSummarizer summarizes a transcribed hour and records the result
// (Task 11). It is responsible for moving the hour past `transcribed`.
type HourSummarizer interface {
	SummarizeHour(ctx context.Context, doc *HourDoc) error
}

// Pipeline processes finished hours of raw recordings.
type Pipeline struct {
	Cfg         config.Config
	Paths       config.Paths
	Store       *store.Store
	Transcriber media.Transcriber
	OCR         media.OCR
	Diarizer    diarize.Diarizer // nil or Cfg.Diarization "off": system speech is "others"
	Voices      *diarize.Voices  // nil: no known-voice naming
	Run         media.Runner
	Now         func() time.Time
	Summarize   HourSummarizer // nil: hours stay `transcribed`
	// Privacy runs the privacy-filter skill (privacy.go); nil: no filter.
	Privacy skills.Runner
	Logf    func(format string, args ...any)
}

func (p *Pipeline) now() time.Time {
	if p.Now != nil {
		return p.Now().In(time.Local)
	}
	return time.Now()
}

func (p *Pipeline) logf(format string, args ...any) {
	if p.Logf != nil {
		p.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// hourBounds returns [start, end) of a recorded wall-clock hour: raw file
// names and hour keys are the wall clock where they were recorded, not the
// Mac's current zone (see dayClock.bounds).
func hourBounds(paths config.Paths, day string, hour int) (time.Time, time.Time, error) {
	c, _, err := readDayClock(paths, day)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return c.bounds(hour)
}

// ProcessPending transcribes (if needed) and summarizes every pending hour.
// A failing hour is logged (unless Claude is usage-limited), keeps its
// status, and does not stop the loop; the joined errors are returned for
// logging.
func (p *Pipeline) ProcessPending(ctx context.Context) error {
	hours, err := p.PendingHours()
	if err != nil {
		return err
	}
	var errs []error
	for _, h := range hours {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		if err := p.processHour(ctx, h); err != nil {
			err = fmt.Errorf("hour %s %02d: %w", h.Day, h.Hour, err)
			// A Claude usage limit is logged once by the scheduler, not per hour.
			if !errors.As(err, new(skills.ErrUsageLimited)) {
				p.logf("process: %v", err)
			}
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (p *Pipeline) processHour(ctx context.Context, h HourKey) error {
	rec, ok, err := p.Store.Hour(h.Day, h.Hour)
	if err != nil {
		return err
	}
	var doc *HourDoc
	if ok && rec.Status == store.StatusTranscribed {
		doc, err = Load(p.Paths.DerivedHour(h.Day, h.Hour))
		if err != nil {
			// Missing or unparseable (e.g. truncated by a crash): rebuild it.
			p.logf("process: derived doc for %s %02d unusable (%v); transcribing again", h.Day, h.Hour, err)
			doc, err = p.Transcribe(ctx, h.Day, h.Hour)
		}
	} else {
		doc, err = p.Transcribe(ctx, h.Day, h.Hour)
	}
	if err != nil {
		return err
	}
	if doc.Status != store.StatusTranscribed || p.Summarize == nil {
		return nil
	}
	return p.Summarize.SummarizeHour(ctx, doc)
}

// Transcribe builds the hour's HourDoc from raw data, writes the derived
// JSON, indexes transcript and OCR text, upserts meetings and segments, and
// sets the hour's status: `transcribed` (keeping a partial hour's summary so
// far), `needs_review` without a summary when the privacy filter's output
// was invalid (speech withheld), or `done` with summary "{}" when the
// hour has no speech, no screen text, no active time and ends no meeting. On error nothing
// about the hour's status changes.
func (p *Pipeline) Transcribe(ctx context.Context, day string, hour int) (*HourDoc, error) {
	doc, _, err := p.build(ctx, day, hour, time.Time{}, time.Time{})
	if err != nil {
		return nil, err
	}
	summary := ""
	if doc.SpeechWithheld {
		// needs_review without a summary; Reprocess tries again.
	} else if isEmpty(doc) {
		doc.Status, summary = store.StatusDone, "{}"
	} else if rec, ok, err := p.Store.Hour(day, hour); err != nil {
		return nil, err
	} else if ok && rec.Status == store.StatusPartial {
		// Keep showing the summary so far until the final one succeeds.
		summary = rec.SummaryJSON
	}
	if err := p.record(doc, summary); err != nil {
		return nil, err
	}
	return doc, nil
}

// build assembles the HourDoc of a finished hour from raw data (upto zero),
// or of the in-progress hour from the inputs ready at upto (see
// ProcessCurrent; chunks started before a non-zero rotatedAt count as
// finished). It also returns the time up to which the doc's audio is
// complete. It writes nothing but the processing cache.
func (p *Pipeline) build(ctx context.Context, day string, hour int, upto, rotatedAt time.Time) (*HourDoc, time.Time, error) {
	clock, err := p.dayClock(day)
	if err != nil {
		return nil, time.Time{}, err
	}
	start, end, err := clock.bounds(hour)
	if err != nil {
		return nil, time.Time{}, err
	}
	midnight, err := clock.zones.Date(day, 0, 0, 0)
	if err != nil {
		return nil, time.Time{}, err
	}
	partial := !upto.IsZero()
	cut := end
	if partial {
		cut = earlier(upto, end)
	}
	events, err := p.stateEvents(clock)
	if err != nil {
		return nil, time.Time{}, err
	}
	segs := activity.Segments(events, start, cut)

	all := clock.chunks
	chunks, covered := inHour(all, start, end), cut
	if partial {
		chunks, covered = readyChunks(all, chunks, upto, rotatedAt)
		covered = earlier(covered, cut)
	}
	var wavs wavDir
	defer wavs.close()
	audio, err := p.loadAudio(ctx, day, chunks, partial, &wavs)
	if err != nil {
		return nil, time.Time{}, err
	}
	screens, err := p.ocrHour(ctx, day, clock.shots, start, cut, segs)
	if err != nil {
		return nil, time.Time{}, err
	}
	speech := audibleSpeech(segs, speechOf(audio))
	detected, err := p.detectMeetings(day, hour, midnight, start, cut, events, speech)
	if err != nil {
		return nil, time.Time{}, err
	}
	lab, utts, meetings, err := p.labelHour(ctx, day, hour, start, end, audio, detected, segs, &wavs)
	if err != nil {
		return nil, time.Time{}, err
	}
	p.warnDiarization(day, hour, &wavs)
	h := &hearing{lab: lab, utts: utts, meetings: meetings}
	if p.privacyOn() && len(utts) > 0 {
		in := hourAudio{day: day, hour: hour, midnight: midnight, start: start, end: end, cut: cut, events: events,
			segs: segs, audio: audio, speech: speech, detected: detected, wavs: &wavs}
		if h, err = p.filterHour(ctx, in, h, partial); err != nil {
			return nil, time.Time{}, err
		}
	}
	lab, utts, meetings = h.lab, h.utts, h.meetings
	// Only the part of a segment inside a meeting is meeting time.
	segs = activity.SplitAtMeetings(segs, meetings)

	doc := &HourDoc{
		Day: day, Hour: hour, Status: store.StatusTranscribed,
		Segments: segs, Transcript: utts, Screens: screens, Meetings: meetings,
		Removed: h.removed, SpeechWithheld: h.withheld, Embeddings: lab.embs, speakers: lab.sp,
	}
	if h.withheld {
		doc.Status = store.StatusNeedsReview
		return doc, covered, nil // no meeting summaries either
	}
	if partial {
		doc.Status = store.StatusPartial
		if doc.EndedMeetings, err = p.endedSoFar(meetings, covered); err != nil {
			return nil, time.Time{}, err
		}
		return doc, covered, nil
	}
	for _, m := range meetings {
		if m.End.Before(end) {
			doc.EndedMeetings = append(doc.EndedMeetings, m.ID)
		}
	}
	return doc, covered, nil
}

// labelHour labels the hour's audio for the detected meetings, drops the
// in-person meetings with one voice (their mic speech is the user's) and
// returns the audible labelled speech and the meetings kept.
func (p *Pipeline) labelHour(ctx context.Context, day string, hour int, start, end time.Time, audio []audioChunk, meetings []activity.Meeting, segs []activity.Segment, wavs *wavDir) (*labelled, []media.Utterance, []activity.Meeting, error) {
	meetings = slices.Clone(meetings)
	var inPerson []activity.Meeting
	for _, m := range meetings {
		if m.App == activity.InPersonApp {
			inPerson = append(inPerson, m)
		}
	}
	lab, err := p.labelAudio(ctx, day, start, end, audio, meetings, wavs)
	if err != nil {
		return nil, nil, nil, err
	}
	utts := lab.utts
	for _, m := range inPerson {
		if p.keepInPerson(day, hour, m, lab) {
			continue
		}
		// One voice is dictation or a call on another device, not a meeting.
		meetings = slices.DeleteFunc(meetings, func(x activity.Meeting) bool { return x.ID == m.ID })
		for i, u := range utts {
			if u.Track == "mic" && inMeetingSpan(m, u) {
				utts[i].Speaker = "me"
			}
		}
	}
	return lab, audibleSpeech(segs, utts), meetings, nil
}

// sameMeetings reports whether two detections found the same meetings.
func sameMeetings(a, b []activity.Meeting) bool {
	return slices.EqualFunc(a, b, func(x, y activity.Meeting) bool {
		return x.ID == y.ID && x.App == y.App && x.Start.Equal(y.Start) && x.End.Equal(y.End)
	})
}

// record stores doc: meetings, segments, indexed text, the derived JSON,
// the meeting labels of earlier hours (relabelMeetings) and the hour record
// with doc.Status and summaryJSON.
func (p *Pipeline) record(doc *HourDoc, summaryJSON string) error {
	if err := p.upsertMeetings(doc); err != nil {
		return err
	}
	if err := p.Store.ReplaceSegments(doc.Day, doc.Hour, segmentRecords(doc.Day, doc.Hour, doc.Segments)); err != nil {
		return fmt.Errorf("store segments: %w", err)
	}
	if err := p.IndexText(doc); err != nil {
		return err
	}
	if err := doc.Save(p.Paths.DerivedHour(doc.Day, doc.Hour)); err != nil {
		return fmt.Errorf("save derived doc: %w", err)
	}
	if err := p.relabelMeetings(doc); err != nil {
		return err
	}
	return p.Store.UpsertHour(store.HourRecord{Day: doc.Day, Hour: doc.Hour, Status: doc.Status, SummaryJSON: summaryJSON, UpdatedAt: p.now()})
}

func isEmpty(d *HourDoc) bool {
	if len(d.Transcript) > 0 || len(d.Screens) > 0 || len(d.EndedMeetings) > 0 {
		return false
	}
	for _, s := range d.Segments {
		if s.Kind == activity.KindActive {
			return false
		}
	}
	return true
}

func segmentRecords(day string, hour int, segs []activity.Segment) []store.SegmentRecord {
	out := make([]store.SegmentRecord, 0, len(segs))
	for _, s := range segs {
		out = append(out, store.SegmentRecord{
			Day: day, Hour: hour, Start: s.Start, End: s.End,
			App: s.App, Title: s.Title, Kind: s.Kind, Source: "auto",
		})
	}
	return out
}

// textDocs is the hour's searchable transcript and screen text. Speech during
// an excluded or paused span is never indexed.
func textDocs(d *HourDoc, start time.Time) []store.TextDoc {
	ref := func(t time.Time) string {
		return fmt.Sprintf("hour/%s/%02d#%d", d.Day, d.Hour, int(t.Sub(start).Seconds()))
	}
	var out []store.TextDoc
	for _, u := range visibleSpeech(d) {
		out = append(out, store.TextDoc{Kind: "transcript", Day: d.Day, Hour: d.Hour, TS: u.Start, Text: u.Speaker + ": " + u.Text, Ref: ref(u.Start)})
	}
	for _, s := range d.Screens {
		out = append(out, store.TextDoc{Kind: "ocr", Day: d.Day, Hour: d.Hour, TS: s.Time, Text: s.Text, Ref: ref(s.Time)})
	}
	return out
}

// ocrHour returns the screen text of the screenshots taken in [start, end),
// dropping any taken during an excluded, paused or gap segment before OCR
// sees it. Results come from the processing cache where the file is
// unchanged; the rest are OCRed in one batch and cached. Empty texts are
// dropped and consecutive identical texts collapse to the first.
func (p *Pipeline) ocrHour(ctx context.Context, day string, shots []shot, start, end time.Time, segs []activity.Segment) ([]media.ScreenText, error) {
	var hour []shot
	for _, s := range shots {
		if s.t.Before(start) || !s.t.Before(end) || s.t.Hour() != start.Hour() || !visible(segs, s.t) {
			continue
		}
		hour = append(hour, s)
	}
	if len(hour) == 0 {
		return nil, nil
	}
	cache := p.loadOCRCache(day)
	var missing []string
	stale := map[string]fileKey{}
	for _, s := range hour {
		key, err := statKey(s.path)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", s.path, err)
		}
		if e, ok := cache.Files[filepath.Base(s.path)]; !ok || e.Key != key {
			missing = append(missing, s.path)
			stale[s.path] = key
		}
	}
	if len(missing) > 0 {
		texts, err := p.ocrFiles(ctx, missing)
		if err != nil {
			return nil, fmt.Errorf("ocr: %w", err)
		}
		for _, path := range missing {
			cache.Files[filepath.Base(path)] = ocrEntry{Key: stale[path], Text: texts[path]}
		}
		p.saveOCRCache(day, cache)
	}
	var out []media.ScreenText
	last := ""
	for _, s := range hour {
		text := cache.Files[filepath.Base(s.path)].Text
		if text == "" || (len(out) > 0 && text == last) {
			continue
		}
		out = append(out, media.ScreenText{Time: s.t, File: s.path, Text: text})
		last = text
	}
	return out, nil
}

// ocrFiles returns each file's OCR text ("" for none), keyed by path.
func (p *Pipeline) ocrFiles(ctx context.Context, files []string) (map[string]string, error) {
	if fo, ok := p.OCR.(media.FileOCR); ok {
		return fo.OCRFiles(ctx, files)
	}
	st, err := p.OCR.OCR(ctx, files)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(files))
	for _, s := range st {
		out[s.File] = strings.TrimSpace(s.Text)
	}
	return out, nil
}

// audibleSpeech drops utterances overlapping an excluded or paused segment,
// so they never reach the derived doc (readable by Claude), the index or a
// skill. The cache keeps the raw whisper output; it is outside derived/.
func audibleSpeech(segs []activity.Segment, us []media.Utterance) []media.Utterance {
	return visibleSpeech(&HourDoc{Segments: segs, Transcript: us})
}

// visible reports whether t falls in an active or idle segment.
func visible(segs []activity.Segment, t time.Time) bool {
	for _, s := range segs {
		if !t.Before(s.Start) && t.Before(s.End) {
			return s.Kind == activity.KindActive || s.Kind == activity.KindIdle
		}
	}
	return false
}
