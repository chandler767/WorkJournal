// Package journal writes the Markdown journals: the daily entry (close-day
// skill) and the weekly rollup (weekly-rollup skill).
package journal

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"

	"workjournal/internal/config"
	"workjournal/internal/skills"
	"workjournal/internal/store"
)

// Skill input shapes; each matches the "Input" section of its SKILL.md.
type (
	closeDayInput struct {
		Day      string         `json:"day"`
		Hours    []hourInput    `json:"hours"`
		Meetings []meetingInput `json:"meetings"`
		Totals   map[string]int `json:"totals"`
	}
	hourInput struct {
		Hour        int                 `json:"hour"`
		Notes       []string            `json:"notes"`
		MainFocus   string              `json:"main_focus"`
		ActionItems []skills.ActionItem `json:"action_items"`
	}
	meetingInput struct {
		ID           string              `json:"id"`
		Title        string              `json:"title"`
		Start        string              `json:"start"`
		End          string              `json:"end"`
		Participants []string            `json:"participants"`
		Summary      string              `json:"summary"`
		Decisions    []string            `json:"decisions"`
		ActionItems  []skills.ActionItem `json:"action_items"`
	}
)

// CloseDay writes journal/<day>.md from the day's hour summaries, meetings
// and category totals. Pending hours are processed first through
// processPending (may be nil); its failure is logged and does not stop the
// close. A day with no hours writes nothing and returns nil.
func CloseDay(ctx context.Context, r skills.Runner, st *store.Store, p config.Paths, day string, processPending func(context.Context) error) error {
	if processPending != nil {
		if err := processPending(ctx); err != nil {
			log.Printf("journal: close %s: processing pending hours: %v", day, err)
		}
	}
	hours, err := st.HoursOn(day)
	if err != nil {
		return err
	}
	if len(hours) == 0 {
		return nil
	}
	in := closeDayInput{Day: day, Hours: []hourInput{}, Meetings: []meetingInput{}}
	for _, h := range hours {
		if h.SummaryJSON == "" {
			continue
		}
		var s skills.HourSummary
		if err := json.Unmarshal([]byte(h.SummaryJSON), &s); err != nil {
			return fmt.Errorf("journal: hour %s %02d summary: %w", day, h.Hour, err)
		}
		in.Hours = append(in.Hours, hourInput{Hour: h.Hour, Notes: nonNil(s.Notes), MainFocus: s.MainFocus, ActionItems: nonNilItems(s.ActionItems)})
	}
	meetings, err := st.MeetingsOn(day)
	if err != nil {
		return err
	}
	for _, m := range meetings {
		mi := meetingInput{ID: m.ID, Title: m.Title, Start: m.Start.Format("15:04"), End: m.End.Format("15:04"),
			Participants: []string{}, Decisions: []string{}, ActionItems: []skills.ActionItem{}}
		var ms skills.MeetingSummary
		if m.SummaryJSON != "" {
			if err := json.Unmarshal([]byte(m.SummaryJSON), &ms); err != nil {
				return fmt.Errorf("journal: meeting %s summary: %w", m.ID, err)
			}
		}
		mi.Summary, mi.Decisions, mi.ActionItems = ms.Summary, nonNil(ms.Decisions), nonNilItems(ms.ActionItems)
		parts, err := st.Participants(m.ID)
		if err != nil {
			return err
		}
		for _, pt := range parts {
			name := pt.Name
			if name == "" {
				name = pt.Speaker
			}
			mi.Participants = append(mi.Participants, name)
		}
		in.Meetings = append(in.Meetings, mi)
	}
	if in.Totals, err = minuteTotals(st, day, day); err != nil {
		return err
	}
	path := filepath.Join(filepath.Dir(p.DerivedHour(day, 0)), ".inputs", "close-day.json")
	var out skills.Markdown
	if err := runSkill(ctx, r, "close-day", path, in, &out); err != nil {
		return fmt.Errorf("journal: close %s: %w", day, err)
	}
	return writeAtomic(p.DailyJournal(day), out.Markdown)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilItems(s []skills.ActionItem) []skills.ActionItem {
	if s == nil {
		return []skills.ActionItem{}
	}
	return s
}

// minuteTotals is store.CategoryTotals in whole minutes.
func minuteTotals(st *store.Store, from, to string) (map[string]int, error) {
	t, err := st.CategoryTotals(from, to)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int, len(t))
	for c, d := range t {
		if c != "" {
			out[c] = int(math.Round(d.Minutes()))
		}
	}
	return out, nil
}

// runSkill writes the input JSON to path and runs the skill on it.
func runSkill(ctx context.Context, r skills.Runner, skill, path string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("write skill input: %w", err)
	}
	return r.Run(ctx, skill, path, out)
}

// writeAtomic writes via a temp file in the same directory and a rename.
func writeAtomic(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".journal-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.WriteString(content)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(tmp)
		return werr
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
