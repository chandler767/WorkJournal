package journal

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"workjournal/internal/config"
	"workjournal/internal/skills"
	"workjournal/internal/store"
)

type fakeRunner struct {
	skill  string
	input  []byte
	path   string
	md     string
	err    error
	called int
}

func (f *fakeRunner) Run(_ context.Context, skill, inputPath string, out any) error {
	f.called++
	f.skill, f.path = skill, inputPath
	b, err := os.ReadFile(inputPath)
	if err != nil {
		return err
	}
	f.input = b
	if f.err != nil {
		return f.err
	}
	out.(*skills.Markdown).Markdown = f.md
	return nil
}

func setup(t *testing.T) (*store.Store, config.Paths) {
	t.Helper()
	p := config.Paths{Root: t.TempDir()}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, p
}

func at(day string, h, m int) time.Time {
	d, _ := time.ParseInLocation("2006-01-02", day, time.Local)
	return time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, time.Local)
}

func seedDay(t *testing.T, st *store.Store, day string) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.UpsertHour(store.HourRecord{Day: day, Hour: 9, Status: store.StatusDone, UpdatedAt: time.Now(),
		SummaryJSON: `{"notes":["Refactored"],"main_focus":"Scheduler","action_items":[{"text":"Send notes","owner":"You"}]}`}))
	must(st.ReplaceSegments(day, 9, []store.SegmentRecord{
		{Day: day, Hour: 9, Start: at(day, 9, 0), End: at(day, 9, 40), App: "Code", Kind: "app", Category: "Deep work"},
		{Day: day, Hour: 9, Start: at(day, 9, 40), End: at(day, 9, 50), App: "Slack", Kind: "app", Category: "Communication"},
		{Day: day, Hour: 9, Start: at(day, 9, 50), End: at(day, 9, 55), App: "?", Kind: "app", Category: ""},
	}))
	must(st.UpsertMeeting(store.MeetingRecord{ID: "m1", Day: day, Start: at(day, 9, 10), End: at(day, 9, 30), App: "zoom", Title: "Standup",
		SummaryJSON: `{"participants":[],"summary":"Ship Friday","decisions":["Ship"],"action_items":[]}`}))
	must(st.SetParticipant(store.ParticipantRecord{MeetingID: "m1", Speaker: "Speaker 1", Name: "Priya"}))
}

func TestCloseDayWritesMarkdown(t *testing.T) {
	st, p := setup(t)
	seedDay(t, st, "2026-10-07")
	r := &fakeRunner{md: "## Summary\nGood day.\n"}
	pending := 0
	if err := CloseDay(context.Background(), r, st, p, "2026-10-07", func(context.Context) error { pending++; return nil }); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p.DailyJournal("2026-10-07"))
	if err != nil || string(got) != r.md {
		t.Fatalf("journal = %q, %v", got, err)
	}
	if r.skill != "close-day" || pending != 1 {
		t.Errorf("skill %q pending calls %d", r.skill, pending)
	}
	ents, _ := os.ReadDir(filepath.Dir(p.DailyJournal("2026-10-07")))
	if len(ents) != 1 {
		t.Errorf("temp files left behind: %v", ents)
	}
}

func TestCloseDaySkipsEmptyDay(t *testing.T) {
	st, p := setup(t)
	r := &fakeRunner{md: "x"}
	if err := CloseDay(context.Background(), r, st, p, "2026-10-07", nil); err != nil {
		t.Fatal(err)
	}
	if r.called != 0 {
		t.Error("runner called for empty day")
	}
	if _, err := os.Stat(p.DailyJournal("2026-10-07")); !os.IsNotExist(err) {
		t.Errorf("journal written for empty day: %v", err)
	}
}

func TestCloseDayProcessPendingErrorDoesNotStopClose(t *testing.T) {
	st, p := setup(t)
	seedDay(t, st, "2026-10-07")
	r := &fakeRunner{md: "ok"}
	err := CloseDay(context.Background(), r, st, p, "2026-10-07", func(context.Context) error { return errors.New("boom") })
	if err != nil || r.called != 1 {
		t.Fatalf("err %v called %d", err, r.called)
	}
}

func TestCloseDayRunnerErrorWritesNothing(t *testing.T) {
	st, p := setup(t)
	seedDay(t, st, "2026-10-07")
	r := &fakeRunner{err: skills.ErrInvalidOutput}
	if err := CloseDay(context.Background(), r, st, p, "2026-10-07", nil); !errors.Is(err, skills.ErrInvalidOutput) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(p.DailyJournal("2026-10-07")); !os.IsNotExist(err) {
		t.Error("journal written despite failure")
	}
}

