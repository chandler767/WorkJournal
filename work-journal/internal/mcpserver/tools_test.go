package mcpserver

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"workjournal/internal/media"
	"workjournal/internal/process"
	"workjournal/internal/store"
)

func contains(t *testing.T, out string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q\n---\n%s", s, out)
		}
	}
}

func lacks(t *testing.T, out string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if strings.Contains(out, s) {
			t.Errorf("output contains %q\n---\n%s", s, out)
		}
	}
}

func TestSearch(t *testing.T) {
	j := newFixture(t)
	out, err := j.Search(SearchArgs{Query: "release Friday"})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "[transcript] 2026-10-07 09:12", "hour 09", "[meeting]", "http://127.0.0.1:4747/hour/2026-10-07/09#720",
		"http://127.0.0.1:4747/meeting/"+fxMeeting, "[release]")
	lacks(t, out, "WITHHELDSECRET", "2026-10-08")

	out, err = j.Search(SearchArgs{Query: "release", From: fxNextDay, To: fxNextDay})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "2026-10-08", "/meeting/"+fxMeet2)
	lacks(t, out, "2026-10-07 09")

	out, err = j.Search(SearchArgs{Query: "nothingmatchesthis"})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "No matches")
}

func TestSearchPagesWithOffset(t *testing.T) {
	j := newFixture(t)
	first, err := j.Search(SearchArgs{Query: "release", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, first, "offset=1")
	second, err := j.Search(SearchArgs{Query: "release", Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("offset returned the same page")
	}
}

func TestArgumentValidation(t *testing.T) {
	j := newFixture(t)
	cases := []struct {
		name string
		call func() (string, error)
		want string
	}{
		{"search empty", func() (string, error) { return j.Search(SearchArgs{Query: "  "}) }, "query"},
		{"search limit", func() (string, error) { return j.Search(SearchArgs{Query: "x", Limit: 51}) }, "limit"},
		{"search from", func() (string, error) { return j.Search(SearchArgs{Query: "x", From: "07/10/2026"}) }, "YYYY-MM-DD"},
		{"search reversed", func() (string, error) { return j.Search(SearchArgs{Query: "x", From: fxNextDay, To: fxDay}) }, "after"},
		{"day bad", func() (string, error) { return j.GetDay(DayArgs{Date: "2026-13-40"}) }, "YYYY-MM-DD"},
		{"hour range", func() (string, error) { return j.GetHour(HourArgs{Date: fxDay, Hour: 24}) }, "hour"},
		{"hour offset", func() (string, error) { return j.GetHour(HourArgs{Date: fxDay, Hour: 9, TranscriptOffset: -1}) }, "offset"},
		{"meeting id", func() (string, error) { return j.GetMeeting(MeetingArgs{ID: "../../etc/passwd"}) }, "meeting id"},
		{"meeting unknown", func() (string, error) { return j.GetMeeting(MeetingArgs{ID: "2026-10-07-0000-none"}) }, "no meeting"},
		{"meetings range", func() (string, error) { return j.ListMeetings(ListMeetingsArgs{From: "2025-01-01", To: "2026-12-31"}) }, "days"},
		{"report group", func() (string, error) {
			return j.TimeReport(TimeReportArgs{From: fxDay, To: fxDay, GroupBy: "person"})
		}, "group_by"},
		{"week bad", func() (string, error) { return j.GetWeek(WeekArgs{Week: "2026-41"}) }, "YYYY-Www"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := c.call()
			if err == nil {
				t.Fatalf("no error; output:\n%s", out)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not mention %q", err, c.want)
			}
		})
	}
}

func TestRelativeDates(t *testing.T) {
	j := newFixture(t)
	today, err := j.GetDay(DayArgs{Date: "today"})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, today, "2026-10-07", "Scheduler work")
	j.Now = func() time.Time { return fxTime(fxNextDay, 9, 0) }
	yest, err := j.GetDay(DayArgs{Date: "yesterday"})
	if err != nil {
		t.Fatal(err)
	}
	if yest != today {
		t.Fatalf("yesterday from the 8th differs from the 7th:\n%s\n---\n%s", yest, today)
	}
	week, err := j.GetWeek(WeekArgs{Week: "yesterday"})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, week, "2026-W41")
}

