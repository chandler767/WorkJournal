package mcpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"workjournal/internal/activity"
	"workjournal/internal/config"
	"workjournal/internal/media"
	"workjournal/internal/process"
	"workjournal/internal/skills"
	"workjournal/internal/store"
)

// Tool inputs. Fields without omitempty are required.
type (
	SearchArgs struct {
		Query  string `json:"query" jsonschema:"words to find; every word must match"`
		From   string `json:"from,omitempty" jsonschema:"first day to search: YYYY-MM-DD, today or yesterday"`
		To     string `json:"to,omitempty" jsonschema:"last day to search: YYYY-MM-DD, today or yesterday"`
		Limit  int    `json:"limit,omitempty" jsonschema:"hits to return, 1-50 (default 20)"`
		Offset int    `json:"offset,omitempty" jsonschema:"hits to skip, for the next page"`
	}
	DayArgs struct {
		Date   string `json:"date" jsonschema:"the day: YYYY-MM-DD, today or yesterday"`
		Offset int    `json:"offset,omitempty" jsonschema:"hours to skip, when an earlier call was truncated"`
	}
	HourArgs struct {
		Date              string `json:"date" jsonschema:"the day: YYYY-MM-DD, today or yesterday"`
		Hour              int    `json:"hour" jsonschema:"the hour, 0-23 (local time)"`
		IncludeTranscript bool   `json:"include_transcript,omitempty" jsonschema:"also return the transcript lines"`
		TranscriptOffset  int    `json:"transcript_offset,omitempty" jsonschema:"transcript lines to skip, for the next page"`
	}
	ListMeetingsArgs struct {
		From   string `json:"from" jsonschema:"first day: YYYY-MM-DD, today or yesterday"`
		To     string `json:"to" jsonschema:"last day: YYYY-MM-DD, today or yesterday"`
		Person string `json:"person,omitempty" jsonschema:"only meetings with a participant whose name contains this (any case)"`
		Offset int    `json:"offset,omitempty" jsonschema:"meetings to skip, for the next page"`
	}
	MeetingArgs struct {
		ID                string `json:"id" jsonschema:"the meeting id, as returned by list_meetings or search"`
		IncludeTranscript bool   `json:"include_transcript,omitempty" jsonschema:"also return the meeting transcript"`
		Offset            int    `json:"offset,omitempty" jsonschema:"transcript lines to skip, for the next page"`
	}
	TimeReportArgs struct {
		From    string `json:"from" jsonschema:"first day: YYYY-MM-DD, today or yesterday"`
		To      string `json:"to" jsonschema:"last day: YYYY-MM-DD, today or yesterday"`
		GroupBy string `json:"group_by" jsonschema:"category, day or app"`
	}
	WeekArgs struct {
		Week string `json:"week" jsonschema:"ISO week YYYY-Www (e.g. 2026-W41), or today or yesterday for the week holding that day"`
	}
)

const (
	defaultSearchLimit = 20
	maxSearchLimit     = 50
	maxMeetingDays     = 92
	maxReportDays      = 366
)

// ---- search ----

func (j *Journal) Search(a SearchArgs) (string, error) {
	q := strings.TrimSpace(a.Query)
	if q == "" {
		return "", errors.New("query is required")
	}
	if a.Limit < 0 || a.Limit > maxSearchLimit {
		return "", fmt.Errorf("limit must be 1-%d, got %d", maxSearchLimit, a.Limit)
	}
	if err := offsetArg("offset", a.Offset); err != nil {
		return "", err
	}
	limit := a.Limit
	if limit == 0 {
		limit = defaultSearchLimit
	}
	from, to, err := j.dayRange(a.From, a.To, "", "", 0)
	if err != nil {
		return "", err
	}
	hits, err := j.Store.SearchRange(q, from, to, limit+1, a.Offset)
	if err != nil {
		return "", fmt.Errorf("search: %w", err)
	}
	more := len(hits) > limit
	if more {
		hits = hits[:limit]
	}
	withheld := map[string]bool{}
	var items []string
	for _, h := range hits {
		if h.Kind == "transcript" || h.Kind == "ocr" {
			key := fmt.Sprintf("%s/%02d", h.Day, h.Hour)
			w, seen := withheld[key]
			if !seen {
				w = j.speechWithheld(h.Day, h.Hour)
				withheld[key] = w
			}
			if w && h.Kind == "transcript" {
				continue
			}
		}
		line := fmt.Sprintf("- [%s] %s %s (hour %02d): %s", h.Kind, h.Day, h.TS.Local().Format("15:04"), h.Hour,
			strings.Join(strings.Fields(h.Snippet), " "))
		if p := hitPath(h.Ref); p != "" {
			line += " — " + j.link(p)
		}
		items = append(items, line)
	}
	moreNote := ""
	if more {
		moreNote = fmt.Sprintf("\n[More matches: call again with offset=%d.]\n", a.Offset+limit)
	}
	if len(items) == 0 {
		if a.Offset == 0 && !more {
			return "No matches.", nil
		}
		return "No matches on this page." + moreNote, nil
	}
	head := "Matches, best first ([brackets] mark the matched words):\n"
	return paged(head, items, 0, "offset", "hits") + moreNote, nil
}

