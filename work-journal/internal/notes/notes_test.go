package notes

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"workjournal/internal/store"
)

const day = "2026-10-07"

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestValidate(t *testing.T) {
	for _, c := range []struct {
		d   Draft
		bad string
	}{
		{Draft{Kind: "context", Day: day, Text: "x"}, ""},
		{Draft{Kind: "Correction", Day: day, From: "13:10", To: "13:20", Text: "disregard"}, ""},
		{Draft{Kind: "context", Day: day, From: "23:00", To: "24:00", Text: "x"}, ""},
		{Draft{Kind: "memo", Day: day, Text: "x"}, "kind"},
		{Draft{Kind: "context", Day: "07/10/2026", Text: "x"}, "date"},
		{Draft{Kind: "context", Day: day, Text: "  "}, "empty"},
		{Draft{Kind: "context", Day: day, From: "13:10", Text: "x"}, "both"},
		{Draft{Kind: "context", Day: day, From: "13:20", To: "13:10", Text: "x"}, "after"},
		{Draft{Kind: "context", Day: day, From: "1:10", To: "13:20", Text: "x"}, "HH:MM"},
		{Draft{Kind: "context", Day: day, Text: strings.Repeat("a", MaxBytes+1)}, "limit"},
		{Draft{Kind: "context", Day: day, Text: "a\x00b"}, "binary"},
	} {
		_, err := Validate(c.d)
		if (err != nil) != (c.bad != "") || (err != nil && !strings.Contains(err.Error(), c.bad)) {
			t.Errorf("%+v: err %v, want %q", c.d, err, c.bad)
		}
	}
}

func TestReadFileRefusesBinary(t *testing.T) {
	dir := t.TempDir()
	txt, bin := filepath.Join(dir, "a.md"), filepath.Join(dir, "b.png")
	os.WriteFile(txt, []byte("# Plan\nship it"), 0o600)
	os.WriteFile(bin, []byte{0x89, 'P', 'N', 'G', 0, 1, 2}, 0o600)
	if s, err := ReadFile(txt); err != nil || s != "# Plan\nship it" {
		t.Fatalf("text: %q %v", s, err)
	}
	if _, err := ReadFile(bin); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("binary: %v", err)
	}
	// A read that fails (here a directory) is an error, not an empty note.
	if _, err := ReadFile(dir); err == nil {
		t.Fatal("read error ignored")
	}
	big := filepath.Join(dir, "big.txt")
	os.WriteFile(big, []byte(strings.Repeat("a", MaxBytes+10)), 0o600)
	if _, err := ReadFile(big); err == nil {
		t.Fatal("oversized file accepted")
	}
}

func TestHoursAndCovers(t *testing.T) {
	n := store.NoteRecord{Day: day, Start: "13:50", End: "15:00"}
	if got := Hours(n); !reflect.DeepEqual(got, []int{13, 14}) {
		t.Errorf("hours %v", got)
	}
	at := func(h, m int) time.Time { return time.Date(2026, 10, 7, h, m, 0, 0, time.Local) }
	if !Covers(n, at(14, 59), at(15, 30)) || Covers(n, at(15, 0), at(15, 30)) || Covers(n, at(12, 0), at(13, 50)) {
		t.Error("covers wrong at the range edges")
	}
	// Speech reaching a few seconds into 13:50 overlaps a note from 13:50.
	sec := func(h, m, s int) time.Time { return time.Date(2026, 10, 7, h, m, s, 0, time.Local) }
	if !Covers(n, sec(13, 49, 50), sec(13, 50, 10)) || Covers(n, sec(13, 49, 0), at(13, 50)) {
		t.Error("covers wrong within a minute")
	}
	whole := store.NoteRecord{Day: day}
	if !Covers(whole, at(3, 0), at(4, 0)) || Covers(whole, at(3, 0).AddDate(0, 0, 1), at(4, 0).AddDate(0, 0, 1)) {
		t.Error("whole-day note covers wrong")
	}
}

// A ranged note on part of a meeting also queues the hour the meeting
// ended in, where it is summarized; an imported meeting is not.
func TestQueueMeetingEndHour(t *testing.T) {
	st := openStore(t)
	at := func(h, m int) time.Time { return time.Date(2026, 10, 7, h, m, 0, 0, time.Local) }
	st.UpsertMeeting(store.MeetingRecord{ID: "m", Day: day, Start: at(9, 30), End: at(10, 30)})
	q, err := Queue(st, &store.NoteRecord{Kind: store.NoteCorrection, Day: day, Start: "09:40", End: "09:50"})
	if err != nil {
		t.Fatal(err)
	}
	want := []store.NoteQueueItem{{Day: day, Hour: 9, Redo: store.RedoPrivacy}, {Day: day, Hour: 10, Redo: store.RedoSummary}, {Day: day, Hour: -1, Redo: store.RedoSummary}}
	if !reflect.DeepEqual(q, want) {
		t.Fatalf("queue %+v\nwant %+v", q, want)
	}
	if q, _ := Queue(st, &store.NoteRecord{Kind: store.NoteContext, Day: day, Start: "11:00", End: "11:30"}); len(q) != 2 {
		t.Fatalf("note outside the meeting queued %+v", q)
	}
}

