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
	case "privacy-filter":
		return &PrivacyResult{}
	default:
		return &Markdown{}
	}
}

func check(t *testing.T, name string, out any) {
	t.Helper()
	switch name {
	case "summarize-hour/03": // empty hour
		h := out.(*HourSummary)
		if len(h.Notes) != 0 || h.MainFocus != "" || len(h.ActionItems) != 0 || h.Narrative != "" || len(h.Timeline) != 0 {
			t.Errorf("empty hour gave %+v", *h)
		}
	case "summarize-hour/01": // hour 10 in the fixture
		checkDetailedHour(t, out.(*HourSummary), 10, []string{"parser.go", "ParseInvoice", "acme-billing", "encoding/json", "pkg.go.dev"})
	case "summarize-hour/02": // hour 14 in the fixture
		checkDetailedHour(t, out.(*HourSummary), 14, []string{"Q2 roadmap", "estimate", "Thursday", "Notion"})
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
	case "summarize-meeting/04": // imported recording with the user's note
		m := out.(*MeetingSummary)
		if !strings.Contains(strings.ToLower(m.Summary), "cluster") {
			t.Errorf("summary misses the note's focus (speaker clustering): %q", m.Summary)
		}
		// The note names Maya, but no transcript cue ties her to a speaker.
		for _, p := range m.Participants {
			if strings.Contains(p.Name, "Maya") {
				t.Errorf("named a speaker from the note alone: %+v", p)
			}
		}
	case "privacy-filter/01": // bystanders' weekend chat while the user is silent
		checkDrops(t, out.(*PrivacyResult), []int{1, 2, 3, 4, 5, 6}, "bystander")
	case "privacy-filter/02": // coworkers' personal chat in a meeting
		checkDrops(t, out.(*PrivacyResult), nil, "")
	case "privacy-filter/03": // the user's call with their mother; line 0 is context
		checkDrops(t, out.(*PrivacyResult), []int{1, 2, 3, 4}, "personal_non_work")
	case "privacy-filter/04": // work chat with an account and routing number
		p := out.(*PrivacyResult)
		checkDrops(t, p, nil, "")
		var got string
		for _, r := range p.Redact {
			if r.Index == 1 {
				got = r.Text
			} else {
				t.Errorf("redacted line %d: %q", r.Index, r.Text)
			}
		}
		if !strings.Contains(got, "[redacted]") || strings.Contains(got, "4417") || strings.Contains(got, "021000021") {
			t.Errorf("line 1 redacted as %q", got)
		}
	case "privacy-filter/06": // a FaceTime call with Mom about dinner: both tracks; line 0 is context
		checkDrops(t, out.(*PrivacyResult), []int{1, 2, 3, 4, 5, 6, 7}, "personal_non_work")
	case "privacy-filter/07": // coworkers' work talk the user's correction says to disregard
		checkDrops(t, out.(*PrivacyResult), []int{1, 2, 3, 4}, "user_note")
	case "summarize-hour/04": // context note says what the call was
		h := out.(*HourSummary)
		all := strings.ToLower(h.Narrative + " " + strings.Join(h.Notes, " ") + " " + h.MainFocus)
		if !strings.Contains(all, "cloudflare") {
			t.Errorf("summary misses the note's context (Cloudflare): %+v", *h)
		}
		checkDetailedHour(t, h, 14, []string{"Cloudflare", "Europe", "region"})
	case "summarize-meeting/05": // a context note alone names no speaker
		m := out.(*MeetingSummary)
		if !strings.Contains(strings.ToLower(m.Summary), "cloudflare") {
			t.Errorf("summary misses the note's context (Cloudflare): %q", m.Summary)
		}
		for _, p := range m.Participants {
			if p.Name != "" && p.Speaker != "You" {
				t.Errorf("named a speaker from a context note alone: %+v", p)
			}
		}
	case "summarize-meeting/06": // a correction names Speaker 2
		m := out.(*MeetingSummary)
		for _, p := range m.Participants {
			switch {
			case p.Speaker == "Speaker 2" && !strings.HasPrefix(p.Name, "Maya"):
				t.Errorf("Speaker 2 named %q, want Maya from the correction", p.Name)
			case p.Speaker == "Speaker 1" && p.Name != "":
				t.Errorf("Speaker 1 named %q without a cue", p.Name)
			}
		}
	case "close-day/04": // context notes on the day and on an hour
		md := out.(*Markdown).Markdown
		if !strings.Contains(strings.ToLower(md), "launch review") {
			t.Errorf("entry misses the whole-day note (launch review): %s", md)
		}
		if strings.Count(md, "## ") != 6 {
			t.Errorf("want the six headings: %s", md)
		}
	case "privacy-filter/05": // ambiguous: kept
		checkDrops(t, out.(*PrivacyResult), nil, "")
	case "summarize-meeting/02": // no name cues
		m := out.(*MeetingSummary)
		for _, p := range m.Participants {
			if p.Name != "" {
				t.Errorf("participant %q named %q without a cue", p.Speaker, p.Name)
			}
		}
	}
}

// checkDrops verifies the privacy-filter output drops exactly want (with
// at least the app's default confidence 0.7 and the given reason).
func checkDrops(t *testing.T, p *PrivacyResult, want []int, reason string) {
	t.Helper()
	var got []int
	for _, d := range p.Drop {
		if d.Confidence < 0.7 {
			continue
		}
		if d.Reason != reason {
			t.Errorf("drop %+v: reason %q, want %q", d, d.Reason, reason)
		}
		for i := d.From; i <= d.To; i++ {
			got = append(got, i)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("dropped %v, want %v (output %+v)", got, want, *p)
	}
}

// checkDetailedHour verifies the richer hour output: a narrative, at most 12
// notes, a timeline of valid HH:MM entries inside the hour, and at least one
// note naming a specific identifier from the fixture.
func checkDetailedHour(t *testing.T, h *HourSummary, hour int, identifiers []string) {
	t.Helper()
	if strings.TrimSpace(h.Narrative) == "" {
		t.Error("empty narrative")
	}
	if len(h.Notes) == 0 || len(h.Notes) > 12 {
		t.Errorf("%d notes, want 1-12", len(h.Notes))
	}
	if len(h.Timeline) == 0 {
		t.Error("empty timeline")
	}
	for _, e := range h.Timeline {
		s, serr := time.Parse("15:04", e.Start)
		en, eerr := time.Parse("15:04", e.End)
		if serr != nil || eerr != nil {
			t.Errorf("timeline entry %+v: bad HH:MM", e)
			continue
		}
		lo, hi := hour*60, (hour+1)*60
		sm, em := s.Hour()*60+s.Minute(), en.Hour()*60+en.Minute()
		if sm < lo || sm >= hi || em < sm || em > hi {
			t.Errorf("timeline entry %+v outside hour %02d", e, hour)
		}
		if e.App == "" || e.Activity == "" {
			t.Errorf("timeline entry %+v has an empty app or activity", e)
		}
	}
	joined := strings.ToLower(strings.Join(h.Notes, "\n"))
	if !slices.ContainsFunc(identifiers, func(id string) bool { return strings.Contains(joined, strings.ToLower(id)) }) {
		t.Errorf("no note mentions any of %v: %q", identifiers, h.Notes)
	}
}