func TestGetDay(t *testing.T) {
	j := newFixture(t)
	out, err := j.GetDay(DayArgs{Date: fxDay})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out,
		"Shipped the scheduler retry work", // journal markdown
		"Deep work: 40 min", "Meetings: 20 min", "Communication: 20 min",
		"09:10–09:30 Standup (zoom.us)", "id "+fxMeeting, "The team agreed to ship the release on Friday.",
		"09:00 — done", "Rewrote the scheduler retry loop", "Main focus: Scheduler work", "Refactored the scheduler retry loop",
		"Send release notes (owner: Priya)", "4 min of non-work audio removed (09:40–09:44)",
		"10:00 — needs_review", "Speech withheld",
		"11:00 — pending", "http://127.0.0.1:4747/hour/2026-10-07/09", "http://127.0.0.1:4747/day/2026-10-07")
	empty, err := j.GetDay(DayArgs{Date: "2026-10-01"})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, empty, "No journal entry", "No hours recorded")
}

func TestGetHour(t *testing.T) {
	j := newFixture(t)
	out, err := j.GetHour(HourArgs{Date: fxDay, Hour: 9})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "Status: done", "Rewrote the scheduler retry loop", "09:10–09:30 zoom.us: Standup meeting about the release",
		"Refactored the scheduler retry loop", "Send release notes (owner: Priya)", "4 min of non-work audio removed",
		"Meetings: Standup", "3 transcript lines", "include_transcript=true",
		"09:00 Code — scheduler.go (Deep work, 10 min)")
	lacks(t, out, "Let us ship")

	out, err = j.GetHour(HourArgs{Date: fxDay, Hour: 9, IncludeTranscript: true})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "09:12:00 Speaker 1: Let us ship the release on Friday", "09:14:00 You: Sounds good")

	out, err = j.GetHour(HourArgs{Date: fxDay, Hour: 9, IncludeTranscript: true, TranscriptOffset: 2})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "I can review it tomorrow")
	lacks(t, out, "Let us ship")
}

func TestWithheldHourReturnsNoteNotSpeech(t *testing.T) {
	j := newFixture(t)
	out, err := j.GetHour(HourArgs{Date: fxDay, Hour: 10, IncludeTranscript: true})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "Status: needs_review", "Speech withheld")
	lacks(t, out, "WITHHELDSECRET")
}

func TestGetHourWithoutData(t *testing.T) {
	j := newFixture(t)
	out, err := j.GetHour(HourArgs{Date: fxDay, Hour: 3})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "Nothing recorded")
}

// bigHour adds hour 13 with n long utterances.
func bigHour(t *testing.T, j *Journal, n int) {
	t.Helper()
	doc := process.HourDoc{Day: fxDay, Hour: 13, Status: store.StatusDone}
	for i := range n {
		at := fxTime(fxDay, 13, 0).Add(time.Duration(i) * time.Second)
		doc.Transcript = append(doc.Transcript, media.Utterance{Start: at, End: at.Add(time.Second), Track: "mic", Speaker: "You",
			Text: fmt.Sprintf("line %04d %s", i, strings.Repeat("words ", 30))})
	}
	if err := doc.Save(j.Paths.DerivedHour(fxDay, 13)); err != nil {
		t.Fatal(err)
	}
}