func TestCloseDayInputHasTotals(t *testing.T) {
	st, p := setup(t)
	seedDay(t, st, "2026-10-07")
	r := &fakeRunner{md: "ok"}
	if err := CloseDay(context.Background(), r, st, p, "2026-10-07", nil); err != nil {
		t.Fatal(err)
	}
	var in struct {
		Day   string `json:"day"`
		Hours []struct {
			Hour        int      `json:"hour"`
			Notes       []string `json:"notes"`
			MainFocus   string   `json:"main_focus"`
			ActionItems []struct {
				Text, Owner string
			} `json:"action_items"`
		} `json:"hours"`
		Meetings []struct {
			ID           string   `json:"id"`
			Title        string   `json:"title"`
			Start        string   `json:"start"`
			End          string   `json:"end"`
			Participants []string `json:"participants"`
			Summary      string   `json:"summary"`
			Decisions    []string `json:"decisions"`
		} `json:"meetings"`
		Totals map[string]int `json:"totals"`
	}
	if err := json.Unmarshal(r.input, &in); err != nil {
		t.Fatal(err)
	}
	if want := map[string]int{"Deep work": 40, "Communication": 10}; !reflect.DeepEqual(in.Totals, want) {
		t.Errorf("totals = %v, want %v", in.Totals, want)
	}
	if in.Day != "2026-10-07" || len(in.Hours) != 1 || in.Hours[0].Hour != 9 || in.Hours[0].MainFocus != "Scheduler" || in.Hours[0].Notes[0] != "Refactored" || in.Hours[0].ActionItems[0].Text != "Send notes" {
		t.Errorf("hours = %+v", in.Hours)
	}
	if len(in.Meetings) != 1 || in.Meetings[0].Title != "Standup" || in.Meetings[0].Start != "09:10" || in.Meetings[0].End != "09:30" ||
		!reflect.DeepEqual(in.Meetings[0].Participants, []string{"Priya"}) || in.Meetings[0].Summary != "Ship Friday" {
		t.Errorf("meetings = %+v", in.Meetings)
	}
	if want := filepath.Join(p.Root, "derived", "2026-10-07", ".inputs", "close-day.json"); r.path != want {
		t.Errorf("input path %s, want %s", r.path, want)
	}
}

func TestWeekDays(t *testing.T) {
	cases := []struct {
		week, first, last string
	}{
		{"2026-W41", "2026-10-05", "2026-10-11"},
		{"2026-W53", "2026-12-28", "2027-01-03"},
		{"2027-W01", "2027-01-04", "2027-01-10"},
		{"2025-W01", "2024-12-30", "2025-01-05"},
	}
	for _, c := range cases {
		d, err := WeekDays(c.week)
		if err != nil || len(d) != 7 || d[0] != c.first || d[6] != c.last {
			t.Errorf("%s: %v %v", c.week, d, err)
		}
	}
	for _, bad := range []string{"2027-W53", "2026-W00", "2026-W54", "x", "2026-10-07"} {
		if _, err := WeekDays(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestRollupUsesPriorWeekTotals(t *testing.T) {
	st, p := setup(t)
	// 2026-W41 is 2026-10-05..11; prior week is 09-28..10-04.
	seedDay(t, st, "2026-10-07")
	seedDay(t, st, "2026-09-30")
	seedDay(t, st, "2026-10-14") // next week: must not count
	for _, d := range []string{"2026-10-06", "2026-10-07"} {
		if err := os.WriteFile(p.DailyJournal(d), []byte("## Summary\n"+d+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := &fakeRunner{md: "## Themes\nx\n"}
	if err := Rollup(context.Background(), r, st, p, "2026-W41"); err != nil {
		t.Fatal(err)
	}
	var in struct {
		Week string `json:"week"`
		Days []struct {
			Day      string `json:"day"`
			Markdown string `json:"markdown"`
		} `json:"days"`
		Totals      map[string]int `json:"totals"`
		PriorTotals map[string]int `json:"prior_totals"`
	}
	if err := json.Unmarshal(r.input, &in); err != nil {
		t.Fatal(err)
	}
	if r.skill != "weekly-rollup" || in.Week != "2026-W41" || len(in.Days) != 2 || in.Days[0].Day != "2026-10-06" || in.Days[1].Markdown != "## Summary\n2026-10-07\n" {
		t.Errorf("skill %q input %+v", r.skill, in)
	}
	want := map[string]int{"Deep work": 40, "Communication": 10}
	if !reflect.DeepEqual(in.Totals, want) || !reflect.DeepEqual(in.PriorTotals, want) {
		t.Errorf("totals %v prior %v", in.Totals, in.PriorTotals)
	}
	if want := filepath.Join(p.Root, "derived", ".inputs", "2026-W41-weekly-rollup.json"); r.path != want {
		t.Errorf("input path %s", r.path)
	}
	got, err := os.ReadFile(p.WeeklyJournal("2026-W41"))
	if err != nil || string(got) != r.md {
		t.Fatalf("weekly = %q, %v", got, err)
	}
}

func TestRollupWithoutDailyJournalsWritesNothing(t *testing.T) {
	st, p := setup(t)
	seedDay(t, st, "2026-10-07")
	r := &fakeRunner{md: "x"}
	if err := Rollup(context.Background(), r, st, p, "2026-W41"); err != nil {
		t.Fatal(err)
	}
	if r.called != 0 {
		t.Error("runner called")
	}
	if _, err := os.Stat(p.WeeklyJournal("2026-W41")); !os.IsNotExist(err) {
		t.Error("weekly journal written")
	}
}

// I3: an hour without a summary is listed, not silently dropped.
func TestCloseDayIncludesHoursWithoutSummary(t *testing.T) {
	st, p := setup(t)
	seedDay(t, st, "2026-10-07")
	if err := st.UpsertHour(store.HourRecord{Day: "2026-10-07", Hour: 11, Status: store.StatusNeedsReview, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{md: "ok"}
	if err := CloseDay(context.Background(), r, st, p, "2026-10-07", nil); err != nil {
		t.Fatal(err)
	}
	var in struct {
		Hours []struct {
			Hour  int      `json:"hour"`
			Notes []string `json:"notes"`
		} `json:"hours"`
	}
	if err := json.Unmarshal(r.input, &in); err != nil {
		t.Fatal(err)
	}
	if len(in.Hours) != 2 || in.Hours[1].Hour != 11 || !reflect.DeepEqual(in.Hours[1].Notes, []string{"(summary unavailable — needs review)"}) {
		t.Fatalf("hours = %+v", in.Hours)
	}
}
