package process

import (
	"errors"
	"io/fs"
	"time"

	"workjournal/internal/activity"
	"workjournal/internal/media"
	"workjournal/internal/store"
)

// detectMeetings runs meeting detection over [local midnight, end) using the
// whole day's activity and the speech of this hour plus the day's earlier
// derived hours, and returns the meetings overlapping [start, end);
// a meeting ending exactly at start is included so the hour after its last
// one can report it ended. events must include the previous day's (see
// stateEvents).
func (p *Pipeline) detectMeetings(day string, hour int, start, end time.Time, events []activity.Event, utts []media.Utterance) ([]activity.Meeting, error) {
	midnight := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.Local)
	segs := activity.Segments(events, midnight, end)
	var speech []activity.SpeechSpan
	for h := 0; h < hour; h++ {
		d, err := Load(p.Paths.DerivedHour(day, h))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			p.logf("process: meeting speech from %s %02d: %v", day, h, err)
			continue
		}
		speech = appendSpeech(speech, d.Transcript)
	}
	speech = appendSpeech(speech, utts)
	var out []activity.Meeting
	for _, m := range activity.DetectMeetings(segs, speech, p.Cfg) {
		if !m.End.Before(start) && m.Start.Before(end) {
			out = append(out, m)
		}
	}
	return out, nil
}

func appendSpeech(dst []activity.SpeechSpan, us []media.Utterance) []activity.SpeechSpan {
	for _, u := range us {
		dst = append(dst, activity.SpeechSpan{Start: u.Start, End: u.End, Track: u.Track})
	}
	return dst
}

// upsertMeetings stores meetings, keeping any summary already recorded.
func (p *Pipeline) upsertMeetings(day string, ms []activity.Meeting) error {
	for _, m := range ms {
		prev, ok, err := p.Store.Meeting(m.ID)
		if err != nil {
			return err
		}
		rec := store.MeetingRecord{ID: m.ID, Day: day, Start: m.Start, End: m.End, App: m.App, Title: m.Title}
		if ok {
			rec.SummaryJSON = prev.SummaryJSON
		}
		if err := p.Store.UpsertMeeting(rec); err != nil {
			return err
		}
	}
	return nil
}
