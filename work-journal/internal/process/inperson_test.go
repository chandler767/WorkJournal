package process

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"workjournal/internal/activity"
	"workjournal/internal/diarize"
	"workjournal/internal/media"
	"workjournal/internal/store"
)

// talk is mic speech: 7 s of every 10 s over [from, to) seconds of a chunk.
func talk(prefix string, from, to int) []fu {
	var out []fu
	for s := from; s < to; s += 10 {
		out = append(out, fu{float64(s), float64(s + 7), fmt.Sprintf("%s%d", prefix, s)})
	}
	return out
}

// inPersonFixture: hour 14 with the screen locked and ten minutes of dense
// mic conversation (chunks 1400 and 1405), one system voice inside it, and a
// lone mic remark at 14:20 after the conversation.
func inPersonFixture(e *env) {
	e.activity(old, ev(14, 0, "start"), ev(14, 0, "app", "loginwindow"), ev(14, 2, "idle"))
	for _, f := range []string{"audio/1400-mic.m4a", "audio/1400-system.m4a", "audio/1405-mic.m4a", "audio/1415-mic.m4a"} {
		e.file(f, old)
	}
	e.tr.utts["1400-mic"] = talk("a", 0, 300)
	e.tr.utts["1405-mic"] = talk("b", 0, 300)
	e.tr.utts["1415-mic"] = []fu{{300, 303, "lone remark"}, {400, 401, "You"}}
	e.tr.utts["1400-system"] = []fu{{200, 203, "system voice"}}
	e.dz.by["1400-mic"] = diar{
		turns: []diarize.Turn{tn(0, 140, 0), tn(150, 300, 1)}, // a140 (140-147 s) falls between turns
		embs:  map[int][]float32{0: {1, 0, 0}, 1: {0, 1, 0}},
	}
	e.dz.by["1405-mic"] = diar{
		turns: []diarize.Turn{tn(0, 300, 4)},
		embs:  map[int][]float32{4: {0, 1, 0.05}}, // the same voice as 1400's cluster 1
	}
	e.dz.by["1400-system"] = diar{
		turns: []diarize.Turn{tn(195, 210, 0)},
		embs:  map[int][]float32{0: {0, 0, 1}},
	}
	if err := e.p.Voices.Enroll("me", []float32{1, 0, 0}); err != nil {
		e.t.Fatal(err)
	}
}

func TestInPersonMicSpeechIsDiarized(t *testing.T) {
	e := newEnv(t)
	inPersonFixture(e)
	doc, err := e.p.Transcribe(context.Background(), day, 14)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Meetings) != 1 || doc.Meetings[0].App != activity.InPersonApp || doc.Meetings[0].ID != "2026-10-07-1400-in-person" {
		t.Fatalf("meetings = %+v", doc.Meetings)
	}
	label := map[string]string{}
	for _, u := range doc.Transcript {
		label[u.Text] = u.Track + "/" + u.Speaker
	}
	want := map[string]string{
		"a0":           "mic/me", // cluster 0 is the enrolled "me" voice
		"a130":         "mic/me",
		"a140":         "mic/Speaker ?", // in the meeting but no diarizer turn
		"a150":         "mic/Speaker 2", // the second voice to speak; "me" was the first
		"a290":         "mic/Speaker 2",
		"b0":           "mic/Speaker 2", // the same voice across chunks
		"b290":         "mic/Speaker 2",
		"system voice": "system/Speaker 3", // shared numbering with mic speakers
		"lone remark":  "mic/me",           // outside the meeting: not diarized
	}
	for text, w := range want {
		if label[text] != w {
			t.Errorf("%q labelled %q, want %q", text, label[text], w)
		}
	}
	if _, ok := label["You"]; ok {
		t.Errorf("isolated silence hallucination kept")
	}
	slices.Sort(e.dz.calls)
	if !reflect.DeepEqual(e.dz.calls, []string{"1400-mic", "1400-system", "1405-mic"}) {
		t.Errorf("diarized %v; the mic chunk outside the meeting must not be", e.dz.calls)
	}
	// Locked and idle time inside the meeting is split off for categorization.
	var bounds []string
	for _, s := range doc.Segments {
		bounds = append(bounds, s.Start.Format("15:04:05")+"-"+s.End.Format("15:04:05")+" "+s.Kind)
	}
	end := doc.Meetings[0].End.Format("15:04:05")
	if !slices.Contains(bounds, "14:02:00-"+end+" idle") || !slices.Contains(bounds, end+"-15:00:00 idle") {
		t.Errorf("segments %v not split at the meeting end %s", bounds, end)
	}

	// The mic diarization is cached like the system chunks'.
	e.resetCounts()
	again, err := e.p.Transcribe(context.Background(), day, 14)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.dz.calls) != 0 || len(e.tr.calls) != 0 {
		t.Fatalf("cache miss: diarizer %v, whisper %v", e.dz.calls, e.tr.calls)
	}
	if !reflect.DeepEqual(speakers(again.Transcript), speakers(doc.Transcript)) {
		t.Fatalf("cached labels differ")
	}
}

