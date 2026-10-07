package store

import "time"

type MeetingRecord struct {
	ID, Day     string
	Start, End  time.Time
	App, Title  string
	SummaryJSON string
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
	if err := r.Scan(&m.ID, &m.Day, &st, &en, &m.App, &m.Title, &m.SummaryJSON); err != nil {
		return m, err
	}
	var err error
	if m.Start, err = parseTime(st); err != nil {
		return m, err
	}
	m.End, err = parseTime(en)
	return m, err
}

const meetingCols = `id,day,start,end,app,title,summary_json`

func (s *Store) Meeting(id string) (MeetingRecord, bool, error) {
	m, err := scanMeeting(s.db.QueryRow(`SELECT `+meetingCols+` FROM meetings WHERE id=?`, id))
	if isNoRows(err) {
		return MeetingRecord{}, false, nil
	}
	if err != nil {
		return MeetingRecord{}, false, err
	}
	return m, true, nil
}

func (s *Store) MeetingsOn(day string) ([]MeetingRecord, error) {
	rows, err := s.db.Query(`SELECT `+meetingCols+` FROM meetings WHERE day=? ORDER BY start`, day)
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