// Saving a correction queues its hours for privacy, a context note its
// hours for a summary, and both their day's journal; editing queues the old
// and new hours; the queue entry is done only if not queued again.
func TestAddUpdateDeleteQueue(t *testing.T) {
	st := openStore(t)
	now := time.Now()
	c, err := Add(st, Draft{Kind: "correction", Day: day, From: "13:10", To: "13:20", Text: "disregard, bystanders"}, store.SourceDashboard, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Add(st, Draft{Kind: "context", Day: day, From: "13:00", To: "14:30", Text: "Cloudflare planning call"}, store.SourceMCP, now); err != nil {
		t.Fatal(err)
	}
	q, _ := st.NoteQueue()
	want := []store.NoteQueueItem{{Day: day, Hour: 13, Redo: store.RedoPrivacy, Seq: 2}, {Day: day, Hour: 14, Redo: store.RedoSummary, Seq: 1}, {Day: day, Hour: -1, Redo: store.RedoSummary, Seq: 2}}
	if !reflect.DeepEqual(q, want) {
		t.Fatalf("queue %+v\nwant %+v", q, want)
	}
	for _, it := range q {
		st.DoneNoteQueue(it)
	}
	if _, err := Update(st, c.ID, Draft{Kind: "correction", From: "16:00", To: "16:05", Text: "moved"}, now); err != nil {
		t.Fatal(err)
	}
	q, _ = st.NoteQueue()
	var hours []int
	for _, it := range q {
		hours = append(hours, it.Hour)
	}
	if !reflect.DeepEqual(hours, []int{13, 16, -1}) {
		t.Fatalf("after update queue %+v", q)
	}
	// Queued again while running: stays.
	stale := q[1] // hour 16, which the delete queues again
	if _, err := Delete(st, c.ID); err != nil {
		t.Fatal(err)
	}
	st.DoneNoteQueue(stale)
	if q2, _ := st.NoteQueue(); len(q2) != 3 || q2[1].Hour != 16 {
		t.Fatalf("re-queued hour removed: %+v", q2)
	}
	if _, err := Delete(st, c.ID); err != ErrNotFound {
		t.Fatalf("delete twice: %v", err)
	}
	ns, _ := st.NotesOn(day)
	if len(ns) != 1 || ns[0].Source != store.SourceMCP {
		t.Fatalf("notes %+v", ns)
	}
}

// A context note is searchable, linked to its hour; a correction is not
// indexed, and editing or deleting a note updates the index. Hour
// reindexing (ReplaceText) leaves notes alone.
func TestNotesSearch(t *testing.T) {
	st := openStore(t)
	now := time.Now()
	n, err := Add(st, Draft{Kind: "context", Day: day, From: "13:00", To: "14:00", Text: "Cloudflare planning call"}, store.SourceCLI, now)
	if err != nil {
		t.Fatal(err)
	}
	Add(st, Draft{Kind: "correction", Day: day, From: "13:10", To: "13:20", Text: "bystanders talking about zebras"}, store.SourceCLI, now)
	if err := st.ReplaceText(day, 13, nil); err != nil {
		t.Fatal(err)
	}
	hits, _ := st.Search("cloudflare", 10)
	if len(hits) != 1 || hits[0].Kind != store.KindUserNote || hits[0].Hour != 13 || !strings.HasPrefix(hits[0].Ref, "hour/"+day+"/13#note-") {
		t.Fatalf("hits %+v", hits)
	}
	if hits, _ := st.Search("zebras", 10); len(hits) != 0 {
		t.Errorf("correction indexed: %+v", hits)
	}
	Update(st, n.ID, Draft{Kind: "context", Text: "Fastly call"}, now)
	if hits, _ := st.Search("cloudflare", 10); len(hits) != 0 {
		t.Errorf("old text still found: %+v", hits)
	}
	if hits, _ := st.Search("fastly", 10); len(hits) != 1 || hits[0].Ref != "day/"+day+"#note-1" {
		t.Errorf("edited whole-day note: %+v", hits)
	}
	Delete(st, n.ID)
	if hits, _ := st.Search("fastly", 10); len(hits) != 0 {
		t.Errorf("deleted note found: %+v", hits)
	}
}