func TestInPersonMicWithoutDiarizationStaysMe(t *testing.T) {
	e := newEnv(t)
	inPersonFixture(e)
	e.p.Cfg.Diarization = "off"
	doc, err := e.p.Transcribe(context.Background(), day, 14)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range doc.Transcript {
		if u.Track == "mic" && u.Speaker != "me" {
			t.Fatalf("mic utterance %q labelled %q", u.Text, u.Speaker)
		}
	}
	if len(doc.Meetings) != 1 {
		t.Fatalf("meetings = %+v", doc.Meetings)
	}
}

func TestSkillSpeakerNames(t *testing.T) {
	for _, c := range []struct {
		u    media.Utterance
		want string
	}{
		{media.Utterance{Track: "mic", Speaker: "me"}, "You"},
		{media.Utterance{Track: "mic", Speaker: ""}, "You"},
		{media.Utterance{Track: "mic", Speaker: "Speaker 3"}, "Speaker 3"},
		{media.Utterance{Track: "mic", Speaker: "Priya"}, "Priya"},
		{media.Utterance{Track: "mic", Speaker: "Speaker ?"}, "Speaker ?"},
		{media.Utterance{Track: "system", Speaker: "Speaker 1"}, "Speaker 1"},
	} {
		if got := speaker(c.u); got != c.want {
			t.Errorf("speaker(%+v) = %q, want %q", c.u, got, c.want)
		}
	}
}

func mic(from, to int, speaker, text string) media.Utterance {
	return media.Utterance{Start: at(14, 0, from), End: at(14, 0, to), Track: "mic", Speaker: speaker, Text: text}
}

func TestEnrollMicSpeakerInPersonMeeting(t *testing.T) {
	e := newEnv(t)
	e.file("audio/1400-mic.m4a", old)
	e.file("audio/1400-system.m4a", old)
	doc := &HourDoc{
		Day: day, Hour: 14, Status: store.StatusDone,
		Transcript: []media.Utterance{
			mic(1, 5, "Speaker 1", "my point"),
			mic(10, 20, "Speaker 2", "their point"),
			mic(30, 35, "Speaker 1", "my second point"),
			sys(at(14, 0, 40), at(14, 0, 45), "Speaker 1", "unrelated system speech"),
		},
	}
	if err := doc.Save(e.paths.DerivedHour(day, 14)); err != nil {
		t.Fatal(err)
	}
	m := store.MeetingRecord{ID: "2026-10-07-1400-in-person", Day: day, Start: at(14, 0, 0), End: at(14, 20, 0), App: activity.InPersonApp}
	if err := e.st.UpsertMeeting(m); err != nil {
		t.Fatal(err)
	}
	e.dz.by["1400-mic"] = diar{
		turns: []diarize.Turn{tn(0, 6, 0), tn(9, 21, 1), tn(29, 36, 0)},
		embs:  map[int][]float32{0: {3, 0, 0}, 1: {0, 2, 0}},
	}
	if err := e.p.EnrollSpeaker(context.Background(), m, "Speaker 1", "me"); err != nil {
		t.Fatal(err)
	}
	if name, _, ok := e.p.Voices.Match([]float32{1, 0, 0}); !ok || name != "me" {
		t.Fatalf("voice not enrolled from the mic track: %q %v", name, ok)
	}
	if !reflect.DeepEqual(e.dz.calls, []string{"1400-mic"}) {
		t.Fatalf("diarized %v, want only the mic chunk", e.dz.calls)
	}
	got, err := Load(e.paths.DerivedHour(day, 14))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"me: my point", "Speaker 2: their point", "me: my second point", "Speaker 1: unrelated system speech"}
	if s := speakers(got.Transcript); !reflect.DeepEqual(s, want) {
		t.Fatalf("transcript = %q", s)
	}
}