// speechWithheld reports whether the hour's derived doc withholds speech.
func (j *Journal) speechWithheld(day string, hour int) bool {
	doc, err := process.Load(j.Paths.DerivedHour(day, hour))
	return err == nil && doc.SpeechWithheld
}

// ---- get_day ----

func (j *Journal) GetDay(a DayArgs) (string, error) {
	day, err := j.day("date", a.Date)
	if err != nil {
		return "", err
	}
	if err := offsetArg("offset", a.Offset); err != nil {
		return "", err
	}
	t, _ := time.ParseInLocation("2006-01-02", day, time.Local)
	var b strings.Builder
	fmt.Fprintf(&b, "# %s (%s)\nDashboard: %s\n\n## Journal entry\n", day, t.Weekday(), j.link("/day/"+day))
	md, err := readOptional(j.Paths.DailyJournal(day))
	if err != nil {
		return "", err
	}
	if md == "" {
		b.WriteString("No journal entry has been written for this day yet.\n")
	} else {
		b.WriteString(strings.TrimSpace(md) + "\n")
	}

	b.WriteString("\n## Time by category (minutes)\n")
	totals, err := j.Store.CategoryTotals(day, day)
	if err != nil {
		return "", err
	}
	if lines := j.totalLines(totals, "- %s: %d min"); len(lines) > 0 {
		b.WriteString(strings.Join(lines, "\n") + "\n")
	} else {
		b.WriteString("No categorized time.\n")
	}

	b.WriteString("\n## Meetings\n")
	meets, err := j.Store.MeetingsOn(day)
	if err != nil {
		return "", err
	}
	if len(meets) == 0 {
		b.WriteString("No meetings.\n")
	}
	for _, m := range meets {
		line := fmt.Sprintf("- %s %s (%s) — id %s", clockSpan(m.Start, m.End), m.Title, m.App, m.ID)
		if ms, ok := meetingSummary(m); ok && ms.Summary != "" {
			line += " — " + firstLine(ms.Summary, 200)
		}
		b.WriteString(line + " — " + j.link("/meeting/"+m.ID) + "\n")
	}

	b.WriteString("\n## Hours\n")
	hours, err := j.Store.HoursOn(day)
	if err != nil {
		return "", err
	}
	if len(hours) == 0 {
		b.WriteString("No hours recorded.\n")
		return paged(b.String(), nil, 0, "offset", "hours"), nil
	}
	var items []string
	for _, h := range hours {
		items = append(items, j.hourBlock(h))
	}
	return paged(b.String(), items, a.Offset, "offset", "hours"), nil
}

// hourBlock is one hour of get_day.
func (j *Journal) hourBlock(h store.HourRecord) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %02d:00 — %s%s — %s\n", h.Hour, h.Status, statusNote(h.Status), j.link(fmt.Sprintf("/hour/%s/%02d", h.Day, h.Hour)))
	sum, ok := hourSummary(h)
	if ok {
		writeSummary(&b, sum, false)
	}
	j.writePrivacy(&b, h.Day, h.Hour)
	return strings.TrimRight(b.String(), "\n")
}

func statusNote(status string) string {
	switch status {
	case store.StatusPending, store.StatusTranscribed:
		return " (not summarized yet)"
	case store.StatusPartial:
		return " (in progress: summary so far)"
	case store.StatusNeedsReview:
		return " (processing needs review)"
	}
	return ""
}

