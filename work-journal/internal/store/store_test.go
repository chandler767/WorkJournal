package store

import (
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func at(day string, h, m int) time.Time {
	d, err := time.ParseInLocation("2006-01-02", day, time.Local)
	if err != nil {
		panic(err)
	}
	return d.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
}

func TestHourUpsertAndStatusQuery(t *testing.T) {
	s := openTest(t)
	now := at("2026-10-07", 9, 5)
	if err := s.UpsertHour(HourRecord{Day: "2026-10-07", Hour: 9, Status: StatusPending, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertHour(HourRecord{Day: "2026-10-07", Hour: 10, Status: StatusDone, SummaryJSON: `{"a":1}`, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertHour(HourRecord{Day: "2026-10-07", Hour: 9, Status: StatusTranscribed, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	h, ok, err := s.Hour("2026-10-07", 9)
	if err != nil || !ok || h.Status != StatusTranscribed || !h.UpdatedAt.Equal(now) {
		t.Fatalf("Hour = %+v ok=%v err=%v", h, ok, err)
	}
	if _, ok, _ := s.Hour("2026-10-07", 3); ok {
		t.Fatal("expected missing hour")
	}
	got, err := s.HoursByStatus(StatusTranscribed, StatusNeedsReview)
	if err != nil || len(got) != 1 || got[0].Hour != 9 {
		t.Fatalf("HoursByStatus = %+v err=%v", got, err)
	}
	if got, err := s.HoursByStatus(); err != nil || len(got) != 0 {
		t.Fatalf("empty statuses = %+v err=%v", got, err)
	}
	on, err := s.HoursOn("2026-10-07")
	if err != nil || len(on) != 2 || on[0].Hour != 9 || on[1].Hour != 10 || on[1].SummaryJSON != `{"a":1}` {
		t.Fatalf("HoursOn = %+v err=%v", on, err)
	}
}

func seg(day string, hour, m int, dur time.Duration, cat string) SegmentRecord {
	st := at(day, hour, m)
	return SegmentRecord{Day: day, Hour: hour, Start: st, End: st.Add(dur), App: "Zoom", Title: "t", Kind: "app", Category: cat, Confidence: 0.9, Source: "rule"}
}

func TestReplaceSegmentsIsIdempotent(t *testing.T) {
	s := openTest(t)
	segs := []SegmentRecord{seg("2026-10-07", 9, 0, 10*time.Minute, "Meetings"), seg("2026-10-07", 9, 20, 5*time.Minute, "Admin")}
	for i := 0; i < 2; i++ {
		if err := s.ReplaceSegments("2026-10-07", 9, segs); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.SegmentsOn("2026-10-07")
	if err != nil || len(got) != 2 {
		t.Fatalf("got %d err=%v", len(got), err)
	}
	if !got[0].Start.Equal(segs[0].Start) || !got[0].End.Equal(segs[0].End) || got[0].Category != "Meetings" || got[0].Confidence != 0.9 || got[0].Source != "rule" {
		t.Fatalf("seg0 = %+v", got[0])
	}
	if err := s.ReplaceSegments("2026-10-07", 9, segs[:1]); err != nil {
		t.Fatal(err)
	}
	got, _ = s.SegmentsOn("2026-10-07")
	if len(got) != 1 {
		t.Fatalf("after shrink got %d", len(got))
	}
}

func TestCategoryTotalsAcrossDays(t *testing.T) {
	s := openTest(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.ReplaceSegments("2026-10-06", 9, []SegmentRecord{seg("2026-10-06", 9, 0, 30*time.Minute, "Meetings")}))
	must(s.ReplaceSegments("2026-10-07", 10, []SegmentRecord{seg("2026-10-07", 10, 0, 15*time.Minute, "Admin"), seg("2026-10-07", 10, 30, 10*time.Minute, "")}))
	must(s.ReplaceSegments("2026-10-09", 10, []SegmentRecord{seg("2026-10-09", 10, 0, 40*time.Minute, "Meetings")}))
	got, err := s.CategoryTotals("2026-10-06", "2026-10-07")
	must(err)
	if len(got) != 2 || got["Meetings"] != 30*time.Minute || got["Admin"] != 15*time.Minute {
		t.Fatalf("totals = %v", got)
	}
}

func TestSetSegmentCategoryMarksUser(t *testing.T) {
	s := openTest(t)
	sg := seg("2026-10-07", 9, 0, 10*time.Minute, "Admin")
	if err := s.ReplaceSegments("2026-10-07", 9, []SegmentRecord{sg}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSegmentCategory("2026-10-07", 9, sg.Start, "Deep work"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.SegmentsOn("2026-10-07")
	if len(got) != 1 || got[0].Category != "Deep work" || got[0].Source != "user" {
		t.Fatalf("got %+v", got)
	}
}

func TestSearchFindsTranscriptAndSnippet(t *testing.T) {
	s := openTest(t)
	docs := []TextDoc{
		{Kind: "transcript", Day: "2026-10-07", Hour: 9, TS: at("2026-10-07", 9, 3), Text: "we need to discuss the budget for next quarter", Ref: "r1"},
		{Kind: "ocr", Day: "2026-10-07", Hour: 9, TS: at("2026-10-07", 9, 4), Text: "unrelated screen text", Ref: "r2"},
	}
	if err := s.ReplaceText("2026-10-07", 9, docs); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search("budget", 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits=%+v err=%v", hits, err)
	}
	h := hits[0]
	if h.Kind != "transcript" || h.Day != "2026-10-07" || h.Hour != 9 || h.Ref != "r1" || !h.TS.Equal(docs[0].TS) {
		t.Fatalf("hit = %+v", h)
	}
	if !contains(h.Snippet, "budget") {
		t.Fatalf("snippet %q", h.Snippet)
	}
	for _, q := range []string{"", "   "} {
		hits, err := s.Search(q, 10)
		if err != nil || len(hits) != 0 {
			t.Fatalf("q=%q hits=%v err=%v", q, hits, err)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestSearchToleratesSyntaxChars(t *testing.T) {
	s := openTest(t)
	for _, q := range []string{"foo\" AND", "AND", "a OR", "(", "NEAR(", "*", "col:x", "\"\""} {
		if _, err := s.Search(q, 5); err != nil {
			t.Fatalf("q=%q: %v", q, err)
		}
	}
}

func TestReplaceTextRemovesOldDocs(t *testing.T) {
	s := openTest(t)
	d := func(day string, hour int, text string) TextDoc {
		return TextDoc{Kind: "note", Day: day, Hour: hour, TS: at(day, hour, 0), Text: text}
	}
	s.ReplaceText("2026-10-07", 9, []TextDoc{d("2026-10-07", 9, "alpha")})
	s.ReplaceText("2026-10-07", 10, []TextDoc{d("2026-10-07", 10, "alpha")})
	if err := s.ReplaceText("2026-10-07", 9, []TextDoc{d("2026-10-07", 9, "beta")}); err != nil {
		t.Fatal(err)
	}
	hits, _ := s.Search("alpha", 10)
	if len(hits) != 1 || hits[0].Hour != 10 {
		t.Fatalf("alpha hits = %+v", hits)
	}
	hits, _ = s.Search("beta", 10)
	if len(hits) != 1 || hits[0].Hour != 9 {
		t.Fatalf("beta hits = %+v", hits)
	}
}

func TestKV(t *testing.T) {
	s := openTest(t)
	if _, ok, err := s.GetKV("k"); ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	s.SetKV("k", "v1")
	s.SetKV("k", "v2")
	v, ok, err := s.GetKV("k")
	if err != nil || !ok || v != "v2" {
		t.Fatalf("v=%q ok=%v err=%v", v, ok, err)
	}
}

func TestMeetingsAndParticipantsUpsert(t *testing.T) {
	s := openTest(t)
	m := MeetingRecord{ID: "m1", Day: "2026-10-07", Start: at("2026-10-07", 9, 0), End: at("2026-10-07", 9, 30), App: "Zoom", Title: "Standup", SummaryJSON: "{}"}
	if err := s.UpsertMeeting(m); err != nil {
		t.Fatal(err)
	}
	m.Title = "Standup 2"
	if err := s.UpsertMeeting(m); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Meeting("m1")
	if err != nil || !ok || got.Title != "Standup 2" || !got.End.Equal(m.End) {
		t.Fatalf("got %+v ok=%v err=%v", got, ok, err)
	}
	if _, ok, _ := s.Meeting("nope"); ok {
		t.Fatal("expected missing")
	}
	on, err := s.MeetingsOn("2026-10-07")
	if err != nil || len(on) != 1 {
		t.Fatalf("on=%+v err=%v", on, err)
	}
}

func TestParticipantsUpsert(t *testing.T) {
	s := openTest(t)
	s.SetParticipant(ParticipantRecord{MeetingID: "m1", Speaker: "S1", Name: "Ann", Confidence: 0.4})
	s.SetParticipant(ParticipantRecord{MeetingID: "m1", Speaker: "S2", Name: "Bob", Confidence: 0.5})
	if err := s.SetParticipant(ParticipantRecord{MeetingID: "m1", Speaker: "S1", Name: "Anna", Confidence: 1, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	ps, err := s.Participants("m1")
	if err != nil || len(ps) != 2 {
		t.Fatalf("ps=%+v err=%v", ps, err)
	}
	if ps[0].Speaker != "S1" || ps[0].Name != "Anna" || !ps[0].Confirmed || ps[0].Confidence != 1 || ps[1].Name != "Bob" || ps[1].Confirmed {
		t.Fatalf("ps=%+v", ps)
	}
}

func TestDayRevAndHourRev(t *testing.T) {
	st := openTest(t)
	day := "2026-10-07"
	t0 := time.Date(2026, 10, 7, 9, 0, 0, 0, time.Local)
	r0, err := st.DayRev(day)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertHour(HourRecord{Day: day, Hour: 9, Status: StatusDone, UpdatedAt: t0, SummaryJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	r1, _ := st.DayRev(day)
	if r1 == r0 {
		t.Error("DayRev unchanged after hour insert")
	}
	if r2, _ := st.DayRev(day); r2 != r1 {
		t.Error("DayRev unstable")
	}
	h0, _ := st.HourRev(day, 9)
	if err := st.UpsertHour(HourRecord{Day: day, Hour: 9, Status: StatusDone, UpdatedAt: t0, SummaryJSON: `{"a":1}`}); err != nil {
		t.Fatal(err)
	}
	if h1, _ := st.HourRev(day, 9); h1 == h0 {
		t.Error("HourRev unchanged after summary change")
	}
}

func TestDeleteMeetingRemovesParticipants(t *testing.T) {
	s := openTest(t)
	for _, id := range []string{"m1", "m2"} {
		if err := s.UpsertMeeting(MeetingRecord{ID: id, Day: "2026-10-07", Start: at("2026-10-07", 9, 0), End: at("2026-10-07", 9, 30)}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetParticipant(ParticipantRecord{MeetingID: id, Speaker: "S1"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteMeeting("m1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Meeting("m1"); ok {
		t.Fatal("m1 still there")
	}
	if ps, err := s.Participants("m1"); err != nil || len(ps) != 0 {
		t.Fatalf("m1 participants = %+v %v", ps, err)
	}
	if _, ok, _ := s.Meeting("m2"); !ok {
		t.Fatal("m2 deleted")
	}
	if ps, _ := s.Participants("m2"); len(ps) != 1 {
		t.Fatalf("m2 participants = %+v", ps)
	}
}
