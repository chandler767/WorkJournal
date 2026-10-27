// Package notes holds the rules for the user's notes to the journal
// (store.NoteRecord): what a valid note is, which hours it covers, the work
// saving or deleting one queues, and how notes reach the skills.
//
// A context note is text the user adds (what a call was, a pasted doc). It
// goes to summarize-hour and summarize-meeting for the hours it covers, to
// close-day for its day (and so, through the daily entry, to
// weekly-rollup), and into search. It is never a recording: it adds no
// time, speech or meeting.
//
// A correction is an instruction about recorded content ("disregard speech
// 13:10-13:20, it was bystanders"). It goes to privacy-filter as a strong
// hint for the hours it covers, and to the summarizing skills as the
// user's own statement. Saving one clears those hours' privacy decisions
// and reprocesses them.
package notes

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"workjournal/internal/config"
	"workjournal/internal/skills"
	"workjournal/internal/store"
)

// MaxBytes bounds a note's text.
const MaxBytes = 100 * 1024

var (
	dateRE  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	clockRE = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
)

// ErrBinary is returned for text that is not UTF-8 text.
var ErrBinary = errors.New("the note is not text (binary data is refused)")

// Draft is a note as the user entered it.
type Draft struct {
	Kind, Day, From, To, Text string
}

// Validate checks d and returns it as a record (without ID or times). Kind
// is context or correction; From and To are both empty (the whole day) or
// HH:MM with From before To, and To may be 24:00 for the end of the day.
func Validate(d Draft) (store.NoteRecord, error) {
	n := store.NoteRecord{Kind: strings.ToLower(strings.TrimSpace(d.Kind)), Day: strings.TrimSpace(d.Day),
		Start: strings.TrimSpace(d.From), End: strings.TrimSpace(d.To), Text: strings.TrimSpace(d.Text)}
	if n.Kind != store.NoteContext && n.Kind != store.NoteCorrection {
		return n, fmt.Errorf("kind must be context or correction, got %q", d.Kind)
	}
	if _, err := time.Parse("2006-01-02", n.Day); err != nil || !dateRE.MatchString(n.Day) {
		return n, fmt.Errorf("date must be YYYY-MM-DD, got %q", d.Day)
	}
	if err := CheckText(n.Text); err != nil {
		return n, err
	}
	if (n.Start == "") != (n.End == "") {
		return n, errors.New("give both from and to, or neither for the whole day")
	}
	if n.Start != "" {
		if !clockRE.MatchString(n.Start) {
			return n, fmt.Errorf("from must be HH:MM, got %q", d.From)
		}
		if !clockRE.MatchString(n.End) && n.End != "24:00" {
			return n, fmt.Errorf("to must be HH:MM, got %q", d.To)
		}
		if n.End <= n.Start {
			return n, fmt.Errorf("to %s must be after from %s", n.End, n.Start)
		}
	}
	return n, nil
}

// CheckText refuses an empty, oversized or binary note text.
func CheckText(text string) error {
	switch {
	case strings.TrimSpace(text) == "":
		return errors.New("the note is empty")
	case len(text) > MaxBytes:
		return fmt.Errorf("the note is %d KB; the limit is %d KB", (len(text)+1023)/1024, MaxBytes/1024)
	case !utf8.ValidString(text) || strings.ContainsRune(text, 0):
		return ErrBinary
	}
	return nil
}

// ReadFile reads a note's text from path, refusing binary and oversized
// files before reading them whole.
func ReadFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return "", err
	}
	if len(b) > MaxBytes {
		return "", fmt.Errorf("%s is over the %d KB limit", path, MaxBytes/1024)
	}
	if bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) {
		return "", fmt.Errorf("%s: %w", path, ErrBinary)
	}
	return string(b), nil
}

// minutes returns a note's range in minutes of its day; the whole day is
// 0-1440.
func minutes(n store.NoteRecord) (int, int) {
	if n.Start == "" {
		return 0, 24 * 60
	}
	return clockMin(n.Start), clockMin(n.End)
}

func clockMin(s string) int {
	h, _ := strconv.Atoi(s[:2])
	m, _ := strconv.Atoi(s[3:])
	return h*60 + m
}

// Hours returns the clock hours a note's range touches; nil for a whole-day
// note.
func Hours(n store.NoteRecord) []int {
	if n.Start == "" {
		return nil
	}
	from, to := minutes(n)
	var out []int
	for h := from / 60; h*60 < to; h++ {
		out = append(out, h)
	}
	return out
}

// Queue returns the work saving, changing or deleting a note queues, for
// each of its versions (old and new; either may be nil): its day's journal
// entry, and the hours it covers summarized again (a context note) or
// reprocessed with their privacy decisions cleared (a correction). A
// whole-day correction covers every hour of the day the store has; a
// whole-day context note only the journal entry. A meeting the note covers
// is summarized in the hour it ended (process.HourDoc.EndedMeetings), so
// that hour is summarized again too, after the hours before it.
func Queue(st *store.Store, versions ...*store.NoteRecord) ([]store.NoteQueueItem, error) {
	var out []store.NoteQueueItem
	for _, n := range versions {
		if n == nil {
			continue
		}
		redo := store.RedoSummary
		if n.Kind == store.NoteCorrection {
			redo = store.RedoPrivacy
		}
		hours := Hours(*n)
		if n.Start == "" && n.Kind == store.NoteCorrection {
			recs, err := st.HoursOn(n.Day)
			if err != nil {
				return nil, err
			}
			for _, r := range recs {
				hours = append(hours, r.Hour)
			}
		}
		for _, h := range hours {
			out = append(out, store.NoteQueueItem{Day: n.Day, Hour: h, Redo: redo})
		}
		if n.Start != "" {
			ms, err := st.MeetingsOn(n.Day)
			if err != nil {
				return nil, err
			}
			for _, m := range ms {
				if m.Imported || !Covers(*n, m.Start, m.End) {
					continue
				}
				day := config.DayKey(m.End)
				out = append(out, store.NoteQueueItem{Day: day, Hour: m.End.Hour(), Redo: store.RedoSummary})
				if day != n.Day {
					out = append(out, store.NoteQueueItem{Day: day, Hour: -1, Redo: store.RedoSummary})
				}
			}
		}
		out = append(out, store.NoteQueueItem{Day: n.Day, Hour: -1, Redo: store.RedoSummary})
	}
	return out, nil
}

