package web

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"workjournal/internal/config"
	"workjournal/internal/media"
	"workjournal/internal/process"
	"workjournal/internal/scheduler"
	"workjournal/internal/store"
)

const mtgID = "2026-10-07-1400-zoom"

// seedMeeting adds a 14:00-14:30 meeting with a transcript spanning hours
// 14 (inside) and 15 (outside the meeting, must not show).
func seedMeeting(t *testing.T, e *env, parts ...store.ParticipantRecord) {
	t.Helper()
	summary := `{"participants":[],"summary":"Planned the <i>launch</i>","decisions":["Ship Friday"],"action_items":[{"text":"Write notes","owner":"Priya"}]}`
	if err := e.st.UpsertMeeting(store.MeetingRecord{ID: mtgID, Day: seedDay, Start: seedTime(14, 0), End: seedTime(14, 30),
		App: "zoom.us", Title: "Launch sync", SummaryJSON: summary}); err != nil {
		t.Fatal(err)
	}
	doc := process.HourDoc{Day: seedDay, Hour: 14, Transcript: []media.Utterance{
		{Start: seedTime(14, 1), End: seedTime(14, 2), Track: "system", Speaker: "Speaker 2", Text: "I will write the notes"},
		{Start: seedTime(14, 3), End: seedTime(14, 4), Track: "mic", Speaker: "Me", Text: "Thanks"},
		{Start: seedTime(14, 40), End: seedTime(14, 41), Track: "system", Speaker: "Speaker 2", Text: "after the meeting chatter"},
	}}
	if err := doc.Save(e.p.DerivedHour(seedDay, 14)); err != nil {
		t.Fatal(err)
	}
	for _, p := range parts {
		p.MeetingID = mtgID
		if err := e.st.SetParticipant(p); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMeetingPageLowConfidenceName(t *testing.T) {
	e := newEnv(t)
	seedMeeting(t, e, store.ParticipantRecord{Speaker: "Speaker 2", Name: "Priya", Confidence: 0.5})
	rec := e.get("/meeting/" + mtgID)
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	body := rec.Body.String()
	mustContain(t, body, "Speaker 2 (Priya?)", "I will write the notes", "Launch sync", "Ship Friday", "Write notes")
	if strings.Contains(body, "after the meeting chatter") {
		t.Error("utterance outside the meeting window shown")
	}
}

func TestMeetingPageConfirmedName(t *testing.T) {
	e := newEnv(t)
	seedMeeting(t, e, store.ParticipantRecord{Speaker: "Speaker 2", Name: "Priya", Confidence: 0.2, Confirmed: true})
	body := e.get("/meeting/" + mtgID).Body.String()
	mustContain(t, body, `<strong class="spk">Priya</strong>`)
	if strings.Contains(body, "Priya?") {
		t.Error("confirmed name shown as a guess")
	}
}

func TestMeetingPageHighConfidenceAndEmptyName(t *testing.T) {
	e := newEnv(t)
	seedMeeting(t, e,
		store.ParticipantRecord{Speaker: "Speaker 2", Name: "Priya", Confidence: 0.9},
		store.ParticipantRecord{Speaker: "Speaker 3", Name: "", Confidence: 0})
	body := e.get("/meeting/" + mtgID).Body.String()
	mustContain(t, body, `<strong class="spk">Priya</strong>`, "Speaker 3")
}

func TestMeetingIDValidation(t *testing.T) {
	e := newEnv(t)
	if c := e.get("/meeting/bad%20id").Code; c != 400 {
		t.Errorf("malformed id = %d", c)
	}
	if c := e.get("/meeting/m1").Code; c != 400 {
		t.Errorf("short id = %d", c)
	}
	if c := e.get("/meeting/2026-10-07-1400-nope").Code; c != 404 {
		t.Errorf("unknown id = %d", c)
	}
	if c := e.post("/meeting/m1/speaker", url.Values{"speaker": {"Speaker 2"}, "name": {"X"}}, nil).Code; c != 400 {
		t.Errorf("post malformed id = %d", c)
	}
	if c := e.post("/meeting/2026-10-07-1400-nope/speaker", url.Values{"speaker": {"Speaker 2"}, "name": {"X"}}, nil).Code; c != 404 {
		t.Errorf("post unknown id = %d", c)
	}
}

func TestConfirmSpeakerEnrolls(t *testing.T) {
	e := newEnv(t)
	seedMeeting(t, e, store.ParticipantRecord{Speaker: "Speaker 2", Name: "Priya", Confidence: 0.5})
	e.jobs.enrollSaw = e.st
	rec := e.post("/meeting/"+mtgID+"/speaker", url.Values{"speaker": {"Speaker 2"}, "name": {"  Priya Shah  "}}, nil)
	if rec.Code != 303 {
		t.Fatalf("code %d: %s", rec.Code, rec.Body)
	}
	if len(e.jobs.enrolled) != 1 || e.jobs.enrolled[0] != [3]string{mtgID, "Speaker 2", "Priya Shah"} {
		t.Fatalf("enrolled = %v", e.jobs.enrolled)
	}
	if len(e.jobs.sawParticipants) != 1 || !e.jobs.sawParticipants[0].Confirmed {
		t.Errorf("participant not saved before enrol: %+v", e.jobs.sawParticipants)
	}
	ps, _ := e.st.Participants(mtgID)
	if len(ps) != 1 || ps[0].Name != "Priya Shah" || !ps[0].Confirmed || ps[0].Confidence != 1 {
		t.Errorf("participants = %+v", ps)
	}
	if loc := rec.Header().Get("Location"); loc != "/meeting/"+mtgID {
		t.Errorf("location = %q", loc)
	}
}

func TestSpeakerNameValidation(t *testing.T) {
	e := newEnv(t)
	seedMeeting(t, e, store.ParticipantRecord{Speaker: "Speaker 2", Name: "Priya", Confidence: 0.5})
	for _, name := range []string{"", "   ", strings.Repeat("x", 81)} {
		rec := e.post("/meeting/"+mtgID+"/speaker", url.Values{"speaker": {"Speaker 2"}, "name": {name}}, nil)
		if rec.Code != 400 {
			t.Errorf("name len %d: code %d", len(name), rec.Code)
		}
	}
	if len(e.jobs.enrolled) != 0 {
		t.Error("enrolled on invalid input")
	}
	if rec := e.post("/meeting/"+mtgID+"/speaker", url.Values{"speaker": {""}, "name": {"A"}}, nil); rec.Code != 400 {
		t.Errorf("empty speaker: %d", rec.Code)
	}
}

func TestSpeakerPostRejectsForeignOrigin(t *testing.T) {
	e := newEnv(t)
	seedMeeting(t, e)
	rec := e.post("/meeting/"+mtgID+"/speaker", url.Values{"speaker": {"Speaker 2"}, "name": {"A"}}, map[string]string{"Origin": "http://evil.example"})
	if rec.Code != 403 {
		t.Errorf("code %d", rec.Code)
	}
}

func TestEnrollWithExpiredMediaShowsMessage(t *testing.T) {
	e := newEnv(t)
	seedMeeting(t, e, store.ParticipantRecord{Speaker: "Speaker 2", Name: "Priya", Confidence: 0.5})
	e.jobs.enrollErr = scheduler.ErrMediaExpired
	rec := e.post("/meeting/"+mtgID+"/speaker", url.Values{"speaker": {"Speaker 2"}, "name": {"Priya"}}, nil)
	if rec.Code != 303 {
		t.Fatalf("code %d", rec.Code)
	}
	ps, _ := e.st.Participants(mtgID)
	if len(ps) != 1 || !ps[0].Confirmed || ps[0].Name != "Priya" {
		t.Fatalf("name not kept: %+v", ps)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "notice=expired") {
		t.Fatalf("location %q", loc)
	}
	body := e.get(loc).Body.String()
	mustContain(t, body, "raw media expired — name saved, voice not learned")
}

func TestEnrollOtherErrorKeepsName(t *testing.T) {
	e := newEnv(t)
	seedMeeting(t, e, store.ParticipantRecord{Speaker: "Speaker 2", Name: "Priya", Confidence: 0.5})
	e.jobs.enrollErr = errors.New("boom")
	rec := e.post("/meeting/"+mtgID+"/speaker", url.Values{"speaker": {"Speaker 2"}, "name": {"Priya"}}, nil)
	loc := rec.Header().Get("Location")
	if rec.Code != 303 || !strings.Contains(loc, "notice=enroll-failed") {
		t.Fatalf("code %d loc %q", rec.Code, loc)
	}
	if ps, _ := e.st.Participants(mtgID); len(ps) != 1 || !ps[0].Confirmed {
		t.Errorf("name not kept: %+v", ps)
	}
	if body := e.get(loc).Body.String(); !strings.Contains(body, "voice not learned") {
		t.Errorf("no failure notice: %s", body)
	}
}

func TestUnknownNoticeIgnored(t *testing.T) {
	e := newEnv(t)
	seedMeeting(t, e)
	body := e.get("/meeting/" + mtgID + "?notice=<script>x</script>").Body.String()
	if strings.Contains(body, "<script>x") {
		t.Error("notice reflected")
	}
}

func TestDiarizationOffHidesNamingForm(t *testing.T) {
	e := newEnv(t)
	seedMeeting(t, e, store.ParticipantRecord{Speaker: "Speaker 2", Name: "Priya", Confidence: 0.5})
	if !strings.Contains(e.get("/meeting/"+mtgID).Body.String(), `action="/meeting/`+mtgID+`/speaker"`) {
		t.Fatal("naming form missing with diarization on")
	}
	cfg := e.srv.Cfg()
	cfg.Diarization = "off"
	e.srv.Cfg = func() config.Config { return cfg }
	body := e.get("/meeting/" + mtgID).Body.String()
	if strings.Contains(body, "/speaker") {
		t.Error("naming form shown with diarization off")
	}
}

func TestMeetingPageEscapesHostileInput(t *testing.T) {
	e := newEnv(t)
	seedMeeting(t, e, store.ParticipantRecord{Speaker: "Speaker 2", Name: "<script>alert(1)</script>", Confidence: 0.9})
	doc := process.HourDoc{Day: seedDay, Hour: 14, Transcript: []media.Utterance{
		{Start: seedTime(14, 1), End: seedTime(14, 2), Track: "system", Speaker: "Speaker 2", Text: "<script>alert(1)</script>"}}}
	if err := doc.Save(e.p.DerivedHour(seedDay, 14)); err != nil {
		t.Fatal(err)
	}
	body := e.get("/meeting/" + mtgID).Body.String()
	if strings.Contains(body, "<script>alert(1)") {
		t.Errorf("unescaped script in page:\n%s", body)
	}
	mustContain(t, body, "&lt;script&gt;alert(1)&lt;/script&gt;")
}

func TestDayPageLinksMeeting(t *testing.T) {
	e := newEnv(t)
	mustContain(t, e.get("/day/"+seedDay).Body.String(), `href="/meeting/2026-10-07-0910-zoom"`)
}

func TestNameUnknownSpeakerRejected(t *testing.T) {
	e := newEnv(t)
	seedMeeting(t, e, store.ParticipantRecord{Speaker: "Speaker 2", Name: "Priya", Confidence: 0.5})
	rec := e.post("/meeting/"+mtgID+"/speaker", url.Values{"speaker": {"Speaker 99"}, "name": {"Bogus"}}, nil)
	if rec.Code != 400 {
		t.Fatalf("code %d", rec.Code)
	}
	if ps, _ := e.st.Participants(mtgID); len(ps) != 1 || ps[0].Speaker != "Speaker 2" {
		t.Errorf("bogus participant stored: %+v", ps)
	}
	if len(e.jobs.enrolled) != 0 {
		t.Error("enrolled unknown speaker")
	}
}

func TestNameSpeakerRejectedWhenDiarizationOff(t *testing.T) {
	e := newEnv(t)
	seedMeeting(t, e, store.ParticipantRecord{Speaker: "Speaker 2", Name: "Priya", Confidence: 0.5})
	cfg := e.srv.Cfg()
	cfg.Diarization = "off"
	e.srv.Cfg = func() config.Config { return cfg }
	rec := e.post("/meeting/"+mtgID+"/speaker", url.Values{"speaker": {"Speaker 2"}, "name": {"Priya Shah"}}, nil)
	if rec.Code != 409 {
		t.Fatalf("code %d", rec.Code)
	}
	if ps, _ := e.st.Participants(mtgID); len(ps) != 1 || ps[0].Confirmed || ps[0].Name != "Priya" {
		t.Errorf("participant changed: %+v", ps)
	}
	if len(e.jobs.enrolled) != 0 {
		t.Error("enrolled with diarization off")
	}
}
