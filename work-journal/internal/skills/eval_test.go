//go:build skills

package skills

// Skill evals: run every plugin fixture through the real claude CLI with the
// installed skills (`make skills-eval`). Run() itself enforces each schema.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// evalSlots bounds concurrent claude processes.
var evalSlots = make(chan struct{}, 4)

// evalCategories is the allowed list in the categorize-time fixtures.
var evalCategories = []string{"Deep work", "Meetings", "Communication", "Admin", "Browsing/Research", "Break/Idle"}

// evalCategorization checks categorize-time output like the summarizer does.
type evalCategorization struct {
	Categorization
}

func (c *evalCategorization) Validate() error {
	for _, g := range c.Segments {
		if !slices.Contains(evalCategories, g.Category) {
			return fmt.Errorf("category %q not allowed", g.Category)
		}
	}
	return nil
}

func TestSkillEvals(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".claude", "skills", installedPrefix+"summarize-hour", "SKILL.md")); err != nil {
		t.Fatalf("skills not installed under %s/.claude/skills: %v", repo, err)
	}
	for _, skill := range Names {
		fixtures, err := filepath.Glob(filepath.Join("..", "..", "skills", "work-journal", "skills", skill, "fixtures", "*-input.json"))
		if err != nil || len(fixtures) == 0 {
			t.Fatalf("%s: no fixtures (%v)", skill, err)
		}
		for _, fx := range fixtures {
			name := skill + "/" + strings.TrimSuffix(filepath.Base(fx), "-input.json")
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				evalSlots <- struct{}{}
				defer func() { <-evalSlots }()
				data := t.TempDir() // input lives outside WorkDir, as in the app
				b, err := os.ReadFile(fx)
				if err != nil {
					t.Fatal(err)
				}
				in := filepath.Join(data, "input.json")
				if err := os.WriteFile(in, b, 0o600); err != nil {
					t.Fatal(err)
				}
				r := &ClaudeRunner{Bin: "claude", WorkDir: repo, DataDir: data}
				out := outputFor(skill)
				began := time.Now()
				err = r.Run(context.Background(), skill, in, out)
				t.Logf("%s: %s", name, time.Since(began).Round(time.Second))
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				check(t, name, out)
			})
		}
	}
}

func outputFor(skill string) any {
	switch skill {
	case "summarize-hour":
		return &HourSummary{}
	case "categorize-time":
		return &evalCategorization{}
	case "summarize-meeting":
		return &MeetingSummary{}
	default:
		return &Markdown{}
	}
}

func check(t *testing.T, name string, out any) {
	t.Helper()
	switch name {
	case "summarize-hour/03": // empty hour
		h := out.(*HourSummary)
		if len(h.Notes) != 0 || h.MainFocus != "" || len(h.ActionItems) != 0 {
			t.Errorf("empty hour gave %+v", *h)
		}
	case "categorize-time/01", "categorize-time/02", "categorize-time/03":
		c := out.(*evalCategorization)
		want := map[string]int{"categorize-time/01": 3, "categorize-time/02": 3, "categorize-time/03": 0}[name]
		if len(c.Segments) != want {
			t.Errorf("%d entries, want %d: %+v", len(c.Segments), want, c.Segments)
		}
		if name == "categorize-time/02" {
			for _, g := range c.Segments {
				if g.Index == 1 && g.Category != "Break/Idle" {
					t.Errorf("loginwindow categorized %q, want Break/Idle", g.Category)
				}
			}
		}
	case "summarize-meeting/01": // "Thanks, Priya" cue + on-screen "Priya N."
		m := out.(*MeetingSummary)
		var priya []Participant
		for _, p := range m.Participants {
			if strings.HasPrefix(p.Name, "Priya") {
				priya = append(priya, p)
			}
		}
		if len(priya) != 1 || priya[0].Speaker != "Remote 1" {
			t.Errorf("want exactly one participant named Priya (Remote 1), got %+v", m.Participants)
		}
	case "summarize-meeting/02": // no name cues
		m := out.(*MeetingSummary)
		for _, p := range m.Participants {
			if p.Name != "" {
				t.Errorf("participant %q named %q without a cue", p.Speaker, p.Name)
			}
		}
	}
}