func hourSummary(h store.HourRecord) (skills.HourSummary, bool) {
	var s skills.HourSummary
	if h.SummaryJSON == "" || json.Unmarshal([]byte(h.SummaryJSON), &s) != nil {
		return s, false
	}
	return s, true
}

func writeSummary(b *strings.Builder, s skills.HourSummary, timeline bool) {
	if s.MainFocus != "" {
		fmt.Fprintf(b, "Main focus: %s\n", s.MainFocus)
	}
	if s.Narrative != "" {
		fmt.Fprintf(b, "Narrative: %s\n", s.Narrative)
	}
	if timeline && len(s.Timeline) > 0 {
		b.WriteString("Timeline:\n")
		for _, e := range s.Timeline {
			fmt.Fprintf(b, "- %s–%s %s: %s\n", e.Start, e.End, e.App, e.Activity)
		}
	}
	if len(s.Notes) > 0 {
		b.WriteString("Notes:\n")
		for _, n := range s.Notes {
			fmt.Fprintf(b, "- %s\n", n)
		}
	}
	writeActions(b, s.ActionItems)
}

func writeActions(b *strings.Builder, items []skills.ActionItem) {
	if len(items) == 0 {
		return
	}
	b.WriteString("Action items:\n")
	for _, a := range items {
		if a.Owner != "" {
			fmt.Fprintf(b, "- %s (owner: %s)\n", a.Text, a.Owner)
		} else {
			fmt.Fprintf(b, "- %s\n", a.Text)
		}
	}
}

const withheldNote = "Speech withheld: the privacy check failed for this hour, so its speech is not available."

// writePrivacy adds the hour's removed-span notices (no content) and the
// withheld note; it returns the derived doc, nil when there is none.
func (j *Journal) writePrivacy(b *strings.Builder, day string, hour int) *process.HourDoc {
	doc, err := process.Load(j.Paths.DerivedHour(day, hour))
	if err != nil {
		return nil
	}
	for _, l := range process.RemovedNotices(doc.Removed) {
		b.WriteString(l + "\n")
	}
	if doc.SpeechWithheld {
		b.WriteString(withheldNote + "\n")
	}
	return doc
}

// ---- get_hour ----

func (j *Journal) GetHour(a HourArgs) (string, error) {
	day, err := j.day("date", a.Date)
	if err != nil {
		return "", err
	}
	if a.Hour < 0 || a.Hour > 23 {
		return "", fmt.Errorf("hour must be 0-23, got %d", a.Hour)
	}
	if err := offsetArg("transcript_offset", a.TranscriptOffset); err != nil {
		return "", err
	}
	rec, found, err := j.Store.Hour(day, a.Hour)
	if err != nil {
		return "", err
	}
	segs, err := j.Store.SegmentsOn(day)
	if err != nil {
		return "", err
	}
	segs = slices.DeleteFunc(segs, func(g store.SegmentRecord) bool { return g.Hour != a.Hour })

	var b strings.Builder
	fmt.Fprintf(&b, "# %s %02d:00–%02d:00\n", day, a.Hour, (a.Hour+1)%24)
	if found {
		fmt.Fprintf(&b, "Status: %s%s\n", rec.Status, statusNote(rec.Status))
	}
	fmt.Fprintf(&b, "Dashboard: %s\n", j.link(fmt.Sprintf("/hour/%s/%02d", day, a.Hour)))
	if found {
		if s, ok := hourSummary(rec); ok {
			writeSummary(&b, s, true)
		}
	}
	if len(segs) > 0 {
		b.WriteString("Activity:\n")
		for _, g := range segs {
			if g.App == "" {
				continue
			}
			line := "- " + g.Start.Local().Format("15:04") + " " + g.App
			if g.Title != "" {
				line += " — " + g.Title
			}
			cat := g.Category
			if cat == "" {
				cat = "uncategorized"
			}
			fmt.Fprintf(&b, "%s (%s, %d min)\n", line, cat, minutes(g.End.Sub(g.Start)))
		}
	}
	meets, err := j.Store.MeetingsOn(day)
	if err != nil {
		return "", err
	}
	hs := hourStart(day, a.Hour)
	var mlist []string
	for _, m := range meets {
		if m.Start.Before(hs.Add(time.Hour)) && m.End.After(hs) {
			mlist = append(mlist, fmt.Sprintf("%s (%s, id %s)", m.Title, clockSpan(m.Start, m.End), m.ID))
		}
	}
	if len(mlist) > 0 {
		b.WriteString("Meetings: " + strings.Join(mlist, "; ") + "\n")
	}
	doc := j.writePrivacy(&b, day, a.Hour)
	if !found && doc == nil && len(segs) == 0 {
		b.WriteString("Nothing recorded for this hour.\n")
		return b.String(), nil
	}
	if doc == nil || doc.SpeechWithheld {
		return paged(b.String(), nil, 0, "transcript_offset", "transcript lines"), nil
	}
	speech := visibleSpeech(doc)
	if !a.IncludeTranscript {
		if len(speech) > 0 {
			fmt.Fprintf(&b, "Transcript: %d transcript lines; call again with include_transcript=true to read them.\n", len(speech))
		}
		return paged(b.String(), nil, 0, "transcript_offset", "transcript lines"), nil
	}
	if len(speech) == 0 {
		b.WriteString("Transcript: none for this hour.\n")
		return b.String(), nil
	}
	fmt.Fprintf(&b, "\n## Transcript (%d lines)\n", len(speech))
	lines := make([]string, len(speech))
	for i, u := range speech {
		lines[i] = u.Start.Local().Format("15:04:05") + " " + u.Speaker + ": " + u.Text
	}
	return paged(b.String(), lines, a.TranscriptOffset, "transcript_offset", "transcript lines"), nil
}

