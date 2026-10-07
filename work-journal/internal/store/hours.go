package store

import (
	"strings"
	"time"
)

// HourRecord is the processing state and summary of one hour.
type HourRecord struct {
	Day         string
	Hour        int
	Status      string
	SummaryJSON string
	UpdatedAt   time.Time
}

func (s *Store) UpsertHour(h HourRecord) error {
	_, err := s.db.Exec(`INSERT INTO hours(day,hour,status,summary_json,updated_at) VALUES(?,?,?,?,?)
		ON CONFLICT(day,hour) DO UPDATE SET status=excluded.status, summary_json=excluded.summary_json, updated_at=excluded.updated_at`,
		h.Day, h.Hour, h.Status, h.SummaryJSON, fmtTime(h.UpdatedAt))
	return err
}

type scanner interface{ Scan(...any) error }

func scanHour(r scanner) (HourRecord, error) {
	var h HourRecord
	var upd string
	if err := r.Scan(&h.Day, &h.Hour, &h.Status, &h.SummaryJSON, &upd); err != nil {
		return h, err
	}
	var err error
	h.UpdatedAt, err = parseTime(upd)
	return h, err
}

const hourCols = `day,hour,status,summary_json,updated_at`

func (s *Store) Hour(day string, hour int) (HourRecord, bool, error) {
	h, err := scanHour(s.db.QueryRow(`SELECT `+hourCols+` FROM hours WHERE day=? AND hour=?`, day, hour))
	if isNoRows(err) {
		return HourRecord{}, false, nil
	}
	if err != nil {
		return HourRecord{}, false, err
	}
	return h, true, nil
}

func (s *Store) queryHours(q string, args ...any) ([]HourRecord, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HourRecord
	for rows.Next() {
		h, err := scanHour(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// HoursByStatus returns hours in any of the given statuses, oldest first.
func (s *Store) HoursByStatus(statuses ...string) ([]HourRecord, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	args := make([]any, len(statuses))
	for i, st := range statuses {
		args[i] = st
	}
	return s.queryHours(`SELECT `+hourCols+` FROM hours WHERE status IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(statuses)), ",")+`) ORDER BY day,hour`, args...)
}

func (s *Store) HoursOn(day string) ([]HourRecord, error) {
	return s.queryHours(`SELECT `+hourCols+` FROM hours WHERE day=? ORDER BY hour`, day)
}