func TestTranscriptTruncatesAndPages(t *testing.T) {
	j := newFixture(t)
	bigHour(t, j, 1000)
	out, err := j.GetHour(HourArgs{Date: fxDay, Hour: 13, IncludeTranscript: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > MaxOutput {
		t.Fatalf("output %d bytes, cap %d", len(out), MaxOutput)
	}
	contains(t, out, "line 0000", "Truncated", "transcript_offset=")
	lacks(t, out, "line 0999")
	// Follow the paging note to the end; every line appears exactly once.
	seen := map[string]int{}
	offset := 0
	for range 100 {
		out, err := j.GetHour(HourArgs{Date: fxDay, Hour: 13, IncludeTranscript: true, TranscriptOffset: offset})
		if err != nil {
			t.Fatal(err)
		}
		if len(out) > MaxOutput {
			t.Fatalf("page at %d is %d bytes", offset, len(out))
		}
		for _, l := range strings.Split(out, "\n") {
			if i := strings.Index(l, "line "); i >= 0 && strings.Contains(l, "You:") {
				seen[l[i:i+9]]++
			}
		}
		i := strings.Index(out, "transcript_offset=")
		if i < 0 {
			break
		}
		if _, err := fmt.Sscanf(out[i:], "transcript_offset=%d", &offset); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 1000 {
		t.Fatalf("saw %d distinct lines, want 1000", len(seen))
	}
	for l, n := range seen {
		if n != 1 {
			t.Fatalf("%s seen %d times", l, n)
		}
	}
	past, err := j.GetHour(HourArgs{Date: fxDay, Hour: 13, IncludeTranscript: true, TranscriptOffset: 5000})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, past, "past the end")
}

func TestListMeetings(t *testing.T) {
	j := newFixture(t)
	out, err := j.ListMeetings(ListMeetingsArgs{From: fxDay, To: fxNextDay})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, fxMeeting, "2026-10-07 09:10–09:30", "zoom.us", "Standup", "Priya", "Speaker 2 (Bob?)",
		"The team agreed to ship the release on Friday.", fxMeet2, "Alice", "http://127.0.0.1:4747/meeting/"+fxMeeting)
	lacks(t, out, "Priya owns the notes") // one-line summary only

	out, err = j.ListMeetings(ListMeetingsArgs{From: fxDay, To: fxNextDay, Person: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, fxMeet2)
	lacks(t, out, fxMeeting)

	out, err = j.ListMeetings(ListMeetingsArgs{From: "today", To: "today", Person: "nobody"})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "No meetings")
}

func TestGetMeeting(t *testing.T) {
	j := newFixture(t)
	out, err := j.GetMeeting(MeetingArgs{ID: fxMeeting})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "Standup", "2026-10-07 09:10–09:30", "The team agreed to ship the release on Friday. Priya owns the notes.",
		"Decisions:", "- Ship the release on Friday", "Send release notes (owner: Priya)", "Participants: Priya, Speaker 2 (Bob?)",
		"3 transcript lines", "include_transcript=true")
	lacks(t, out, "Let us ship")

	out, err = j.GetMeeting(MeetingArgs{ID: fxMeeting, IncludeTranscript: true})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "09:12:00 Priya: Let us ship the release on Friday", "09:16:00 Speaker 2 (Bob?): I can review it tomorrow")

	out, err = j.GetMeeting(MeetingArgs{ID: fxMeeting, IncludeTranscript: true, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	lacks(t, out, "Let us ship")
	contains(t, out, "Sounds good")
}

func TestTimeReport(t *testing.T) {
	j := newFixture(t)
	out, err := j.TimeReport(TimeReportArgs{From: fxDay, To: fxNextDay, GroupBy: "category"})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "| Meetings | 65 |", "| Deep work | 40 |", "| Communication | 20 |", "| Total | 125 |")

	out, err = j.TimeReport(TimeReportArgs{From: fxDay, To: fxNextDay, GroupBy: "day"})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "| 2026-10-07 | 80 |", "| 2026-10-08 | 45 |")

	out, err = j.TimeReport(TimeReportArgs{From: fxDay, To: fxDay, GroupBy: "app"})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "| Code | 40 |", "| zoom.us | 20 |", "| Slack | 20 |")
	if strings.Index(out, "| Code |") > strings.Index(out, "| Slack |") {
		t.Error("rows not sorted by minutes")
	}
}

func TestGetWeek(t *testing.T) {
	j := newFixture(t)
	out, err := j.GetWeek(WeekArgs{Week: "2026-W41"})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "Scheduler reliability", "2026-10-07 (Wed)", "Deep work 40 min", "2026-10-08 (Thu)", "Meetings 45 min",
		"http://127.0.0.1:4747/week/2026-W41")
	none, err := j.GetWeek(WeekArgs{Week: "2026-W30"})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, none, "No weekly rollup")
}

