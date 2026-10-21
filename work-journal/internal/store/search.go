package store

import (
	"database/sql"
	"strings"
	"time"
)

// TextDoc is one full-text-indexed document. Kind is one of
// transcript|ocr|note|meeting.
type TextDoc struct {
	Kind string
	Day  string
	Hour int
	TS   time.Time
	Text string
	Ref  string
}

type SearchHit struct {
	TextDoc
	Snippet string
}

// notImported leaves out the docs of imported recordings, which are indexed
// per import (ReplaceImportText), not per hour.
const notImported = ` AND ref NOT IN (SELECT 'meeting/'||id FROM imports)`

// ReplaceText atomically replaces all indexed docs for the given day+hour,
// except those of imported recordings.
func (s *Store) ReplaceText(day string, hour int, docs []TextDoc) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM docs_fts WHERE day=? AND hour=?`+notImported, day, hour); err != nil {
			return err
		}
		for _, d := range docs {
			if _, err := tx.Exec(`INSERT INTO docs_fts(text,kind,day,hour,ts,ref) VALUES(?,?,?,?,?,?)`,
				d.Text, d.Kind, day, hour, fmtTime(d.TS), d.Ref); err != nil {
				return err
			}
		}
		return nil
	})
}

// ReplaceTextKinds replaces the hour's indexed docs of the given kinds with
// docs (which must be of those kinds), leaving other kinds and imported
// recordings alone.
func (s *Store) ReplaceTextKinds(day string, hour int, kinds []string, docs []TextDoc) error {
	return s.tx(func(tx *sql.Tx) error {
		for _, k := range kinds {
			if _, err := tx.Exec(`DELETE FROM docs_fts WHERE day=? AND hour=? AND kind=?`+notImported, day, hour, k); err != nil {
				return err
			}
		}
		for _, d := range docs {
			if _, err := tx.Exec(`INSERT INTO docs_fts(text,kind,day,hour,ts,ref) VALUES(?,?,?,?,?,?)`,
				d.Text, d.Kind, day, hour, fmtTime(d.TS), d.Ref); err != nil {
				return err
			}
		}
		return nil
	})
}

// ReplaceImportText atomically replaces the indexed docs of imported
// recording id (those whose Ref is meeting/<id>) with docs.
func (s *Store) ReplaceImportText(id string, docs []TextDoc) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM docs_fts WHERE ref=?`, "meeting/"+id); err != nil {
			return err
		}
		for _, d := range docs {
			if _, err := tx.Exec(`INSERT INTO docs_fts(text,kind,day,hour,ts,ref) VALUES(?,?,?,?,?,?)`,
				d.Text, d.Kind, d.Day, d.Hour, fmtTime(d.TS), d.Ref); err != nil {
				return err
			}
		}
		return nil
	})
}

// quoteQuery turns free user input into an FTS5 query of quoted terms
// (implicit AND), so operators and stray quotes are treated as literals.
func quoteQuery(q string) string {
	terms := strings.Fields(q)
	for i, t := range terms {
		terms[i] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
	}
	return strings.Join(terms, " ")
}

// Search runs a ranked full-text query. Empty queries return no hits.
func (s *Store) Search(q string, limit int) ([]SearchHit, error) {
	return s.SearchRange(q, "", "", limit, 0)
}

// SearchRange is Search limited to the inclusive day range (an empty bound
// is open), skipping the first offset hits.
func (s *Store) SearchRange(q, fromDay, toDay string, limit, offset int) ([]SearchHit, error) {
	match := quoteQuery(q)
	if match == "" {
		return nil, nil
	}
	query := `SELECT kind,day,hour,ts,ref,text,snippet(docs_fts,0,'[',']','…',12)
		FROM docs_fts WHERE docs_fts MATCH ?`
	args := []any{match}
	if fromDay != "" {
		query += ` AND day>=?`
		args = append(args, fromDay)
	}
	if toDay != "" {
		query += ` AND day<=?`
		args = append(args, toDay)
	}
	query += ` ORDER BY bm25(docs_fts) LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchHit
	for rows.Next() {
		var h SearchHit
		var ts string
		if err := rows.Scan(&h.Kind, &h.Day, &h.Hour, &ts, &h.Ref, &h.Text, &h.Snippet); err != nil {
			return nil, err
		}
		if h.TS, err = parseTime(ts); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