// RawGone reports whether retention already removed day's raw recordings,
// so a correction can change only summaries, not the transcript.
func RawGone(p config.Paths, day string) bool {
	_, err := os.Stat(p.RawDay(day))
	return errors.Is(err, os.ErrNotExist)
}

// spanMinutes returns [start, end) as minutes of day on the clock each
// time was recorded by, clamped to the day, with start rounded down and end
// up so that a span reaching into a minute overlaps it; ok is false when
// the span misses day.
func spanMinutes(day string, start, end time.Time) (int, int, bool) {
	at := func(t time.Time, up bool) int {
		switch d := config.DayKey(t); {
		case d < day:
			return 0
		case d > day:
			return 24 * 60
		}
		m := t.Hour()*60 + t.Minute()
		if up && (t.Second() > 0 || t.Nanosecond() > 0) {
			m++
		}
		return m
	}
	s, e := at(start, false), at(end, true)
	return s, e, s < e
}

// Covers reports whether n covers some of [start, end): a range note that
// overlaps it, or a whole-day note on a day it touches.
func Covers(n store.NoteRecord, start, end time.Time) bool {
	s, e, ok := spanMinutes(n.Day, start, end)
	if !ok {
		return false
	}
	from, to := minutes(n)
	return from < e && s < to
}

// ForSpan returns, as skill input, the notes of notes that cover [start,
// end): range notes of either kind, and whole-day corrections (a whole-day
// context note goes to close-day only). The result is never nil.
func ForSpan(notes []store.NoteRecord, start, end time.Time) []skills.UserNote {
	out := []skills.UserNote{}
	for _, n := range notes {
		if n.Start == "" && n.Kind == store.NoteContext {
			continue
		}
		if Covers(n, start, end) {
			out = append(out, Input(n))
		}
	}
	return out
}

// ForHour is ForSpan for clock hour h of the notes' day.
func ForHour(notes []store.NoteRecord, h int) []skills.UserNote {
	out := []skills.UserNote{}
	for _, n := range notes {
		if n.Start == "" && n.Kind == store.NoteContext {
			continue
		}
		if from, to := minutes(n); from < (h+1)*60 && h*60 < to {
			out = append(out, Input(n))
		}
	}
	return out
}

// Corrections returns the corrections of notes that cover [start, end), as
// skill input; never nil.
func Corrections(notes []store.NoteRecord, start, end time.Time) []skills.UserNote {
	out := []skills.UserNote{}
	for _, n := range notes {
		if n.Kind == store.NoteCorrection && Covers(n, start, end) {
			out = append(out, Input(n))
		}
	}
	return out
}

// Inputs returns all of notes as skill input; never nil.
func Inputs(notes []store.NoteRecord) []skills.UserNote {
	out := []skills.UserNote{}
	for _, n := range notes {
		out = append(out, Input(n))
	}
	return out
}

// Input is a note as the skills receive it.
func Input(n store.NoteRecord) skills.UserNote {
	return skills.UserNote{Kind: n.Kind, From: n.Start, To: n.End, Context: skills.TextLines(n.Text)}
}

// Range describes a note's range for people: "13:10–13:20" or "all day".
func Range(n store.NoteRecord) string {
	if n.Start == "" {
		return "all day"
	}
	return n.Start + "–" + n.End
}

// Add validates and saves a new note from source, queueing the work it
// needs.
func Add(st *store.Store, d Draft, source string, now time.Time) (store.NoteRecord, error) {
	n, err := Validate(d)
	if err != nil {
		return n, err
	}
	n.Source, n.CreatedAt, n.UpdatedAt = source, now, now
	q, err := Queue(st, &n)
	if err != nil {
		return n, err
	}
	n.ID, err = st.AddNote(n, q)
	return n, err
}

// ErrNotFound is returned for a note id that does not exist.
var ErrNotFound = errors.New("no such note")

// Update replaces note id's kind, range and text (its day stays), queueing
// the work for both the old and the new version.
func Update(st *store.Store, id int64, d Draft, now time.Time) (store.NoteRecord, error) {
	old, ok, err := st.Note(id)
	if err != nil {
		return store.NoteRecord{}, err
	}
	if !ok {
		return store.NoteRecord{}, ErrNotFound
	}
	d.Day = old.Day
	n, err := Validate(d)
	if err != nil {
		return n, err
	}
	n.ID, n.Source, n.CreatedAt, n.UpdatedAt = id, old.Source, old.CreatedAt, now
	q, err := Queue(st, &old, &n)
	if err != nil {
		return n, err
	}
	return n, st.UpdateNote(n, q)
}

// Delete removes note id, queueing the work to undo it.
func Delete(st *store.Store, id int64) (store.NoteRecord, error) {
	old, ok, err := st.Note(id)
	if err != nil {
		return old, err
	}
	if !ok {
		return old, ErrNotFound
	}
	q, err := Queue(st, &old)
	if err != nil {
		return old, err
	}
	return old, st.DeleteNote(id, q)
}
