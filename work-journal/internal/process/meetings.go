package process

import (
	"errors"
	"io/fs"
	"slices"
	"time"

	"workjournal/internal/activity"
	"workjournal/internal/config"
	"workjournal/internal/media"
	"workjournal/internal/store"
)

// detectMeetings runs meeting detection over the day using the whole day's
// activity and the speech of this hour plus every other derived hour of the
// day (later ones too, so running an hour again judges a meeting that
// continues past it the same way), and returns the meetings overlapping
// [start, end); a meeting ending exactly at start is included so the hour
// after its last one can report it ended. events must include the previous
// day's (see stateEvents).
func (p *Pipeline) detectMeetings(day string, hour int, start, end time.Time, events []activity.Event, utts []media.Utterance) ([]activity.Meeting, error) {
	midnight := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.Local)
	segs := activity.Segments(events, midnight, end)
	var speech []activity.SpeechSpan
	for h := 0; h < 24; h++ {
		if h == hour {
			continue
		}
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

// inMeetingSpan is the meeting page's rule for activity meetings: the
// utterance starts within [Start, End].
func inMeetingSpan(m activity.Meeting, u media.Utterance) bool {
	return !u.Start.Before(m.Start) && !u.Start.After(m.End)
}

// keepInPerson reports whether in-person meeting m has at least two voices.
// With diarization off it is always kept. Otherwise it is kept when, in this
// hour's labelling (lab) or in another derived hour of the day, its mic
// speech carries two or more speaker labels ("Speaker ?" not counted;
// labels are compared within one hour only). It is also kept, as unknown,
// when this hour has its mic speech but none of it could be diarized.
func (p *Pipeline) keepInPerson(day string, hour int, m activity.Meeting, lab *labelled) bool {
	if p.diarizer() == nil {
		return true
	}
	voices := func(us []media.Utterance) int {
		seen := map[string]bool{}
		for _, u := range us {
			if u.Track == "mic" && u.Speaker != unknownSpeaker && inMeetingSpan(m, u) {
				seen[u.Speaker] = true
			}
		}
		return len(seen)
	}
	if voices(lab.diarizedMic) >= 2 {
		return true
	}
	if voices(lab.diarizedMic) == 0 && voices(lab.undiarizedMic) > 0 {
		p.logf("process: in-person meeting %s: mic not diarized; keeping it", m.ID)
		return true
	}
	from, to := m.Start.In(time.Local).Hour(), m.End.In(time.Local).Hour()
	if config.DayKey(m.Start) != day {
		from = 0
	}
	if config.DayKey(m.End) != day {
		to = 23
	}
	for h := from; h <= to; h++ {
		if h == hour {
			continue
		}
		d, err := Load(p.Paths.DerivedHour(day, h))
		if err != nil {
			continue
		}
		if voices(d.Transcript) >= 2 {
			return true
		}
	}
	return false
}

func appendSpeech(dst []activity.SpeechSpan, us []media.Utterance) []activity.SpeechSpan {
	for _, u := range us {
		dst = append(dst, activity.SpeechSpan{Start: u.Start, End: u.End, Track: u.Track})
	}
	return dst
}

// upsertMeetings stores doc's meetings, keeping any summary already
// recorded, and deletes the day's meeting records that overlap the hour but
// are no longer detected (detection changed as more audio arrived, or a
// meeting turned out to have one voice), with their participants. A meeting
// with a participant the user confirmed is never deleted.
func (p *Pipeline) upsertMeetings(doc *HourDoc) error {
	day, ms := doc.Day, doc.Meetings
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
	start, end, err := hourBounds(doc.Day, doc.Hour)
	if err != nil {
		return err
	}
	recs, err := p.Store.MeetingsOn(day)
	if err != nil {
		return err
	}
	for _, r := range recs {
		if !r.Start.Before(end) || !r.End.After(start) ||
			slices.ContainsFunc(ms, func(m activity.Meeting) bool { return m.ID == r.ID }) {
			continue
		}
		parts, err := p.Store.Participants(r.ID)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(parts, func(x store.ParticipantRecord) bool { return x.Confirmed }) {
			continue
		}
		p.logf("process: meeting %s is no longer detected; removing it", r.ID)
		if err := p.Store.DeleteMeeting(r.ID); err != nil {
			return err
		}
	}
	return nil
}
