package store

import "time"

type MeetingRecord struct {
	ID, Day     string
	Start, End  time.Time
	App, Title  string
	SummaryJSON string
	// Imported is set for a meeting made from an imported recording (see
	// ImportRecord); ImportStatus and ImportError are its processing state.
	// UpsertMeeting ignores all three.
	Imported                  bool
	ImportStatus, ImportError string
}

func (s *Store) UpsertMeeting(m MeetingRecord) error {
	_, err := s.db.Exec(`INSERT INTO meetings(id,day,start,end,app,title,summary_json) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET day=excluded.day,start=excluded.start,end=excluded.end,app=excluded.app,title=excluded.title,summary_json=excluded.summary_json`,
		m.ID, m.Day, fmtTime(m.Start), fmtTime(m.End), m.App, m.Title, m.SummaryJSON)
	return err
}

func scanMeeting(r scanner) (MeetingRecord, error) {
	var m MeetingRecord
	var st, en string
	if err := r.Scan(&m.ID, &m.Day, &st, &en, &m.App, &m.Title, &m.SummaryJSON, &m.ImportStatus, &m.ImportError); err != nil {
		return m, err
	}
	m.Imported = m.ImportStatus != ""
	var err error
	if m.Start, err = parseTime(st); err != nil {
		return m, err
	}
	m.End, err = parseTime(en)
	return m, err
}

// meetingFrom selects meetingCols: every meeting, with its import's state.
const (
	meetingCols = `m.id,m.day,m.start,m.end,m.app,m.title,m.summary_json,coalesce(i.status,''),coalesce(i.error,'')`
	meetingFrom = ` FROM meetings m LEFT JOIN imports i ON i.id=m.id`
)

func (s *Store) Meeting(id string) (MeetingRecord, bool, error) {
	m, err := scanMeeting(s.db.QueryRow(`SELECT `+meetingCols+meetingFrom+` WHERE m.id=?`, id))
	if isNoRows(err) {
		return MeetingRecord{}, false, nil
	}
	if err != nil {
		return MeetingRecord{}, false, err
	}
	return m, true, nil
}

func (s *Store) MeetingsOn(day string) ([]MeetingRecord, error) {
	rows, err := s.db.Query(`SELECT `+meetingCols+meetingFrom+` WHERE m.day=? ORDER BY m.start`, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MeetingRecord
	for rows.Next() {
		m, err := scanMeeting(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type ParticipantRecord struct {
	MeetingID, Speaker, Name string
	Confidence               float64
	Confirmed                bool
}

func (s *Store) SetParticipant(p ParticipantRecord) error {
	_, err := s.db.Exec(`INSERT INTO participants(meeting_id,speaker,name,confidence,confirmed) VALUES(?,?,?,?,?)
		ON CONFLICT(meeting_id,speaker) DO UPDATE SET name=excluded.name,confidence=excluded.confidence,confirmed=excluded.confirmed`,
		p.MeetingID, p.Speaker, p.Name, p.Confidence, p.Confirmed)
	return err
}

func (s *Store) Participants(meetingID string) ([]ParticipantRecord, error) {
	rows, err := s.db.Query(`SELECT meeting_id,speaker,name,confidence,confirmed FROM participants WHERE meeting_id=? ORDER BY speaker`, meetingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ParticipantRecord
	for rows.Next() {
		var p ParticipantRecord
		if err := rows.Scan(&p.MeetingID, &p.Speaker, &p.Name, &p.Confidence, &p.Confirmed); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteParticipant removes one speaker of a meeting; a missing row is not
// an error.
func (s *Store) DeleteParticipant(meetingID, speaker string) error {
	_, err := s.db.Exec(`DELETE FROM participants WHERE meeting_id=? AND speaker=?`, meetingID, speaker)
	return err
}

// DeleteMeeting removes the meeting and its participants.
func (s *Store) DeleteMeeting(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM participants WHERE meeting_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM meetings WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}