func hourStart(day string, hour int) time.Time { return hourStartIn(day, hour, time.Local) }

// hourStartIn is the wall-clock hour:00 of day in loc (right on DST days,
// unlike midnight plus hours).
func hourStartIn(day string, hour int, loc *time.Location) time.Time {
	t, _ := time.ParseInLocation("2006-01-02", day, loc)
	return time.Date(t.Year(), t.Month(), t.Day(), hour, 0, 0, 0, loc)
}

// visibleSpeech drops utterances overlapping an excluded or paused span
// (the pipeline already does; defence in depth).
func visibleSpeech(d *process.HourDoc) []media.Utterance {
	var out []media.Utterance
	for _, u := range d.Transcript {
		if !slices.ContainsFunc(d.Segments, func(g activity.Segment) bool {
			return (g.Kind == activity.KindExcluded || g.Kind == activity.KindPaused) && u.Start.Before(g.End) && g.Start.Before(u.End)
		}) {
			out = append(out, u)
		}
	}
	return out
}

// ---- meetings ----

func meetingSummary(m store.MeetingRecord) (skills.MeetingSummary, bool) {
	var s skills.MeetingSummary
	if m.SummaryJSON == "" || json.Unmarshal([]byte(m.SummaryJSON), &s) != nil {
		return s, false
	}
	return s, true
}

// participants returns the display names of m's speakers and the records by
// speaker label.
func (j *Journal) participants(id string) ([]string, []store.ParticipantRecord, map[string]*store.ParticipantRecord, error) {
	parts, err := j.Store.Participants(id)
	if err != nil {
		return nil, nil, nil, err
	}
	by := make(map[string]*store.ParticipantRecord, len(parts))
	var names []string
	for i := range parts {
		by[parts[i].Speaker] = &parts[i]
		names = append(names, process.SpeakerDisplay(parts[i].Speaker, &parts[i], j.Cfg.NameConfidenceThreshold))
	}
	return names, parts, by, nil
}

