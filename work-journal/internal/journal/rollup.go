package journal

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"workjournal/internal/config"
	"workjournal/internal/skills"
	"workjournal/internal/store"
)

type (
	rollupInput struct {
		Week        string         `json:"week"`
		Days        []rollupDay    `json:"days"`
		Totals      map[string]int `json:"totals"`
		PriorTotals map[string]int `json:"prior_totals"`
	}
	rollupDay struct {
		Day      string   `json:"day"`
		Markdown []string `json:"markdown_lines"` // skills.TextLines
	}
)

// Rollup writes journal/<week>.md from the week's daily journals plus the
// category totals of that week and the one before. A week without daily
// journals writes nothing and returns nil.
func Rollup(ctx context.Context, r skills.Runner, st *store.Store, p config.Paths, week string) error {
	days, err := WeekDays(week)
	if err != nil {
		return err
	}
	in := rollupInput{Week: week, Days: []rollupDay{}}
	for _, d := range days {
		b, err := os.ReadFile(p.DailyJournal(d))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		in.Days = append(in.Days, rollupDay{Day: d, Markdown: skills.TextLines(string(b))})
	}
	if len(in.Days) == 0 {
		return nil
	}
	if in.Totals, err = minuteTotals(st, days[0], days[6]); err != nil {
		return err
	}
	monday, err := time.Parse("2006-01-02", days[0])
	if err != nil {
		return err
	}
	prior := monday.AddDate(0, 0, -7)
	if in.PriorTotals, err = minuteTotals(st, prior.Format("2006-01-02"), prior.AddDate(0, 0, 6).Format("2006-01-02")); err != nil {
		return err
	}
	path := filepath.Join(p.Root, "derived", ".inputs", week+"-weekly-rollup.json")
	var out skills.Markdown
	if err := runSkill(ctx, r, "weekly-rollup", path, in, &out); err != nil {
		return fmt.Errorf("journal: rollup %s: %w", week, err)
	}
	return writeAtomic(p.WeeklyJournal(week), out.Markdown)
}
