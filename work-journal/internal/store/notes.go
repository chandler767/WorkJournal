package store

import (
	"database/sql"
	"fmt"
	"strconv"
	"time"
)

// Note kinds and sources.
const (
	NoteContext    = "context"
	NoteCorrection = "correction"

	SourceDashboard = "dashboard"
	SourceMCP       = "mcp"
	SourceCLI       = "cli"
	SourceImport    = "import"
)

// What a queued hour needs again (NoteQueueItem.Redo).
const (
	RedoSummary = "summary" // summarize it again from its derived doc
	RedoPrivacy = "privacy" // clear its privacy decisions and process it again
)

// NoteRecord is a note the user wrote: Start and End ("" for the whole day)
// are HH:MM on the day's recorded clock, End exclusive.
type NoteRecord struct {
	ID                   int64
	Kind, Day            string
	Start, End           string
	Text, Source         string
	CreatedAt, UpdatedAt time.Time
}

// NoteQueueItem is an hour to process again after a note changed; Hour -1
// is the day's journal entry (and its week's rollup).
type NoteQueueItem struct {
	Day  string
	Hour int
	Redo string
	Seq  int64 // as read; queueing the hour again changes it
}

const noteCols = `id,kind,day,start,end,text,source,created_at,updated_at`

func scanNote(r scanner) (NoteRecord, error) {
	var n NoteRecord
	var cr, up string
	if err := r.Scan(&n.ID, &n.Kind, &n.Day, &n.Start, &n.End, &n.Text, &n.Source, &cr, &up); err != nil {
		return n, err
	}
	var err error
	if n.CreatedAt, err = parseTime(cr); err != nil {
		return n, err
	}
	n.UpdatedAt, err = parseTime(up)
	return n, err
}

// AddNote stores a new note (its ID is ignored) with the work it queues,
// and returns its id.
func (s *Store) AddNote(n NoteRecord, queue []NoteQueueItem) (int64, error) {
	var id int64
	err := s.tx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO notes(kind,day,start,end,text,source,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`,
			n.Kind, n.Day, n.Start, n.End, n.Text, n.Source, fmtTime(n.CreatedAt), fmtTime(n.UpdatedAt))
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		n.ID = id
		if err := indexNote(tx, n); err != nil {
			return err
		}
		return enqueue(tx, queue)
	})
	return id, err
}

// UpdateNote replaces a note's kind, range and text (keeping its day,
// source and creation time) with the work it queues.
func (s *Store) UpdateNote(n NoteRecord, queue []NoteQueueItem) error {
	return s.tx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE notes SET kind=?,start=?,end=?,text=?,updated_at=? WHERE id=?`,
			n.Kind, n.Start, n.End, n.Text, fmtTime(n.UpdatedAt), n.ID)
		if err != nil {
			return err
		}
		if c, err := res.RowsAffected(); err != nil {
			return err
		} else if c == 0 {
			return sql.ErrNoRows
		}
		if err := unindexNote(tx, n.ID); err != nil {
			return err
		}
		if err := indexNote(tx, n); err != nil {
			return err
		}
		return enqueue(tx, queue)
	})
}

// DeleteNote removes a note with the work it queues.
func (s *Store) DeleteNote(id int64, queue []NoteQueueItem) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM notes WHERE id=?`, id); err != nil {
			return err
		}
		if err := unindexNote(tx, id); err != nil {
			return err
		}
		return enqueue(tx, queue)
	})
}

// KindUserNote is the search index kind of a context note. ReplaceText
// leaves these alone: they are indexed per note, not per hour.
const KindUserNote = "user_note"

// noteRef is the indexed Ref of a note: its hour page (the hour it starts
// in) or its day page, with the note's anchor.
func noteRef(n NoteRecord) string {
	if n.Start == "" {
		return fmt.Sprintf("day/%s#note-%d", n.Day, n.ID)
	}
	return fmt.Sprintf("hour/%s/%s#note-%d", n.Day, n.Start[:2], n.ID)
}

// indexNote indexes a context note for search; corrections are
// instructions about recordings, not content, and are not indexed.
func indexNote(tx *sql.Tx, n NoteRecord) error {
	if n.Kind != NoteContext {
		return nil
	}
	hour, ts := -1, n.UpdatedAt
	if n.Start != "" {
		hour, _ = strconv.Atoi(n.Start[:2])
	}
	_, err := tx.Exec(`INSERT INTO docs_fts(text,kind,day,hour,ts,ref) VALUES(?,?,?,?,?,?)`,
		n.Text, KindUserNote, n.Day, hour, fmtTime(ts), noteRef(n))
	return err
}

func unindexNote(tx *sql.Tx, id int64) error {
	_, err := tx.Exec(`DELETE FROM docs_fts WHERE kind=? AND ref LIKE ?`, KindUserNote, fmt.Sprintf("%%#note-%d", id))
	return err
}

// enqueue adds the queue items; redoing privacy covers redoing the summary.
func enqueue(tx *sql.Tx, queue []NoteQueueItem) error {
	for _, q := range queue {
		if _, err := tx.Exec(`INSERT INTO note_queue(day,hour,redo,seq) VALUES(?,?,?,1)
			ON CONFLICT(day,hour) DO UPDATE SET seq=note_queue.seq+1,
				redo=CASE WHEN note_queue.redo=? THEN note_queue.redo ELSE excluded.redo END`,
			q.Day, q.Hour, q.Redo, RedoPrivacy); err != nil {
			return err
		}
	}
	return nil
}

// Note returns the note with id.
func (s *Store) Note(id int64) (NoteRecord, bool, error) {
	n, err := scanNote(s.db.QueryRow(`SELECT `+noteCols+` FROM notes WHERE id=?`, id))
	if isNoRows(err) {
		return NoteRecord{}, false, nil
	}
	if err != nil {
		return NoteRecord{}, false, err
	}
	return n, true, nil
}

// NotesOn returns day's notes: whole-day notes first, then by start, then
// oldest first.
func (s *Store) NotesOn(day string) ([]NoteRecord, error) {
	rows, err := s.db.Query(`SELECT `+noteCols+` FROM notes WHERE day=? ORDER BY start, id`, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NoteRecord
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// NoteQueue returns the queued work, oldest day first, a day's hours
// before its journal (hour -1).
func (s *Store) NoteQueue() ([]NoteQueueItem, error) {
	rows, err := s.db.Query(`SELECT day,hour,redo,seq FROM note_queue ORDER BY day, hour<0, hour`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NoteQueueItem
	for rows.Next() {
		var q NoteQueueItem
		if err := rows.Scan(&q.Day, &q.Hour, &q.Redo, &q.Seq); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// DoneNoteQueue removes q from the queue, unless its hour was queued again
// since q was read.
func (s *Store) DoneNoteQueue(q NoteQueueItem) error {
	_, err := s.db.Exec(`DELETE FROM note_queue WHERE day=? AND hour=? AND seq=?`, q.Day, q.Hour, q.Seq)
	return err
}