func TestInPersonMeetingSummaryListsMicSpeakers(t *testing.T) {
	e := newSumEnv(t)
	m := activity.Meeting{ID: "2026-10-07-1400-in-person", Start: at(14, 0, 0), End: at(14, 20, 0), App: activity.InPersonApp}
	doc := &HourDoc{Day: day, Hour: 14,
		Segments: []activity.Segment{sg(activity.KindIdle, "loginwindow", "", 14, 0, 60)},
		Transcript: []media.Utterance{
			mic(1, 5, "me", "let's start"),
			mic(10, 20, "Speaker 1", "the budget is approved"),
			mic(30, 35, "Speaker 2", "great"),
			mic(40, 45, "Priya", "I'll send it"),      // a known voice the skill left out
			mic(50, 52, "Speaker ?", "unclear words"), // no voice: never a participant
		},
		Meetings:      []activity.Meeting{m},
		EndedMeetings: []string{m.ID},
	}
	e.transcribed(doc)
	e.fk.out["summarize-hour"] = hourOut
	// The skill forgets Speaker 2; the meeting page must still list it.
	e.fk.out["summarize-meeting"] = `{"participants":[{"speaker":"You","name":"","confidence":1},{"speaker":"Speaker 1","name":"","confidence":0}],` +
		`"summary":"Budget approved.","decisions":[],"action_items":[]}`
	if err := e.s.SummarizeHour(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	mc := e.fk.calls[1]
	if mc.skill != "summarize-meeting" || mc.input["meeting"].(map[string]any)["app"] != "In person" {
		t.Fatalf("meeting call %s input %v", mc.skill, mc.input)
	}
	var lines []string
	for _, l := range mc.input["transcript"].([]any) {
		lines = append(lines, l.(map[string]any)["speaker"].(string))
	}
	if !reflect.DeepEqual(lines, []string{"You", "Speaker 1", "Speaker 2", "Priya", "Speaker ?"}) {
		t.Fatalf("transcript speakers = %v", lines)
	}
	ps, err := e.st.Participants(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range ps {
		got = append(got, p.Speaker+"="+p.Name)
	}
	if !reflect.DeepEqual(got, []string{"Priya=Priya", "Speaker 1=", "Speaker 2=", "You="}) {
		t.Fatalf("participants = %v", got)
	}
}

func TestInPersonNeedsTwoVoices(t *testing.T) {
	e := newEnv(t)
	inPersonFixture(e)
	// Dictation: every mic turn is the same voice.
	e.dz.by["1400-mic"] = diar{turns: []diarize.Turn{tn(0, 300, 0)}, embs: map[int][]float32{0: {0, 1, 0}}}
	e.dz.by["1405-mic"] = diar{turns: []diarize.Turn{tn(0, 300, 2)}, embs: map[int][]float32{2: {0, 1, 0.05}}}
	doc, err := e.p.Transcribe(context.Background(), day, 14)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Meetings) != 0 {
		t.Fatalf("one voice made a meeting: %+v", doc.Meetings)
	}
	for _, u := range doc.Transcript {
		if u.Track == "mic" && u.Speaker != "me" {
			t.Fatalf("mic utterance %q labelled %q", u.Text, u.Speaker)
		}
	}
	for _, s := range doc.Segments {
		if s.Start.Equal(at(14, 9, 57)) || s.End.Equal(at(14, 9, 57)) {
			t.Fatalf("segments split at a dropped meeting: %+v", doc.Segments)
		}
	}
	if ms, _ := e.st.MeetingsOn(day); len(ms) != 0 {
		t.Fatalf("stored meetings = %+v", ms)
	}
	// The next hour, which sees the conversation only through hour 14's doc,
	// does not bring it back.
	e.file("audio/1500-mic.m4a", old)
	e.tr.utts["1500-mic"] = []fu{{10, 12, "later"}}
	next, err := e.p.Transcribe(context.Background(), day, 15)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Meetings) != 0 {
		t.Fatalf("hour 15 meetings = %+v", next.Meetings)
	}
}