// TestNothingPrivateLeaks runs every tool with transcripts on and checks
// that no output holds a raw media path or file name, the data directory,
// cache/voices/privacy contents, removal reasons or withheld speech.
func TestNothingPrivateLeaks(t *testing.T) {
	j := newFixture(t)
	var all []string
	add := func(s string, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, s)
	}
	for _, q := range []string{"release", "scheduler", "WITHHELDSECRET", "CACHESECRET", "Speaker", "jpg", "m4a"} {
		add(j.Search(SearchArgs{Query: q, Limit: 50}))
	}
	add(j.GetDay(DayArgs{Date: fxDay}))
	for h := range 24 {
		add(j.GetHour(HourArgs{Date: fxDay, Hour: h, IncludeTranscript: true}))
	}
	add(j.ListMeetings(ListMeetingsArgs{From: fxDay, To: fxNextDay}))
	add(j.GetMeeting(MeetingArgs{ID: fxMeeting, IncludeTranscript: true}))
	add(j.GetMeeting(MeetingArgs{ID: fxMeet2, IncludeTranscript: true}))
	for _, g := range []string{"category", "day", "app"} {
		add(j.TimeReport(TimeReportArgs{From: fxDay, To: fxNextDay, GroupBy: g}))
	}
	add(j.GetWeek(WeekArgs{Week: "2026-W41"}))
	out := strings.Join(all, "\n")
	lacks(t, out, append(secrets, j.Paths.Root, ".m4a", ".jpg", "090500", "/raw/", "cache/", "voices", "embedding")...)
}

func TestServerListsReadOnlyToolsAndCalls(t *testing.T) {
	j := newFixture(t)
	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := j.Server().Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
		if (tl.Annotations == nil || !tl.Annotations.ReadOnlyHint) && !slices.Contains(WriteToolNames, tl.Name) {
			t.Errorf("%s: not marked read-only", tl.Name)
		}
		if tl.InputSchema == nil {
			t.Errorf("%s: no input schema", tl.Name)
		}
	}
	want := slices.Sorted(slices.Values(append(slices.Clone(ToolNames), WriteToolNames...)))
	if got := strings.Join(slices.Sorted(slices.Values(names)), ","); got != strings.Join(want, ",") {
		t.Fatalf("tools %s, want %s", got, strings.Join(want, ","))
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_hour", Arguments: map[string]any{"date": fxDay, "hour": 9}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || len(res.Content) != 1 {
		t.Fatalf("get_hour: %+v", res)
	}
	contains(t, res.Content[0].(*mcp.TextContent).Text, "Scheduler work")

	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_hour", Arguments: map[string]any{"date": "nope", "hour": 9}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatalf("bad date not reported as a tool error: %+v", res)
	}
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_hour", Arguments: map[string]any{"date": fxDay}})
	if err == nil && !res.IsError {
		t.Fatal("missing required hour accepted")
	}
}

func TestOpenIsReadOnly(t *testing.T) {
	j := newFixture(t)
	if err := j.Store.SetKV("x", "y"); err == nil {
		t.Fatal("write succeeded through the MCP server's store")
	}
	if err := j.Store.UpsertHour(store.HourRecord{Day: fxDay, Hour: 9, Status: store.StatusPending}); err == nil {
		t.Fatal("hour write succeeded through the MCP server's store")
	}
}

func TestHourStartOnDSTDay(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip(err)
	}
	// 2026-11-01 falls back at 02:00: hour 2 starts at 02:00 EST, not 3h after midnight minus one.
	for _, h := range []int{0, 1, 2, 3, 23} {
		if got := hourStartIn("2026-11-01", h, ny); got.Hour() != h || got.Day() != 1 {
			t.Errorf("hour %d starts %v", h, got)
		}
	}
	if got := hourStartIn("2026-03-08", 3, ny); got.Hour() != 3 {
		t.Errorf("spring forward hour 3 starts %v", got)
	}
}

func TestWeekIsCaseInsensitive(t *testing.T) {
	j := newFixture(t)
	out, err := j.GetWeek(WeekArgs{Week: "2026-w41"})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "Week 2026-W41", "Scheduler reliability")
}
