package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// SegmentRecord is a categorised stretch of activity. Source is one of
// "rule", "skill", "user" or "auto".
type SegmentRecord struct {
	Day        string
	Hour       int
	Start, End time.Time
	App, Title string
	Kind       string
	Category   string
	Confidence float64
	Source     string
}

// ReplaceSegments atomically replaces all segments of the given day+hour.
func (s *Store) ReplaceSegments(day string, hour int, segs []SegmentRecord) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM segments WHERE day=? AND hour=?`, day, hour); err != nil {
			return err
		}
		for _, g := range segs {
			if _, err := tx.Exec(`INSERT INTO segments(day,hour,start,end,app,title,kind,category,confidence,source) VALUES(?,?,?,?,?,?,?,?,?,?)`,
				day, hour, fmtTime(g.Start), fmtTime(g.End), g.App, g.Title, g.Kind, g.Category, g.Confidence, g.Source); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) SegmentsOn(day string) ([]SegmentRecord, error) {
	rows, err := s.db.Query(`SELECT day,hour,start,end,app,title,kind,category,confidence,source FROM segments WHERE day=? ORDER BY start`, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SegmentRecord
	for rows.Next() {
		var g SegmentRecord
		var st, en string
		if err := rows.Scan(&g.Day, &g.Hour, &st, &en, &g.App, &g.Title, &g.Kind, &g.Category, &g.Confidence, &g.Source); err != nil {
			return nil, err
		}
		if g.Start, err = parseTime(st); err != nil {
			return nil, err
		}
		if g.End, err = parseTime(en); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SetSegmentCategory sets a segment's category and marks it as user-assigned.
func (s *Store) SetSegmentCategory(day string, hour int, start time.Time, category string) error {
	res, err := s.db.Exec(`UPDATE segments SET category=?, source='user' WHERE day=? AND hour=? AND start=?`,
		category, day, hour, fmtTime(start))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("store: no segment at %s hour %d start %s", day, hour, fmtTime(start))
	}
	return nil
}

// CategoryTotals sums segment durations per non-empty category over the
// inclusive day range.
func (s *Store) CategoryTotals(fromDay, toDay string) (map[string]time.Duration, error) {
	rows, err := s.db.Query(`SELECT category,start,end FROM segments WHERE day>=? AND day<=? AND category<>''`, fromDay, toDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Duration{}
	for rows.Next() {
		var cat, st, en string
		if err := rows.Scan(&cat, &st, &en); err != nil {
			return nil, err
		}
		a, err := parseTime(st)
		if err != nil {
			return nil, err
		}
		b, err := parseTime(en)
		if err != nil {
			return nil, err
		}
		out[cat] += b.Sub(a)
	}
	return out, rows.Err()
}