func TestHallucinationFilterSeesBothTracks(t *testing.T) {
	e := newEnv(t)
	transcribeFixture(e)
	// System "second voice" is 12-15 s into 1300-system; the mic replies at 16 s.
	e.tr.utts["1300-mic"] = []fu{{1, 3, "mic hello"}, {16, 17, "Thank you."}, {200, 201, "You"}}
	doc, err := e.p.Transcribe(context.Background(), day, 13)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, u := range doc.Transcript {
		texts = append(texts, u.Text)
	}
	if !slices.Contains(texts, "Thank you.") || slices.Contains(texts, "You") {
		t.Fatalf("transcript = %q: want the reply kept and the isolated You dropped", texts)
	}
}

func TestStaleMeetingsDeleted(t *testing.T) {
	e := newEnv(t)
	transcribeFixture(e) // hour 13: no meetings detected
	recs := []store.MeetingRecord{
		{ID: "2026-10-07-1305-in-person", Day: day, Start: at(13, 5, 0), End: at(13, 20, 0), App: activity.InPersonApp},  // stale: deleted
		{ID: "2026-10-07-1250-in-person", Day: day, Start: at(12, 50, 0), End: at(13, 10, 0), App: activity.InPersonApp}, // stale, overlaps: deleted
		{ID: "2026-10-07-1330-call", Day: day, Start: at(13, 30, 0), End: at(13, 40, 0), App: "call"},                    // stale but named: kept
		{ID: "2026-10-07-1100-zoom-us", Day: day, Start: at(11, 0, 0), End: at(11, 30, 0), App: "zoom.us"},               // another hour: kept
		{ID: "2026-10-07-1200-in-person", Day: day, Start: at(12, 0, 0), End: at(13, 0, 0), App: activity.InPersonApp},   // ends at 13:00: kept
	}
	for _, r := range recs {
		if err := e.st.UpsertMeeting(r); err != nil {
			t.Fatal(err)
		}
		if err := e.st.SetParticipant(store.ParticipantRecord{MeetingID: r.ID, Speaker: "Speaker 1", Name: "Ann", Confidence: 0.9}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.st.SetParticipant(store.ParticipantRecord{MeetingID: "2026-10-07-1330-call", Speaker: "Speaker 2", Name: "Bob", Confidence: 1, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.p.Transcribe(context.Background(), day, 13); err != nil {
		t.Fatal(err)
	}
	ms, err := e.st.MeetingsOn(day)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	if want := []string{"2026-10-07-1100-zoom-us", "2026-10-07-1200-in-person", "2026-10-07-1330-call"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("meetings = %v, want %v", ids, want)
	}
	if ps, _ := e.st.Participants("2026-10-07-1305-in-person"); len(ps) != 0 {
		t.Fatalf("participants of a deleted meeting kept: %+v", ps)
	}
}

func TestInPersonWithNoopDiarizerKeepsMeetingAndMe(t *testing.T) {
	e := newEnv(t)
	inPersonFixture(e)
	e.p.Diarizer = diarize.NoopDiarizer{} // speaker models missing
	doc, err := e.p.Transcribe(context.Background(), day, 14)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Meetings) != 1 || doc.Meetings[0].App != activity.InPersonApp {
		t.Fatalf("meetings = %+v", doc.Meetings)
	}
	for _, u := range doc.Transcript {
		if (u.Track == "mic" && u.Speaker != "me") || (u.Track == "system" && u.Speaker != "others") {
			t.Fatalf("%s utterance %q labelled %q", u.Track, u.Text, u.Speaker)
		}
	}
}
