package store

import (
	"strings"
	"time"
)

// ImportRecord is the processing state of an imported recording; ID is
// also the id of its meeting.
type ImportRecord struct {
	ID        string
	Status    string // StatusPending, StatusDone or StatusNeedsReview
	Error     string // the last processing failure; "" when none
	UpdatedAt time.Time
}

func (s *Store) UpsertImport(r ImportRecord) error {
	_, err := s.db.Exec(`INSERT INTO imports(id,status,error,updated_at) VALUES(?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET status=excluded.status, error=excluded.error, updated_at=excluded.updated_at`,
		r.ID, r.Status, r.Error, fmtTime(r.UpdatedAt))
	return err
}

func scanImport(r scanner) (ImportRecord, error) {
	var i ImportRecord
	var upd string
	if err := r.Scan(&i.ID, &i.Status, &i.Error, &upd); err != nil {
		return i, err
	}
	var err error
	i.UpdatedAt, err = parseTime(upd)
	return i, err
}

func (s *Store) Import(id string) (ImportRecord, bool, error) {
	i, err := scanImport(s.db.QueryRow(`SELECT id,status,error,updated_at FROM imports WHERE id=?`, id))
	if isNoRows(err) {
		return ImportRecord{}, false, nil
	}
	if err != nil {
		return ImportRecord{}, false, err
	}
	return i, true, nil
}

// ImportsByStatus returns the imports in any of the given statuses, by id
// (so oldest recording first within a day).
func (s *Store) ImportsByStatus(statuses ...string) ([]ImportRecord, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	args := make([]any, len(statuses))
	for i, st := range statuses {
		args[i] = st
	}
	rows, err := s.db.Query(`SELECT id,status,error,updated_at FROM imports WHERE status IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(statuses)), ",")+`) ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImportRecord
	for rows.Next() {
		i, err := scanImport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}
