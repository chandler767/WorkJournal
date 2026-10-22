package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"workjournal/internal/activity"
	"workjournal/internal/categorize"
	"workjournal/internal/config"
	"workjournal/internal/media"
	"workjournal/internal/skills"
	"workjournal/internal/store"
)

// SkillSummarizer is the HourSummarizer that runs the Claude skills:
// summarize-hour, categorize-time (only for segments no rule matched) and
// summarize-meeting for each meeting that ended in the hour.
type SkillSummarizer struct {
	Runner skills.Runner
	Store  *store.Store
	Paths  config.Paths
	Cfg    config.Config
	Logf   func(format string, args ...any) // nil: log.Printf
}

func (s *SkillSummarizer) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// Skill input shapes; each matches the "Input" section of its SKILL.md.
type (
	hourInput struct {
		Day        string         `json:"day"`
		Hour       int            `json:"hour"`
		Segments   []segmentInput `json:"segments"`
		Meetings   []meetingBrief `json:"meetings"`
		Transcript []lineInput    `json:"transcript"`
		OCR        []string       `json:"ocr"`
	}
	segmentInput struct {
		Index    int     `json:"index"`
		App      string  `json:"app"`
		Title    string  `json:"title"`
		Minutes  int     `json:"minutes"`
		Start    string  `json:"start,omitempty"` // HH:MM; summarize-hour only
		Category *string `json:"category,omitempty"`
	}
	meetingBrief struct {
		ID    string `json:"id"`
		Start string `json:"start"`
		End   string `json:"end"`
		App   string `json:"app"`
	}
	lineInput struct {
		T       string `json:"t,omitempty"`
		Speaker string `json:"speaker"`
		Text    string `json:"text"`
	}
	categorizeInput struct {
		Categories  []string           `json:"categories"`
		HourSummary skills.HourSummary `json:"hour_summary"`
		Segments    []segmentInput     `json:"segments"`
	}
	meetingInput struct {
		Meeting struct {
			ID    string `json:"id"`
			App   string `json:"app"`
			Start string `json:"start"`
			End   string `json:"end"`
			Title string `json:"title"`
		} `json:"meeting"`
		Transcript []lineInput `json:"transcript"`
		Screens    []string    `json:"screens"`
		// UserNote is the user's own note about an imported recording.
		UserNote *userNote `json:"user_note,omitempty"`
	}
	userNote struct {
		Title   string   `json:"title"`
		Context []string `json:"context_lines"` // skills.TextLines
	}
)

// checkedCategorization rejects categories outside the configured list and
// entries that do not cover exactly the requested segment indexes.
type checkedCategorization struct {
	skills.Categorization
	allowed []string
	want    []int
}

func (c *checkedCategorization) Validate() error {
	seen := map[int]bool{}
	for _, g := range c.Segments {
		if !slices.Contains(c.allowed, g.Category) {
			return fmt.Errorf("category %q is not configured", g.Category)
		}
		if !slices.Contains(c.want, g.Index) || seen[g.Index] {
			return fmt.Errorf("unexpected or repeated segment index %d", g.Index)
		}
		seen[g.Index] = true
	}
	if len(seen) != len(c.want) {
		return fmt.Errorf("categorized %d of %d segments", len(seen), len(c.want))
	}
	return nil
}

// checkedHour rejects a timeline whose start/end are not HH:MM inside the
// hour being summarized (an end of the next hour's :00 is allowed).
type checkedHour struct {
	skills.HourSummary
	hour int
}

func (c *checkedHour) Validate() error {
	lo, hi := c.hour*60, (c.hour+1)*60
	for i, e := range c.Timeline {
		s, err := clockMinutes(e.Start)
		if err != nil {
			return fmt.Errorf("timeline[%d] start: %w", i, err)
		}
		en, err := clockMinutes(e.End)
		if err != nil {
			return fmt.Errorf("timeline[%d] end: %w", i, err)
		}
		if en == 0 && c.hour == 23 {
			en = 24 * 60
		}
		if s < lo || s >= hi || en < s || en > hi {
			return fmt.Errorf("timeline[%d] %s-%s is outside hour %02d", i, e.Start, e.End, c.hour)
		}
	}
	return nil
}