func (j *Journal) ListMeetings(a ListMeetingsArgs) (string, error) {
	if strings.TrimSpace(a.From) == "" || strings.TrimSpace(a.To) == "" {
		return "", errors.New("from and to are required")
	}
	from, to, err := j.dayRange(a.From, a.To, "", "", maxMeetingDays)
	if err != nil {
		return "", err
	}
	if err := offsetArg("offset", a.Offset); err != nil {
		return "", err
	}
	person := strings.ToLower(strings.TrimSpace(a.Person))
	var items []string
	for _, d := range days(from, to) {
		meets, err := j.Store.MeetingsOn(d)
		if err != nil {
			return "", err
		}
		for _, m := range meets {
			names, parts, _, err := j.participants(m.ID)
			if err != nil {
				return "", err
			}
			if person != "" && !slices.ContainsFunc(parts, func(p store.ParticipantRecord) bool {
				return strings.Contains(strings.ToLower(p.Name), person)
			}) {
				continue
			}
			line := fmt.Sprintf("- %s %s · %s (%s) · id %s", m.Day, clockSpan(m.Start, m.End), m.Title, m.App, m.ID)
			if len(names) > 0 {
				line += " · with " + strings.Join(names, ", ")
			}
			if ms, ok := meetingSummary(m); ok && ms.Summary != "" {
				line += " · " + firstLine(ms.Summary, 200)
			}
			items = append(items, line+" · "+j.link("/meeting/"+m.ID))
		}
	}
	scope := from + " to " + to
	if person != "" {
		scope += " with " + strings.TrimSpace(a.Person)
	}
	if len(items) == 0 {
		return "No meetings " + scope + ".", nil
	}
	return paged(fmt.Sprintf("Meetings %s (%d):\n", scope, len(items)), items, a.Offset, "offset", "meetings"), nil
}

func (j *Journal) GetMeeting(a MeetingArgs) (string, error) {
	if !meetingIDRE.MatchString(a.ID) {
		return "", fmt.Errorf("bad meeting id %q (ids look like 2026-10-07-0910-zoom)", a.ID)
	}
	if err := offsetArg("offset", a.Offset); err != nil {
		return "", err
	}
	m, found, err := j.Store.Meeting(a.ID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("no meeting with id %q; use list_meetings to find ids", a.ID)
	}
	names, _, by, err := j.participants(m.ID)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\nWhen: %s %s (%s)\nId: %s\nDashboard: %s\n", m.Title, m.Day, clockSpan(m.Start, m.End), m.App, m.ID, j.link("/meeting/"+m.ID))
	if len(names) > 0 {
		b.WriteString("Participants: " + strings.Join(names, ", ") + "\n")
	}
	if ms, ok := meetingSummary(m); ok {
		if ms.Summary != "" {
			b.WriteString("Summary: " + ms.Summary + "\n")
		}
		if len(ms.Decisions) > 0 {
			b.WriteString("Decisions:\n")
			for _, d := range ms.Decisions {
				b.WriteString("- " + d + "\n")
			}
		}
		writeActions(&b, ms.ActionItems)
	} else {
		b.WriteString("Not summarized yet.\n")
	}

	var lines []string
	for _, t := range process.HourStarts(m.Start, m.End, time.Local) {
		doc, err := process.Load(j.Paths.DerivedHour(config.DayKey(t), t.Hour()))
		if err != nil {
			continue
		}
		if doc.SpeechWithheld {
			fmt.Fprintf(&b, "Speech of %s %02d:00 is withheld (the privacy check failed).\n", config.DayKey(t), t.Hour())
			continue
		}
		for _, l := range process.RemovedNotices(doc.Removed) {
			b.WriteString(l + "\n")
		}
		for _, u := range visibleSpeech(doc) {
			if u.Start.Before(m.Start) || u.Start.After(m.End) {
				continue
			}
			lines = append(lines, u.Start.Local().Format("15:04:05")+" "+
				process.SpeakerDisplay(u.Speaker, by[u.Speaker], j.Cfg.NameConfidenceThreshold)+": "+u.Text)
		}
	}
	if !a.IncludeTranscript {
		if len(lines) > 0 {
			fmt.Fprintf(&b, "Transcript: %d transcript lines; call again with include_transcript=true to read them.\n", len(lines))
		}
		return paged(b.String(), nil, 0, "offset", "transcript lines"), nil
	}
	if len(lines) == 0 {
		b.WriteString("Transcript: none available.\n")
		return b.String(), nil
	}
	fmt.Fprintf(&b, "\n## Transcript (%d lines)\n", len(lines))
	return paged(b.String(), lines, a.Offset, "offset", "transcript lines"), nil
}

// ---- time_report ----

type row struct {
	name string
	d    time.Duration
}

