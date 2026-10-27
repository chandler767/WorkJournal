package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"workjournal/internal/store"
)

// add_note adds a note (source mcp, queued for reprocessing) through the
// read-only journal, and list_notes shows it; add_note is the only tool not
// marked read-only and it is never destructive.
func TestAddAndListNotesOverMCP(t *testing.T) {
	j := newFixture(t)
	out, err := j.AddNote(AddNoteArgs{Date: fxDay, Kind: "correction", From: "09:10", To: "09:20", Text: "people next to me; disregard it"})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "Saved correction note 1 on "+fxDay+" (09:10–09:20)", "/hour/"+fxDay+"/09#note-1")
	if _, err := j.AddNote(AddNoteArgs{Date: fxDay, Kind: "memo", Text: "x"}); err == nil || !strings.Contains(err.Error(), "kind") {
		t.Errorf("bad kind: %v", err)
	}
	out, err = j.ListNotes(ListNotesArgs{Date: fxDay})
	if err != nil {
		t.Fatal(err)
	}
	contains(t, out, "1 · correction · 09:10–09:20 (from mcp)", "people next to me; disregard it")
	if q, _ := j.writer.NoteQueue(); len(q) != 2 || q[0].Redo != store.RedoPrivacy {
		t.Errorf("queue %+v", q)
	}

	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := j.Server().Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		ro := tl.Annotations != nil && tl.Annotations.ReadOnlyHint
		if (tl.Name == "add_note") == ro {
			t.Errorf("%s: read-only %v", tl.Name, ro)
		}
		if tl.Name == "add_note" {
			if d := tl.Annotations.DestructiveHint; d == nil || *d {
				t.Errorf("add_note destructive hint %v", d)
			}
			contains(t, tl.Description, "can only add a note", "never edits or deletes")
		}
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "add_note", Arguments: map[string]any{"date": fxDay, "kind": "context", "text": "Cloudflare planning call"}})
	if err != nil || res.IsError {
		t.Fatalf("add_note: %v %+v", err, res)
	}
	out, _ = j.Search(SearchArgs{Query: "cloudflare"})
	contains(t, out, "[user_note]", "/day/"+fxDay+"#note-2")
}