// clockMinutes parses a strict HH:MM into minutes since midnight.
func clockMinutes(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil || len(s) != 5 {
		return 0, fmt.Errorf("%q is not HH:MM", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// SummarizeHour implements HourSummarizer. Invalid skill output marks the
// hour needs_review while keeping whatever succeeded; any other error
// (claude failing to run, store errors) is returned and the hour stays
// transcribed for a later retry.
func (s *SkillSummarizer) SummarizeHour(ctx context.Context, doc *HourDoc) error {
	w, err := hourBounds(s.Paths, doc.Day, doc.Hour)
	if err != nil {
		return err
	}
	start := w.start()
	invalid := false
	// run runs one skill; invalid output is recorded rather than returned.
	run := func(skill, suffix string, in []byte, out any) (bool, error) {
		path, err := s.writeInput(doc, skill, suffix, in)
		if err != nil {
			return false, err
		}
		err = s.Runner.Run(ctx, skill, path, out)
		if errors.Is(err, skills.ErrInvalidOutput) {
			s.logf("process: hour %s %02d: %v", doc.Day, doc.Hour, err)
			invalid = true
			return false, nil
		}
		return err == nil, err
	}

	// Docs transcribed before meeting splitting existed are split here too.
	doc.Segments = activity.SplitAtMeetings(doc.Segments, doc.Meetings)
	cats, unmatched := categorize.Apply(doc.Segments, doc.Meetings, s.Cfg.Rules)
	recs := segmentRecords(doc.Day, doc.Hour, doc.Segments)
	for i := range recs {
		if recs[i].Category = cats[i]; cats[i] != "" {
			recs[i].Confidence = 1
			if recs[i].Kind == activity.KindActive || categorize.InMeeting(doc.Segments[i], doc.Meetings) {
				recs[i].Source = "rule"
			}
		}
	}

	sum := skills.HourSummary{Timeline: []skills.TimelineEntry{}, Notes: []string{}, ActionItems: []skills.ActionItem{}}
	summaryJSON := ""
	checked := &checkedHour{hour: doc.Hour}
	hourIn, over, err := s.hourInput(doc, cats)
	if err != nil {
		return err
	}
	if over {
		s.logOversized(doc, "summarize-hour", hourIn)
	}
	ok, err := run("summarize-hour", "", hourIn, checked)
	if err != nil {
		return err
	}
	if ok {
		sum = checked.HourSummary
		b, err := json.Marshal(sum)
		if err != nil {
			return err
		}
		summaryJSON = string(b)
	} else {
		sum = skills.HourSummary{Timeline: []skills.TimelineEntry{}, Notes: []string{}, ActionItems: []skills.ActionItem{}}
	}

	if len(unmatched) > 0 {
		in := categorizeInput{Categories: s.Cfg.Categories, HourSummary: sum}
		for _, i := range unmatched {
			in.Segments = append(in.Segments, segInput(i, doc.Segments[i], nil))
		}
		out := &checkedCategorization{allowed: s.Cfg.Categories, want: unmatched}
		b, err := skills.MarshalInput(in)
		if err != nil {
			return err
		}
		ok, err := run("categorize-time", "", b, out)
		if err != nil {
			return err
		}
		if ok {
			for _, g := range out.Segments {
				recs[g.Index].Category, recs[g.Index].Confidence, recs[g.Index].Source = g.Category, g.Confidence, "skill"
			}
		}
	}
	if err := s.Store.ReplaceSegments(doc.Day, doc.Hour, recs); err != nil {
		return fmt.Errorf("store segments: %w", err)
	}

	texts := textDocs(doc)
	ref := fmt.Sprintf("hour/%s/%02d", doc.Day, doc.Hour)
	indexed := append([]string{sum.Narrative}, sum.Notes...)
	for _, e := range sum.Timeline {
		indexed = append(indexed, e.Activity)
	}
	for _, n := range indexed {
		if strings.TrimSpace(n) != "" {
			texts = append(texts, store.TextDoc{Kind: "note", Day: doc.Day, Hour: doc.Hour, TS: start, Text: n, Ref: ref})
		}
	}
	for _, id := range doc.EndedMeetings {
		i := slices.IndexFunc(doc.Meetings, func(m activity.Meeting) bool { return m.ID == id })
		if i < 0 {
			s.logf("process: hour %s %02d: ended meeting %s not in hour doc", doc.Day, doc.Hour, id)
			continue
		}
		m := doc.Meetings[i]
		in, b, over, err := s.meetingInput(doc, m)
		if err != nil {
			return err
		}
		if over {
			s.logOversized(doc, "summarize-meeting", b)
		}
		var ms skills.MeetingSummary
		ok, err := run("summarize-meeting", "-"+m.ID, b, &ms)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := s.saveMeeting(doc.Day, m, ms, in.Transcript); err != nil {
			return err
		}
		texts = append(texts, store.TextDoc{Kind: "meeting", Day: doc.Day, Hour: doc.Hour, TS: m.End, Text: meetingText(ms), Ref: "meeting/" + m.ID})
	}
	if err := s.Store.ReplaceText(doc.Day, doc.Hour, texts); err != nil {
		return fmt.Errorf("index text: %w", err)
	}

	switch {
	case doc.Status == store.StatusPartial:
		// The in-progress hour stays partial; its final pass decides.
		if summaryJSON == "" {
			summaryJSON, err = s.previousSummary(doc)
			if err != nil {
				return err
			}
		}
	case invalid:
		doc.Status = store.StatusNeedsReview
	default:
		doc.Status = store.StatusDone
	}
	if err := doc.Save(s.Paths.DerivedHour(doc.Day, doc.Hour)); err != nil {
		return fmt.Errorf("save derived doc: %w", err)
	}
	return s.Store.UpsertHour(store.HourRecord{Day: doc.Day, Hour: doc.Hour, Status: doc.Status, SummaryJSON: summaryJSON, UpdatedAt: time.Now()})
}

// previousSummary is the summary already stored for doc's hour, kept when a
// partial run's summarize-hour output was invalid.
func (s *SkillSummarizer) previousSummary(doc *HourDoc) (string, error) {
	rec, _, err := s.Store.Hour(doc.Day, doc.Hour)
	return rec.SummaryJSON, err
}

// InputHash implements InputHasher: a fingerprint of the summarize-hour and
// summarize-meeting inputs and of the segments categorize-time would be
// asked about, built exactly as SummarizeHour builds them.
func (s *SkillSummarizer) InputHash(doc *HourDoc) (string, error) {
	split := *doc
	split.Segments = activity.SplitAtMeetings(doc.Segments, doc.Meetings)
	doc = &split
	cats, unmatched := categorize.Apply(doc.Segments, doc.Meetings, s.Cfg.Rules)
	var segs []segmentInput
	for _, i := range unmatched {
		segs = append(segs, segInput(i, doc.Segments[i], nil))
	}
	var meetings []json.RawMessage
	for _, id := range doc.EndedMeetings {
		if i := slices.IndexFunc(doc.Meetings, func(m activity.Meeting) bool { return m.ID == id }); i >= 0 {
			_, b, _, err := s.meetingInput(doc, doc.Meetings[i])
			if err != nil {
				return "", err
			}
			meetings = append(meetings, b)
		}
	}
	hourIn, _, err := s.hourInput(doc, cats)
	if err != nil {
		return "", err
	}
	return hashJSON(json.RawMessage(hourIn), s.Cfg.Categories, segs, meetings)
}

// logOversized notes an input still over input_max_bytes with no OCR left;
// it is sent anyway, since Claude can page through it by line.
func (s *SkillSummarizer) logOversized(doc *HourDoc, skill string, b []byte) {
	s.logf("process: hour %s %02d: %s input is %d bytes (input_max_bytes %d) with no on-screen text left; sending it as is",
		doc.Day, doc.Hour, skill, len(b), s.inputMax())
}

// writeInput writes a skill input to derived/<day>/.inputs/HH-<skill><suffix>.json.
func (s *SkillSummarizer) writeInput(doc *HourDoc, skill, suffix string, b []byte) (string, error) {
	dir := filepath.Join(filepath.Dir(s.Paths.DerivedHour(doc.Day, doc.Hour)), ".inputs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("%02d-%s%s.json", doc.Hour, skill, suffix))
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return "", fmt.Errorf("write skill input: %w", err)
	}
	return path, nil
}

// hourInput returns the encoded summarize-hour input, with the hour's
// on-screen text compacted and capped (see fitOCR), and whether it is over
// input_max_bytes even so. Excluded, paused and gap segments are omitted
// entirely.
func (s *SkillSummarizer) hourInput(doc *HourDoc, cats []string) ([]byte, bool, error) {
	in := hourInput{Day: doc.Day, Hour: doc.Hour, Segments: []segmentInput{}, Meetings: []meetingBrief{}}
	for i, g := range doc.Segments {
		if g.Kind == activity.KindActive || g.Kind == activity.KindIdle {
			si := segInput(i, g, &cats[i])
			si.Start = g.Start.Format("15:04")
			in.Segments = append(in.Segments, si)
		}
	}
	for _, m := range doc.Meetings {
		in.Meetings = append(in.Meetings, meetingBrief{ID: m.ID, Start: m.Start.Format("15:04"), End: m.End.Format("15:04"), App: m.App})
	}
	in.Transcript = []lineInput{}
	for _, u := range visibleSpeech(doc) {
		in.Transcript = append(in.Transcript, lineInput{Speaker: speaker(u), Text: u.Text})
	}
	in.Transcript = splitLines(in.Transcript)
	return s.fitOCR(doc.Screens, func(ocr []string) any { in.OCR = ocr; return in })
}

func segInput(i int, g activity.Segment, category *string) segmentInput {
	return segmentInput{Index: i, App: g.App, Title: g.Title, Minutes: int(math.Round(g.End.Sub(g.Start).Minutes())), Category: category}
}

// speaker names the user "You", as the skills expect: mic speech labelled
// "me" (or not labelled). Mic speech diarized during an in-person meeting
// keeps its speaker label.
func speaker(u media.Utterance) string {
	if u.Track == "mic" && (u.Speaker == "me" || u.Speaker == "") {
		return "You"
	}
	return u.Speaker
}

// meetingInput gathers the meeting's speech and screen text from this hour
// and the day's earlier derived hours it spans, and encodes it with the
// screen text compacted and capped like an hour's (see fitOCR); over
// reports an input over input_max_bytes even so.
func (s *SkillSummarizer) meetingInput(doc *HourDoc, m activity.Meeting) (in meetingInput, b []byte, over bool, err error) {
	in.Meeting.ID, in.Meeting.App, in.Meeting.Title = m.ID, m.App, m.Title
	in.Meeting.Start, in.Meeting.End = m.Start.Format(time.RFC3339), m.End.Format(time.RFC3339)
	docs := []*HourDoc{}
	from := 0
	if config.DayKey(m.Start) == doc.Day {
		from = m.Start.Hour()
	}
	for h := from; h < doc.Hour; h++ {
		d, err := Load(s.Paths.DerivedHour(doc.Day, h))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			s.logf("process: meeting %s: skipping hour %02d: %v", m.ID, h, err)
			continue
		}
		docs = append(docs, d)
	}
	docs = append(docs, doc)
	return s.encodeMeeting(in, docs, m)
}

// encodeMeeting adds the speech and screen text of docs within m to in and
// encodes it (see meetingInput).
func (s *SkillSummarizer) encodeMeeting(in meetingInput, docs []*HourDoc, m activity.Meeting) (_ meetingInput, b []byte, over bool, err error) {
	in.Transcript, in.Screens = []lineInput{}, []string{}
	within := func(t time.Time) bool { return !t.Before(m.Start) && !t.After(m.End) }
	var screens []media.ScreenText
	for _, d := range docs {
		for _, u := range visibleSpeech(d) {
			if within(u.Start) {
				in.Transcript = append(in.Transcript, lineInput{T: clock(u.Start.Sub(m.Start)), Speaker: speaker(u), Text: u.Text})
			}
		}
		for _, sc := range d.Screens {
			if within(sc.Time) {
				screens = append(screens, sc)
			}
		}
	}
	in.Transcript = splitLines(in.Transcript)
	b, over, err = s.fitOCR(screens, func(ocr []string) any { in.Screens = ocr; return in })
	return in, b, over, err
}

// SummarizeImport runs summarize-meeting on an imported recording's
// meeting m, its speech in doc, with the user's note, and saves the summary
// and participants. Invalid output is skills.ErrInvalidOutput.
func (s *SkillSummarizer) SummarizeImport(ctx context.Context, doc *HourDoc, m activity.Meeting, note ImportNote) (skills.MeetingSummary, error) {
	var in meetingInput
	in.Meeting.ID, in.Meeting.App, in.Meeting.Title = m.ID, m.App, m.Title
	in.Meeting.Start, in.Meeting.End = m.Start.Format(time.RFC3339), m.End.Format(time.RFC3339)
	if note.Title != "" || note.Context != "" {
		in.UserNote = &userNote{Title: note.Title, Context: skills.TextLines(note.Context)}
	}
	in, b, over, err := s.encodeMeeting(in, []*HourDoc{doc}, m)
	if err != nil {
		return skills.MeetingSummary{}, err
	}
	if over {
		s.logOversized(doc, "summarize-meeting", b)
	}
	path, err := s.writeInput(doc, "summarize-meeting", "-"+m.ID, b)
	if err != nil {
		return skills.MeetingSummary{}, err
	}
	var ms skills.MeetingSummary
	if err := s.Runner.Run(ctx, "summarize-meeting", path, &ms); err != nil {
		return skills.MeetingSummary{}, err
	}
	return ms, s.saveMeeting(doc.Day, m, ms, in.Transcript)
}

func clock(d time.Duration) string {
	sec := int(d.Seconds())
	return fmt.Sprintf("%02d:%02d:%02d", sec/3600, sec/60%60, sec%60)
}

// anonLabel matches the labels of unnamed voices.
var anonLabel = regexp.MustCompile(`^Speaker \d+$`)

// saveMeeting stores the summary and the meeting's participants: the voices
// in its transcript and any participant the user confirmed, which is never
// changed. A voice gets the skill's proposed name, else none for "Speaker N"
// (and "You"), else its label when that is already a name (a known voice).
// "others" and "Speaker ?" are not voices; a participant the skill invents
// is ignored; an unconfirmed participant whose label is no longer in the
// transcript (speakers were clustered again) is removed.
func (s *SkillSummarizer) saveMeeting(day string, m activity.Meeting, ms skills.MeetingSummary, transcript []lineInput) error {
	b, err := json.Marshal(ms)
	if err != nil {
		return err
	}
	rec, ok, err := s.Store.Meeting(m.ID)
	if err != nil {
		return err
	}
	if !ok {
		rec = store.MeetingRecord{ID: m.ID, Day: day, Start: m.Start, End: m.End, App: m.App, Title: m.Title}
	}
	rec.SummaryJSON = string(b)
	if err := s.Store.UpsertMeeting(rec); err != nil {
		return err
	}
	existing, err := s.Store.Participants(m.ID)
	if err != nil {
		return err
	}
	confirmed := map[string]bool{}
	for _, e := range existing {
		confirmed[e.Speaker] = e.Confirmed
	}
	var voices []string
	for _, l := range transcript {
		if l.Speaker != "" && l.Speaker != "others" && l.Speaker != unknownSpeaker && !slices.Contains(voices, l.Speaker) {
			voices = append(voices, l.Speaker)
		}
	}
	for _, v := range voices {
		if confirmed[v] {
			continue
		}
		rec := store.ParticipantRecord{MeetingID: m.ID, Speaker: v}
		if i := slices.IndexFunc(ms.Participants, func(p skills.Participant) bool { return p.Speaker == v }); i >= 0 {
			rec.Name, rec.Confidence = ms.Participants[i].Name, ms.Participants[i].Confidence
		} else if !anonLabel.MatchString(v) && v != "You" && v != "me" {
			// Already a name: a voice matched to a profile.
			rec.Name, rec.Confidence = v, 1
		}
		if err := s.Store.SetParticipant(rec); err != nil {
			return err
		}
	}
	for _, e := range existing {
		if !e.Confirmed && !slices.Contains(voices, e.Speaker) {
			if err := s.Store.DeleteParticipant(m.ID, e.Speaker); err != nil {
				return err
			}
		}
	}
	return nil
}

func meetingText(ms skills.MeetingSummary) string {
	parts := append([]string{ms.Summary}, ms.Decisions...)
	for _, a := range ms.ActionItems {
		parts = append(parts, a.Text)
	}
	return strings.Join(parts, "\n")
}

// visibleSpeech returns d's utterances that do not overlap an excluded or
// paused segment; nothing said during those spans may reach a skill. The
// pipeline already drops them when building the doc; this is defence in depth.
func visibleSpeech(d *HourDoc) []media.Utterance {
	var out []media.Utterance
	for _, u := range d.Transcript {
		hidden := slices.ContainsFunc(d.Segments, func(g activity.Segment) bool {
			return (g.Kind == activity.KindExcluded || g.Kind == activity.KindPaused) &&
				u.Start.Before(g.End) && g.Start.Before(u.End)
		})
		if !hidden {
			out = append(out, u)
		}
	}
	return out
}
