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

// SummarizeHour implements HourSummarizer. Invalid skill output marks the
// hour needs_review while keeping whatever succeeded; any other error
// (claude failing to run, store errors) is returned and the hour stays
// transcribed for a later retry.
func (s *SkillSummarizer) SummarizeHour(ctx context.Context, doc *HourDoc) error {
	start, _, err := hourBounds(doc.Day, doc.Hour)
	if err != nil {
		return err
	}
	invalid := false
	// run runs one skill; invalid output is recorded rather than returned.
	run := func(skill, suffix string, in, out any) (bool, error) {
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

	cats, unmatched := categorize.Apply(doc.Segments, s.Cfg.Rules)
	recs := segmentRecords(doc.Day, doc.Hour, doc.Segments)
	for i := range recs {
		if recs[i].Category = cats[i]; cats[i] != "" {
			recs[i].Confidence = 1
			if recs[i].Kind == activity.KindActive {
				recs[i].Source = "rule"
			}
		}
	}

	sum := skills.HourSummary{Notes: []string{}, ActionItems: []skills.ActionItem{}}
	summaryJSON := ""
	ok, err := run("summarize-hour", "", s.hourInput(doc, cats), &sum)
	if err != nil {
		return err
	}
	if ok {
		b, err := json.Marshal(sum)
		if err != nil {
			return err
		}
		summaryJSON = string(b)
	} else {
		sum = skills.HourSummary{Notes: []string{}, ActionItems: []skills.ActionItem{}}
	}

	if len(unmatched) > 0 {
		in := categorizeInput{Categories: s.Cfg.Categories, HourSummary: sum}
		for _, i := range unmatched {
			in.Segments = append(in.Segments, segInput(i, doc.Segments[i], nil))
		}
		out := &checkedCategorization{allowed: s.Cfg.Categories, want: unmatched}
		ok, err := run("categorize-time", "", in, out)
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

	texts := textDocs(doc, start)
	ref := fmt.Sprintf("hour/%s/%02d", doc.Day, doc.Hour)
	for _, n := range sum.Notes {
		texts = append(texts, store.TextDoc{Kind: "note", Day: doc.Day, Hour: doc.Hour, TS: start, Text: n, Ref: ref})
	}
	for _, id := range doc.EndedMeetings {
		i := slices.IndexFunc(doc.Meetings, func(m activity.Meeting) bool { return m.ID == id })
		if i < 0 {
			s.logf("process: hour %s %02d: ended meeting %s not in hour doc", doc.Day, doc.Hour, id)
			continue
		}
		m := doc.Meetings[i]
		in := s.meetingInput(doc, m)
		var ms skills.MeetingSummary
		ok, err := run("summarize-meeting", "-"+m.ID, in, &ms)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := s.saveMeeting(doc.Day, m, ms); err != nil {
			return err
		}
		texts = append(texts, store.TextDoc{Kind: "meeting", Day: doc.Day, Hour: doc.Hour, TS: m.End, Text: meetingText(ms), Ref: "meeting/" + m.ID})
	}
	if err := s.Store.ReplaceText(doc.Day, doc.Hour, texts); err != nil {
		return fmt.Errorf("index text: %w", err)
	}

	doc.Status = store.StatusDone
	if invalid {
		doc.Status = store.StatusNeedsReview
	}
	if err := doc.Save(s.Paths.DerivedHour(doc.Day, doc.Hour)); err != nil {
		return fmt.Errorf("save derived doc: %w", err)
	}
	return s.Store.UpsertHour(store.HourRecord{Day: doc.Day, Hour: doc.Hour, Status: doc.Status, SummaryJSON: summaryJSON, UpdatedAt: time.Now()})
}

// writeInput writes a skill input to derived/<day>/.inputs/HH-<skill><suffix>.json.
func (s *SkillSummarizer) writeInput(doc *HourDoc, skill, suffix string, in any) (string, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
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

// hourInput omits excluded, paused and gap segments entirely.
func (s *SkillSummarizer) hourInput(doc *HourDoc, cats []string) hourInput {
	in := hourInput{Day: doc.Day, Hour: doc.Hour, Segments: []segmentInput{}, Meetings: []meetingBrief{}, OCR: dedupe(screenTexts(doc.Screens))}
	for i, g := range doc.Segments {
		if g.Kind == activity.KindActive || g.Kind == activity.KindIdle {
			in.Segments = append(in.Segments, segInput(i, g, &cats[i]))
		}
	}
	for _, m := range doc.Meetings {
		in.Meetings = append(in.Meetings, meetingBrief{ID: m.ID, Start: m.Start.Format("15:04"), End: m.End.Format("15:04"), App: m.App})
	}
	in.Transcript = []lineInput{}
	for _, u := range doc.Transcript {
		in.Transcript = append(in.Transcript, lineInput{Speaker: speaker(u), Text: u.Text})
	}
	return in
}

func segInput(i int, g activity.Segment, category *string) segmentInput {
	return segmentInput{Index: i, App: g.App, Title: g.Title, Minutes: int(math.Round(g.End.Sub(g.Start).Minutes())), Category: category}
}

// speaker names the mic track "You", as the skills expect.
func speaker(u media.Utterance) string {
	if u.Track == "mic" {
		return "You"
	}
	return u.Speaker
}

// meetingInput gathers the meeting's speech and screen text from this hour
// and the day's earlier derived hours it spans.
func (s *SkillSummarizer) meetingInput(doc *HourDoc, m activity.Meeting) meetingInput {
	var in meetingInput
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
	in.Transcript, in.Screens = []lineInput{}, []string{}
	within := func(t time.Time) bool { return !t.Before(m.Start) && !t.After(m.End) }
	var screens []media.ScreenText
	for _, d := range docs {
		for _, u := range d.Transcript {
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
	in.Screens = dedupe(screenTexts(screens))
	return in
}

func clock(d time.Duration) string {
	sec := int(d.Seconds())
	return fmt.Sprintf("%02d:%02d:%02d", sec/3600, sec/60%60, sec%60)
}

func screenTexts(ss []media.ScreenText) []string {
	out := []string{}
	for _, s := range ss {
		out = append(out, s.Text)
	}
	return out
}

// dedupe drops repeated texts, keeping first occurrences in order.
func dedupe(texts []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, t := range texts {
		if t = strings.TrimSpace(t); t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// saveMeeting stores the summary and the proposed participants, leaving any
// participant the user already confirmed untouched.
func (s *SkillSummarizer) saveMeeting(day string, m activity.Meeting, ms skills.MeetingSummary) error {
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
	for _, p := range ms.Participants {
		if slices.ContainsFunc(existing, func(e store.ParticipantRecord) bool { return e.Speaker == p.Speaker && e.Confirmed }) {
			continue
		}
		if err := s.Store.SetParticipant(store.ParticipantRecord{MeetingID: m.ID, Speaker: p.Speaker, Name: p.Name, Confidence: p.Confidence}); err != nil {
			return err
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