func (j *Journal) TimeReport(a TimeReportArgs) (string, error) {
	if strings.TrimSpace(a.From) == "" || strings.TrimSpace(a.To) == "" {
		return "", errors.New("from and to are required")
	}
	from, to, err := j.dayRange(a.From, a.To, "", "", maxReportDays)
	if err != nil {
		return "", err
	}
	var rows []row
	label := ""
	switch a.GroupBy {
	case "category":
		label = "Category"
		totals, err := j.Store.CategoryTotals(from, to)
		if err != nil {
			return "", err
		}
		for c, d := range totals {
			rows = append(rows, row{c, d})
		}
		sortRows(rows)
	case "day":
		label = "Day"
		for _, d := range days(from, to) {
			totals, err := j.Store.CategoryTotals(d, d)
			if err != nil {
				return "", err
			}
			var sum time.Duration
			for _, v := range totals {
				sum += v
			}
			if sum > 0 {
				rows = append(rows, row{d, sum})
			}
		}
	case "app":
		label = "App"
		by := map[string]time.Duration{}
		for _, d := range days(from, to) {
			segs, err := j.Store.SegmentsOn(d)
			if err != nil {
				return "", err
			}
			for _, g := range segs {
				if g.App != "" && g.Kind != activity.KindExcluded && g.Kind != activity.KindPaused {
					by[g.App] += g.End.Sub(g.Start)
				}
			}
		}
		for app, d := range by {
			rows = append(rows, row{app, d})
		}
		sortRows(rows)
	default:
		return "", fmt.Errorf("group_by must be category, day or app; got %q", a.GroupBy)
	}
	title := fmt.Sprintf("Minutes by %s, %s to %s", a.GroupBy, from, to)
	if a.GroupBy == "app" {
		title += " (all recorded app time)"
	} else {
		title += " (categorized time)"
	}
	if len(rows) == 0 {
		return title + ": no recorded time.", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n| %s | Minutes |\n|---|---|\n", title, label)
	var total time.Duration
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %d |\n", r.name, minutes(r.d))
		total += r.d
	}
	fmt.Fprintf(&b, "| Total | %d |\n", minutes(total))
	return paged(b.String(), nil, 0, "offset", "rows"), nil
}

func sortRows(rows []row) {
	sort.Slice(rows, func(a, b int) bool {
		if rows[a].d != rows[b].d {
			return rows[a].d > rows[b].d
		}
		return rows[a].name < rows[b].name
	})
}

// ---- get_week ----

func (j *Journal) GetWeek(a WeekArgs) (string, error) {
	week, ds, err := j.week(a.Week)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Week %s (%s – %s)\nDashboard: %s\n\n## Weekly rollup\n", week, ds[0], ds[6], j.link("/week/"+week))
	md, err := readOptional(j.Paths.WeeklyJournal(week))
	if err != nil {
		return "", err
	}
	if md == "" {
		b.WriteString("No weekly rollup has been written for this week yet.\n")
	} else {
		b.WriteString(strings.TrimSpace(md) + "\n")
	}
	b.WriteString("\n## Minutes per day (categorized time)\n")
	for _, d := range ds {
		totals, err := j.Store.CategoryTotals(d, d)
		if err != nil {
			return "", err
		}
		t, _ := time.ParseInLocation("2006-01-02", d, time.Local)
		lines := j.totalLines(totals, "%s %d min")
		if len(lines) == 0 {
			fmt.Fprintf(&b, "- %s (%s): no categorized time\n", d, t.Format("Mon"))
			continue
		}
		fmt.Fprintf(&b, "- %s (%s): %s\n", d, t.Format("Mon"), strings.Join(lines, ", "))
	}
	return paged(b.String(), nil, 0, "offset", "days"), nil
}

// ---- shared ----

// totalLines formats category totals (format takes name and minutes) in
// the configured category order, then any others by name.
func (j *Journal) totalLines(totals map[string]time.Duration, format string) []string {
	var names []string
	for _, c := range j.Cfg.Categories {
		if totals[c] > 0 {
			names = append(names, c)
		}
	}
	var extra []string
	for c, d := range totals {
		if d > 0 && !slices.Contains(names, c) {
			extra = append(extra, c)
		}
	}
	sort.Strings(extra)
	var out []string
	for _, c := range append(names, extra...) {
		out = append(out, fmt.Sprintf(format, c, minutes(totals[c])))
	}
	return out
}

func clockSpan(a, b time.Time) string {
	return a.Local().Format("15:04") + "–" + b.Local().Format("15:04")
}

// readOptional returns a journal file's text, "" when it does not exist.
func readOptional(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err // never name the data directory
		}
		return "", fmt.Errorf("read journal entry: %w", err)
	}
	return string(b), nil
}
