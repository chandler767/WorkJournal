package mcpserver

import (
	"fmt"
	"strings"

	"workjournal/internal/notes"
	"workjournal/internal/store"
)

type (
	AddNoteArgs struct {
		Date string `json:"date" jsonschema:"the day: YYYY-MM-DD, today or yesterday"`
		Kind string `json:"kind" jsonschema:"context (information for the summaries) or correction (an instruction about recorded content)"`
		Text string `json:"text" jsonschema:"the note, up to 100 KB of text"`
		From string `json:"from,omitempty" jsonschema:"start of the time range, HH:MM; omit with to for the whole day"`
		To   string `json:"to,omitempty" jsonschema:"end of the time range, HH:MM (exclusive)"`
	}
	ListNotesArgs struct {
		Date   string `json:"date" jsonschema:"the day: YYYY-MM-DD, today or yesterday"`
		Offset int    `json:"offset,omitempty" jsonschema:"notes to skip, when an earlier call was truncated"`
	}
)

// AddNote adds a note from MCP; it never changes an existing one.
func (j *Journal) AddNote(a AddNoteArgs) (string, error) {
	day, err := j.day("date", a.Date)
	if err != nil {
		return "", err
	}
	st, err := j.notesWriter()
	if err != nil {
		return "", fmt.Errorf("open journal for the note: %w", err)
	}
	n, err := notes.Add(st, notes.Draft{Kind: a.Kind, Day: day, From: a.From, To: a.To, Text: a.Text}, store.SourceMCP, j.now())
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Saved %s note %d on %s (%s). Dashboard: %s\n", n.Kind, n.ID, n.Day, notes.Range(n), j.link(notePath(n)))
	if n.Kind == store.NoteCorrection {
		if notes.RawGone(j.Paths, day) {
			b.WriteString("The day's raw recordings were already removed, so this correction can change the summaries but not the transcript.\n")
		} else {
			b.WriteString("The hours it covers are reprocessed by journal serve shortly.\n")
		}
	}
	return b.String(), nil
}

// ListNotes lists a day's notes.
func (j *Journal) ListNotes(a ListNotesArgs) (string, error) {
	day, err := j.day("date", a.Date)
	if err != nil {
		return "", err
	}
	ns, err := j.Store.NotesOn(day)
	if err != nil {
		return "", err
	}
	if len(ns) == 0 {
		return "No notes on " + day + ".", nil
	}
	var items []string
	for _, n := range ns {
		items = append(items, fmt.Sprintf("\n## %d · %s · %s (from %s) — %s\n%s", n.ID, n.Kind, notes.Range(n), n.Source, j.link(notePath(n)), n.Text))
	}
	return paged(fmt.Sprintf("# Notes on %s\n", day), items, max(a.Offset, 0), "offset", "notes"), nil
}

func notePath(n store.NoteRecord) string {
	if n.Start == "" {
		return fmt.Sprintf("/day/%s#note-%d", n.Day, n.ID)
	}
	return fmt.Sprintf("/hour/%s/%s#note-%d", n.Day, n.Start[:2], n.ID)
}
