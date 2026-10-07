package process

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"time"

	"workjournal/internal/activity"
	"workjournal/internal/config"
	"workjournal/internal/diarize"
	"workjournal/internal/media"
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
	Logf        func(format string, args ...any)
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

// hourBounds returns [start, end) of a local hour.
func hourBounds(day string, hour int) (time.Time, time.Time, error) {
	d, err := time.ParseInLocation("2006-01-02", day, time.Local)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("bad day %q: %w", day, err)
	}
	start := time.Date(d.Year(), d.Month(), d.Day(), hour, 0, 0, 0, time.Local)
	return start, start.Add(time.Hour), nil
}

// ProcessPending transcribes (if needed) and summarizes every pending hour.
// A failing hour is logged, keeps its status, and does not stop the loop;
// the joined errors are returned for logging.
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
			p.logf("process: %v", err)
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
		if errors.Is(err, fs.ErrNotExist) {
			p.logf("process: derived doc for %s %02d missing; transcribing again", h.Day, h.Hour)
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
// sets the hour's status: `transcribed`, or `done` with summary "{}" when the
// hour has no speech, no screen text and no active time. On error nothing
// about the hour's status changes.
func (p *Pipeline) Transcribe(ctx context.Context, day string, hour int) (*HourDoc, error) {
	start, end, err := hourBounds(day, hour)
	if err != nil {
		return nil, err
	}
	events, err := p.events(day)
	if err != nil {
		return nil, err
	}
	segs := activity.Segments(events, start, end)

	chunks, err := p.chunks(day)
	if err != nil {
		return nil, err
	}
	utts, embs, err := p.transcribeAudio(ctx, inHour(chunks, start, end))
	if err != nil {
		return nil, err
	}
	screens, err := p.ocrHour(ctx, day, start, end, segs)
	if err != nil {
		return nil, err
	}
	meetings, err := p.detectMeetings(day, hour, start, end, events, utts)
	if err != nil {
		return nil, err
	}

	doc := &HourDoc{
		Day: day, Hour: hour, Status: store.StatusTranscribed,
		Segments: segs, Transcript: utts, Screens: screens, Meetings: meetings,
		Embeddings: embs,
	}
	for _, m := range meetings {
		if m.End.Before(end) {
			doc.EndedMeetings = append(doc.EndedMeetings, m.ID)
		}
	}
	summary := ""
	if isEmpty(doc) {
		doc.Status, summary = store.StatusDone, "{}"
	}

	if err := p.upsertMeetings(day, meetings); err != nil {
		return nil, err
	}
	if err := p.Store.ReplaceSegments(day, hour, segmentRecords(day, hour, segs)); err != nil {
		return nil, fmt.Errorf("store segments: %w", err)
	}
	if err := p.Store.ReplaceText(day, hour, textDocs(doc, start)); err != nil {
		return nil, fmt.Errorf("index text: %w", err)
	}
	if err := doc.Save(p.Paths.DerivedHour(day, hour)); err != nil {
		return nil, fmt.Errorf("save derived doc: %w", err)
	}
	if err := p.Store.UpsertHour(store.HourRecord{Day: day, Hour: hour, Status: doc.Status, SummaryJSON: summary, UpdatedAt: p.now()}); err != nil {
		return nil, err
	}
	return doc, nil
}

func isEmpty(d *HourDoc) bool {
	if len(d.Transcript) > 0 || len(d.Screens) > 0 {
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

func textDocs(d *HourDoc, start time.Time) []store.TextDoc {
	ref := func(t time.Time) string {
		return fmt.Sprintf("hour/%s/%02d#%d", d.Day, d.Hour, int(t.Sub(start).Seconds()))
	}
	var out []store.TextDoc
	for _, u := range d.Transcript {
		out = append(out, store.TextDoc{Kind: "transcript", Day: d.Day, Hour: d.Hour, TS: u.Start, Text: u.Speaker + ": " + u.Text, Ref: ref(u.Start)})
	}
	for _, s := range d.Screens {
		out = append(out, store.TextDoc{Kind: "ocr", Day: d.Day, Hour: d.Hour, TS: s.Time, Text: s.Text, Ref: ref(s.Time)})
	}
	return out
}

// ocrHour OCRs the hour's screenshots, dropping any taken during an
// excluded, paused or gap segment before OCR sees it.
func (p *Pipeline) ocrHour(ctx context.Context, day string, start, end time.Time, segs []activity.Segment) ([]media.ScreenText, error) {
	shots, err := p.shots(day)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, s := range shots {
		if s.t.Before(start) || !s.t.Before(end) || !visible(segs, s.t) {
			continue
		}
		files = append(files, s.path)
	}
	if len(files) == 0 {
		return nil, nil
	}
	st, err := p.OCR.OCR(ctx, files)
	if err != nil {
		return nil, fmt.Errorf("ocr: %w", err)
	}
	return st, nil
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
