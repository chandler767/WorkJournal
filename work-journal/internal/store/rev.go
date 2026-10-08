package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
)

// DayRev is an opaque fingerprint of everything the day page shows from the
// database: hours (status, summary, updated_at), segments (category and
// source, so user overrides count), meetings and their participants. It
// changes whenever any of those change and is stable otherwise.
func (s *Store) DayRev(day string) (string, error) {
	h := sha256.New()
	for _, q := range []struct{ name, sql string }{
		{"hours", `SELECT hour,status,updated_at,length(summary_json),summary_json FROM hours WHERE day=? ORDER BY hour`},
		{"segments", `SELECT hour,start,end,app,category,source FROM segments WHERE day=? ORDER BY hour,start`},
		{"meetings", `SELECT id,start,end,app,title,summary_json FROM meetings WHERE day=? ORDER BY id`},
		{"participants", `SELECT p.meeting_id,p.speaker,p.name,p.confidence,p.confirmed FROM participants p JOIN meetings m ON m.id=p.meeting_id WHERE m.day=? ORDER BY p.meeting_id,p.speaker`},
	} {
		if err := s.hashRows(h, q.name, q.sql, day); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// HourRev fingerprints what the hour page shows from the database: the
// hour record and that hour's segments.
func (s *Store) HourRev(day string, hour int) (string, error) {
	h := sha256.New()
	if err := s.hashRows(h, "hour", `SELECT status,updated_at,length(summary_json),summary_json FROM hours WHERE day=? AND hour=?`, day, hour); err != nil {
		return "", err
	}
	if err := s.hashRows(h, "segments", `SELECT start,end,app,title,category,source FROM segments WHERE day=? AND hour=? ORDER BY start`, day, hour); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// hashRows feeds every column of every row of the query into h.
func (s *Store) hashRows(h hash.Hash, name, query string, args ...any) error {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	fmt.Fprintf(h, "[%s]", name)
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		for _, v := range vals {
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			fmt.Fprintf(h, "%v\x1f", v)
		}
		fmt.Fprint(h, "\x1e")
	}
	return rows.Err()
}
