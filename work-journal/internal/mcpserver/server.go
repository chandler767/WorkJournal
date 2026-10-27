// Package mcpserver is the journal MCP server (`journal mcp`): it lets any
// Claude client search and read what the dashboard shows — hour summaries,
// privacy-filtered transcripts, meetings, time totals, the daily and weekly
// journals and the user's notes — and nothing else (no raw media, cache,
// voice profiles or privacy decisions). Its one write is add_note: it adds
// a note and can never edit or delete anything.
package mcpserver

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"workjournal/internal/config"
	"workjournal/internal/store"
)

// ServerName is the MCP server's name; Claude names its tools
// mcp__<ServerName>__<tool>.
const ServerName = "work-journal"

// ToolNames lists the read-only tools in registration order.
var ToolNames = []string{"search", "get_day", "get_hour", "list_meetings", "get_meeting", "time_report", "get_week", "list_notes"}

// WriteToolNames lists the tools that write: add_note only adds a note.
var WriteToolNames = []string{"add_note"}

// Journal answers the tools from a read-only store and the derived files.
type Journal struct {
	Store *store.Store
	Paths config.Paths
	Cfg   config.Config
	Now   func() time.Time // nil: time.Now

	writer *store.Store // opened by the first add_note
}

// Open opens cfg's journal database read-only.
func Open(cfg config.Config) (*Journal, error) {
	p := config.Paths{Root: cfg.DataDir}
	st, err := store.OpenReadOnly(p.DB())
	if err != nil {
		return nil, fmt.Errorf("open journal (has `journal serve` run yet?): %w", err)
	}
	return &Journal{Store: st, Paths: p, Cfg: cfg}, nil
}

// Close closes the store.
func (j *Journal) Close() error {
	if j.writer != nil {
		j.writer.Close()
	}
	return j.Store.Close()
}

// notesWriter opens the journal database for writing, for add_note only.
func (j *Journal) notesWriter() (*store.Store, error) {
	if j.writer == nil {
		st, err := store.Open(j.Paths.DB())
		if err != nil {
			return nil, err
		}
		j.writer = st
	}
	return j.writer, nil
}

func (j *Journal) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

// Run serves the tools over stdin/stdout until the client disconnects or
// ctx ends.
func (j *Journal) Run(ctx context.Context) error {
	return j.Server().Run(ctx, &mcp.StdioTransport{})
}

// Server builds the MCP server with every tool registered.
func (j *Journal) Server() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: ServerName, Title: "Work journal", Version: "1.0.0"}, &mcp.ServerOptions{
		Instructions: "Access to the user's work journal: hourly summaries, privacy-filtered transcripts, meetings, " +
			"time by category, the daily and weekly journal entries and the user's notes. Everything is read-only except add_note, " +
			"which only adds a note when the user asks you to tell the journal something. " +
			"Dates are YYYY-MM-DD (or today/yesterday), weeks YYYY-Www. Results include dashboard links; cite them.",
	})
	add(s, "search", "Search", "Full-text search over transcripts, screen text, hour notes and meeting summaries. Returns ranked snippets with kind, day, hour, time and a dashboard link.", j.Search)
	add(s, "get_day", "Get day", "One day: the daily journal entry (if written), each hour's status, narrative, notes and action items, minutes per category and the day's meetings.", j.GetDay)
	add(s, "get_hour", "Get hour", "One hour: status, narrative, timeline, notes, action items, activity segments and meetings; with include_transcript=true also the transcript lines (paged by transcript_offset).", j.GetHour)
	add(s, "list_meetings", "List meetings", "Meetings between two days (at most 92 days), optionally only those with a person (matched on participant names): id, time, app, title, participants and a one-line summary.", j.ListMeetings)
	add(s, "get_meeting", "Get meeting", "One meeting by id: summary, decisions, action items and participants; with include_transcript=true also its transcript (paged by offset).", j.GetMeeting)
	add(s, "time_report", "Time report", "Minutes of categorized activity between two days (at most 366), grouped by category, day or app.", j.TimeReport)
	add(s, "get_week", "Get week", "One ISO week (YYYY-Www, or today/yesterday for the week holding that day): the weekly rollup (if written) and minutes per category for each day.", j.GetWeek)
	add(s, "list_notes", "List notes", "The notes the user wrote on one day: id, kind (context or correction), time range and text.", j.ListNotes)
	mcp.AddTool(s, &mcp.Tool{Name: "add_note", Title: "Add note",
		Description: "The only tool that writes, and it can only add a note: it never edits or deletes anything. Use it when the user asks you to tell their journal something. " +
			"kind context: free text or a pasted document (what a call was about, who was there), passed to the summaries and the daily entry and searchable; it is never treated as a recording. " +
			"kind correction: an instruction about what was recorded, such as \"disregard speech 13:10-13:20, it was bystanders\" or \"Speaker 2 at 15:00 is Maya\"; the covered hours are reprocessed. " +
			"from/to (HH:MM, on the clock where it was recorded) limit it to a time range; omit both for the whole day. At most 100 KB of text.",
		Annotations: &mcp.ToolAnnotations{Title: "Add note", ReadOnlyHint: false, DestructiveHint: new(bool), IdempotentHint: false, OpenWorldHint: new(bool)}},
		func(_ context.Context, _ *mcp.CallToolRequest, in AddNoteArgs) (*mcp.CallToolResult, any, error) {
			text, err := j.AddNote(in)
			if err != nil {
				return nil, nil, err
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
		})
	return s
}

// add registers a read-only tool whose handler returns text; a handler
// error becomes a tool error the model can read and correct.
func add[In any](s *mcp.Server, name, title, desc string, h func(In) (string, error)) {
	mcp.AddTool(s, &mcp.Tool{Name: name, Title: title, Description: desc,
		Annotations: &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: new(bool)}},
		func(_ context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
			text, err := h(in)
			if err != nil {
				return nil, nil, err
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
		})
}
