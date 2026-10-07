package process

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"workjournal/internal/diarize"
	"workjournal/internal/media"
	"workjournal/internal/store"
)

func sys(from, to time.Time, speaker, text string) media.Utterance {
	return media.Utterance{Start: from, End: to, Track: "system", Speaker: speaker, Text: text}
}

// enrollFixture: a 13:00–13:30 meeting with two system chunks (13:00, 13:05)
// and hour 13's derived doc, already labelled by the pipeline.
func enrollFixture(e *env) store.MeetingRecord {
	e.t.Helper()
	e.file("audio/1300-system.m4a", old)
	e.file("audio/1305-system.m4a", old)
	e.file("audio/1300-mic.m4a", old)
	doc := &HourDoc{
		Day: day, Hour: 13, Status: store.StatusDone,
		Transcript: []media.Utterance{
			{Start: at(13, 0, 1), End: at(13, 0, 2), Track: "mic", Speaker: "me", Text: "hi all"},
			sys(at(13, 0, 2), at(13, 0, 8), "Speaker 1", "first point"),
			sys(at(13, 0, 10), at(13, 0, 20), "Speaker 2", "second voice"),
			sys(at(13, 5, 1), at(13, 5, 5), "Speaker 1", "another point"),
			sys(at(13, 45, 0), at(13, 45, 5), "Speaker 1", "after the meeting"),
		},
		Screens: []media.ScreenText{{Time: at(13, 10, 0), File: "131000.jpg", Text: "roadmap slide"}},
	}
	if err := doc.Save(e.paths.DerivedHour(day, 13)); err != nil {
		e.t.Fatal(err)
	}
	m := store.MeetingRecord{ID: "2026-10-07-1300-zoom-us", Day: day, Start: at(13, 0, 0), End: at(13, 30, 0), App: "zoom.us"}
	if err := e.st.UpsertMeeting(m); err != nil {
		e.t.Fatal(err)
	}
	// Turns are seconds from each chunk's start; cluster ids are chunk-local.
	e.dz.by["1300-system"] = diar{
		turns: []diarize.Turn{tn(0, 9, 0), tn(9, 21, 1)},
		embs:  map[int][]float32{0: {2, 0, 0}, 1: {0, 0, 5}},
	}
	e.dz.by["1305-system"] = diar{
		turns: []diarize.Turn{tn(0.5, 6, 3)},
		embs:  map[int][]float32{3: {0, 3, 0}},
	}
	return m
}

func TestEnrollSpeakerAveragesEmbeddingsAndRelabels(t *testing.T) {
	e := newEnv(t)
	m := enrollFixture(e)
	if err := e.p.EnrollSpeaker(context.Background(), m, "Speaker 1", "Ana"); err != nil {
		t.Fatal(err)
	}

	// Both chunks' Speaker 1 clusters, each normalized, averaged.
	b, err := os.ReadFile(filepath.Join(e.paths.Voices(), "ana.json"))
	if err != nil {
		t.Fatal(err)
	}
	var prof struct {
		Name       string      `json:"name"`
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.Unmarshal(b, &prof); err != nil {
		t.Fatal(err)
	}
	if prof.Name != "Ana" || !reflect.DeepEqual(prof.Embeddings, [][]float32{{0.5, 0.5, 0}}) {
		t.Fatalf("profile = %+v", prof)
	}

	doc, err := Load(e.paths.DerivedHour(day, 13))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"me: hi all", "Ana: first point", "Speaker 2: second voice", "Ana: another point", "Speaker 1: after the meeting"}
	if got := speakers(doc.Transcript); !reflect.DeepEqual(got, want) {
		t.Fatalf("transcript = %q", got)
	}
	if doc.Status != store.StatusDone || len(doc.Screens) != 1 {
		t.Fatalf("doc changed beyond labels: %+v", doc)
	}

	// The index follows the new labels and keeps the screen text.
	hits, err := e.st.Search("first point", 10)
	if err != nil || len(hits) != 1 || hits[0].Text != "Ana: first point" || hits[0].Ref != "hour/2026-10-07/13#2" {
		t.Fatalf("transcript hits = %+v %v", hits, err)
	}
	if hits, err := e.st.Search("roadmap", 10); err != nil || len(hits) != 1 || hits[0].Kind != "ocr" {
		t.Fatalf("ocr hits = %+v %v", hits, err)
	}
}

func TestEnrollSpeakerWithoutRawAudio(t *testing.T) {
	e := newEnv(t)
	m := enrollFixture(e)
	if err := os.RemoveAll(e.paths.RawDay(day)); err != nil {
		t.Fatal(err)
	}
	err := e.p.EnrollSpeaker(context.Background(), m, "Speaker 1", "Ana")
	if !errors.Is(err, ErrNoRawAudio) {
		t.Fatalf("err = %v", err)
	}
	if names, _ := e.p.Voices.List(); len(names) != 0 {
		t.Fatalf("enrolled %v without audio", names)
	}
	doc, _ := Load(e.paths.DerivedHour(day, 13))
	if doc.Transcript[1].Speaker != "Speaker 1" {
		t.Fatalf("relabelled without enrolling: %q", speakers(doc.Transcript))
	}
}

func TestEnrollSpeakerVoiceNotFound(t *testing.T) {
	e := newEnv(t)
	m := enrollFixture(e)
	e.dz.by = map[string]diar{} // the diarizer finds no turns
	err := e.p.EnrollSpeaker(context.Background(), m, "Speaker 1", "Ana")
	if err == nil || errors.Is(err, ErrNoRawAudio) {
		t.Fatalf("err = %v", err)
	}
	if names, _ := e.p.Voices.List(); len(names) != 0 {
		t.Fatalf("enrolled %v", names)
	}
	doc, _ := Load(e.paths.DerivedHour(day, 13))
	if doc.Transcript[1].Speaker != "Speaker 1" {
		t.Fatalf("relabelled without enrolling: %q", speakers(doc.Transcript))
	}
}
