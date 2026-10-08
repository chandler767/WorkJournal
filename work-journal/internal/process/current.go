package process

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"workjournal/internal/activity"
	"workjournal/internal/config"
	"workjournal/internal/store"
)

// meetingSettle is how long after a meeting's detected end the in-progress
// hour waits before treating it as ended: detection merges candidates less
// than two minutes apart, so a meeting can still grow until then.
const meetingSettle = 2 * time.Minute

// InputHasher is implemented by summarizers that can fingerprint the skill
// inputs they would build for doc without running anything.
type InputHasher interface {
	InputHash(doc *HourDoc) (string, error)
}

// PartialInputHashKey is the KV key holding the fingerprint of the skill
// inputs last summarized for the in-progress hour.
func PartialInputHashKey(day string, hour int) string {
	return fmt.Sprintf("partial_input_hash:%sT%02d", day, hour)
}

// ProcessCurrent summarizes the in-progress hour (the local hour of now) from
// the inputs ready at now and marks it `partial`; the hour gets the normal
// final pass once it has ended. Audio and OCR come from the processing cache
// where possible. Nothing is written when the hour has no content yet, and
// when the skill inputs equal those of the previous partial run the
// summarizer is not run again (the existing summary stays).
func (p *Pipeline) ProcessCurrent(ctx context.Context, now time.Time) error {
	now = now.In(time.Local)
	day, hour := config.DayKey(now), now.Hour()
	rec, found, err := p.Store.Hour(day, hour)
	if err != nil {
		return err
	}
	if found && rec.Status != store.StatusPartial && rec.Status != store.StatusPending {
		return nil // already processed in full (or being processed)
	}
	doc, err := p.build(ctx, day, hour, now)
	if err != nil {
		return err
	}
	if isEmpty(doc) {
		return nil
	}
	hash, err := p.inputHash(doc)
	if err != nil {
		return err
	}
	key := PartialInputHashKey(day, hour)
	if found && rec.Status == store.StatusPartial {
		if prev, _, err := p.Store.GetKV(key); err != nil {
			return err
		} else if prev == hash {
			return nil
		}
	}
	if err := p.record(doc, rec.SummaryJSON); err != nil {
		return err
	}
	if p.Summarize == nil {
		return nil
	}
	if err := p.Summarize.SummarizeHour(ctx, doc); err != nil {
		return err
	}
	return p.Store.SetKV(key, hash)
}

// inputHash fingerprints what the summarizer would see: its own hash when it
// offers one, else the whole derived doc.
func (p *Pipeline) inputHash(doc *HourDoc) (string, error) {
	if h, ok := p.Summarize.(InputHasher); ok {
		return h.InputHash(doc)
	}
	return hashJSON(doc)
}

func hashJSON(vs ...any) (string, error) {
	sum := sha256.New()
	for _, v := range vs {
		b, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		sum.Write(b)
		sum.Write([]byte{'\n'})
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// readyChunks returns the chunks of the in-progress hour that are finished at
// now: every chunk but the newest of its track, and the newest once its mtime
// is at least freshAge old. covered is the start of the earliest chunk held
// back (or now): speech after it is not known yet.
func readyChunks(day, hour []chunk, now time.Time) (ready []chunk, covered time.Time) {
	newest := map[string]chunk{}
	for _, c := range day {
		if n, ok := newest[c.track]; !ok || !c.start.Before(n.start) {
			newest[c.track] = c
		}
	}
	covered = now
	for _, c := range hour {
		if !c.start.Before(now) {
			continue
		}
		if newest[c.track].path == c.path {
			fi, err := os.Stat(c.path)
			if err != nil || now.Sub(fi.ModTime()) < freshAge {
				covered = earlier(covered, c.start)
				continue
			}
		}
		ready = append(ready, c)
	}
	return ready, covered
}

// endedSoFar lists the meetings of the in-progress hour that have ended for
// good by covered (the time up to which speech is known) and have no summary
// yet, so each meeting is summarized once here and again only in the hour's
// final pass.
func (p *Pipeline) endedSoFar(ms []activity.Meeting, covered time.Time) ([]string, error) {
	var out []string
	for _, m := range ms {
		if m.End.Add(meetingSettle).After(covered) {
			continue
		}
		rec, ok, err := p.Store.Meeting(m.ID)
		if err != nil {
			return nil, err
		}
		if ok && rec.SummaryJSON != "" {
			continue
		}
		out = append(out, m.ID)
	}
	return out, nil
}

func earlier(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}
