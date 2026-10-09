package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"workjournal/internal/activity"
	"workjournal/internal/diarize"
	"workjournal/internal/media"
	"workjournal/internal/store"
)

// voiceVec is an 8-dim voice on axis d plus a little noise, as one Diarize
// call would embed it.
func voiceVec(r *rand.Rand, d int) []float32 {
	v := make([]float32, 8)
	for i := range v {
		v[i] = 0.15 * (2*r.Float32() - 1)
	}
	v[d]++
	return v
}

// seg is one turn of a fixture voice: [from, to) seconds of a chunk.
type seg struct {
	from, to float64
	voice    byte // 'A'..'D'
	local    int  // the diarizer's chunk-local speaker id
}

// voiceChunks writes mic chunks whose diarizer output follows segs, with
// speech every 10 s (see talk) whose text is "<chunk>-<second>". It returns
// the voice behind each utterance text.
func voiceChunks(e *env, r *rand.Rand, chunks map[string][]seg) map[string]byte {
	axis := map[byte]int{'A': 0, 'B': 1, 'C': 2, 'D': 7}
	want := map[string]byte{}
	for name, segs := range chunks {
		e.file("audio/"+name+".m4a", old)
		e.tr.utts[name] = talk(name+"-", 0, 300)
		d := diar{embs: map[int][]float32{}}
		for _, s := range segs {
			d.turns = append(d.turns, tn(s.from, s.to, s.local))
			if _, ok := d.embs[s.local]; !ok {
				d.embs[s.local] = voiceVec(r, axis[s.voice])
			}
		}
		e.dz.by[name] = d
		for sec := 0; sec < 300; sec += 10 {
			from, to := float64(sec), float64(sec+7)
			best, bestOv := byte(0), 0.0
			for _, s := range segs {
				if ov := min(to, s.to) - max(from, s.from); ov > bestOv {
					best, bestOv = s.voice, ov
				}
			}
			want[fmt.Sprintf("%s-%d", name, sec)] = best
		}
	}
	return want
}

// overSplitFixture: a 20-minute in-person conversation in hour 14 (screen
// locked) between voices A, B and C over four mic chunks. The diarizer gives
// each chunk its own local ids, splits B in two in chunk 1405 (a 10 s
// sliver) and hears a 7 s voice D nobody else matches in chunk 1415.
func overSplitFixture(e *env) map[string]byte {
	e.p.Cfg.SpeakerMinSeconds = 20
	e.activity(old, ev(14, 0, "start"), ev(14, 0, "app", "loginwindow"), ev(14, 2, "idle"))
	return voiceChunks(e, rand.New(rand.NewSource(7)), map[string][]seg{
		"1400-mic": {{0, 100, 'A', 0}, {100, 200, 'B', 1}, {200, 300, 'A', 0}},
		"1405-mic": {{0, 150, 'C', 0}, {150, 290, 'B', 1}, {290, 300, 'B', 2}},
		"1410-mic": {{0, 100, 'A', 3}, {100, 300, 'C', 1}},
		"1415-mic": {{0, 150, 'B', 0}, {150, 293, 'C', 1}, {293, 300, 'D', 2}},
	})
}

func checkVoices(t *testing.T, doc *HourDoc, want map[string]byte, names map[byte]string) {
	t.Helper()
	got := map[string]string{}
	for _, u := range doc.Transcript {
		got[u.Text] = u.Speaker
	}
	for text, v := range want {
		if got[text] != names[v] {
			t.Errorf("%q (voice %c) labelled %q, want %q", text, v, got[text], names[v])
		}
	}
}

func TestMeetingClustersOverSplitVoices(t *testing.T) {
	e := newEnv(t)
	want := overSplitFixture(e)
	doc, err := e.p.Transcribe(context.Background(), day, 14)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Meetings) != 1 || doc.Meetings[0].App != activity.InPersonApp {
		t.Fatalf("meetings = %+v", doc.Meetings)
	}
	// In order of first appearance; B's sliver joins B; D is too short and
	// like nobody.
	checkVoices(t, doc, want, map[byte]string{'A': "Speaker 1", 'B': "Speaker 2", 'C': "Speaker 3", 'D': unknownSpeaker})
	labels := map[string]bool{}
	for _, u := range doc.Transcript {
		labels[u.Speaker] = true
	}
	if len(labels) != 4 {
		t.Errorf("labels = %v, want three voices and %q", labels, unknownSpeaker)
	}
}

func TestMeetingClustersNameKnownVoices(t *testing.T) {
	e := newEnv(t)
	want := overSplitFixture(e)
	if err := e.p.Voices.Enroll("me", []float32{1, 0, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if err := e.p.Voices.Enroll("Ana", []float32{0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	doc, err := e.p.Transcribe(context.Background(), day, 14)
	if err != nil {
		t.Fatal(err)
	}
	// Numbers stay positions in order of appearance, so naming a voice never
	// renumbers the others.
	checkVoices(t, doc, want, map[byte]string{'A': "me", 'B': "Ana", 'C': "Speaker 3", 'D': unknownSpeaker})
}

// A meeting running across an hour boundary is clustered over all its
// chunks, so a voice keeps its number in both hours.
func TestMeetingAcrossHoursKeepsNumbering(t *testing.T) {
	e := newEnv(t)
	e.p.Cfg.SpeakerMinSeconds = 20
	e.activity(old, ev(13, 50, "start"), ev(13, 50, "app", "loginwindow"), ev(13, 52, "idle"))
	want := voiceChunks(e, rand.New(rand.NewSource(3)), map[string][]seg{
		"1350-mic": {{0, 150, 'B', 0}, {150, 300, 'A', 1}},
		"1355-mic": {{0, 300, 'A', 0}},
		"1400-mic": {{0, 150, 'A', 0}, {150, 300, 'B', 1}},
		"1405-mic": {{0, 300, 'B', 2}},
	})
	names := map[byte]string{'A': "Speaker 2", 'B': "Speaker 1"}
	for _, h := range []int{13, 14} {
		doc, err := e.p.Transcribe(context.Background(), day, h)
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Meetings) != 1 || doc.Meetings[0].ID != "2026-10-07-1350-in-person" {
			t.Fatalf("hour %d meetings = %+v", h, doc.Meetings)
		}
		hw := map[string]byte{}
		for text, v := range want {
			if strings.HasPrefix(text, fmt.Sprint(h)) {
				hw[text] = v
			}
		}
		checkVoices(t, doc, hw, names)
	}
}

// The fake afconvert writes a WAV whose header NormalizeWav16k rejects for
// these chunks: whisper still transcribes them, only diarization is skipped
// (and counted as a failure).
func TestUnnormalizableWavStillTranscribed(t *testing.T) {
	e := newEnv(t)
	transcribeFixture(e)
	e.raw["1300-system.m4a"], e.raw["1305-system.m4a"] = true, true
	doc, err := e.p.Transcribe(context.Background(), day, 13)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.tr.calls, []string{"1300-mic", "1300-system", "1305-system"}) {
		t.Fatalf("transcribed %v", e.tr.calls)
	}
	if len(e.dz.calls) != 0 {
		t.Fatalf("diarized %v from WAVs it cannot read", e.dz.calls)
	}
	want := []string{"me: mic hello", "others: first voice", "others: second voice", "others: second again"}
	if got := speakers(doc.Transcript); !reflect.DeepEqual(got, want) {
		t.Fatalf("transcript = %q", got)
	}
	if w := warnings(e); len(w) != 1 || !strings.Contains(w[0], "2 of 2") {
		t.Fatalf("warnings = %q", w)
	}

	// Once the WAV is readable the chunks are diarized without transcribing
	// them again.
	e.resetCounts()
	delete(e.raw, "1300-system.m4a")
	delete(e.raw, "1305-system.m4a")
	again, err := e.p.Transcribe(context.Background(), day, 13)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.tr.calls) != 0 || len(e.dz.calls) != 2 {
		t.Fatalf("second pass: whisper %v, diarizer %v", e.tr.calls, e.dz.calls)
	}
	if got := speakers(again.Transcript); got[1] != "Speaker 1: first voice" {
		t.Fatalf("transcript = %q", got)
	}
}

// Participants are the voices in the meeting's transcript plus confirmed
// names: a label the skill invents, "Speaker ?" and stale unconfirmed rows
// are not.
func TestMeetingParticipantsOnlyRealVoices(t *testing.T) {
	e := newSumEnv(t)
	m := activity.Meeting{ID: "2026-10-07-1400-in-person", Start: at(14, 0, 0), End: at(14, 20, 0), App: activity.InPersonApp}
	for _, p := range []store.ParticipantRecord{
		{MeetingID: m.ID, Speaker: "Speaker 7"},                                              // stale
		{MeetingID: m.ID, Speaker: "Speaker 9", Name: "Bob", Confidence: 1, Confirmed: true}, // stale but confirmed
		{MeetingID: m.ID, Speaker: "Speaker 1", Name: "Old guess", Confidence: 0.4},
	} {
		if err := e.st.UpsertMeeting(store.MeetingRecord{ID: m.ID, Day: day, Start: m.Start, End: m.End, App: m.App}); err != nil {
			t.Fatal(err)
		}
		if err := e.st.SetParticipant(p); err != nil {
			t.Fatal(err)
		}
	}
	doc := &HourDoc{Day: day, Hour: 14,
		Segments: []activity.Segment{sg(activity.KindIdle, "loginwindow", "", 14, 0, 60)},
		Transcript: []media.Utterance{
			mic(1, 5, "me", "let's start"),
			mic(10, 20, "Speaker 1", "the budget is approved"),
			mic(30, 35, "Speaker 2", "great"),
			mic(50, 52, unknownSpeaker, "unclear words"),
		},
		Meetings:      []activity.Meeting{m},
		EndedMeetings: []string{m.ID},
	}
	e.transcribed(doc)
	e.fk.out["summarize-hour"] = hourOut
	e.fk.out["summarize-meeting"] = `{"participants":[{"speaker":"Speaker 1","name":"","confidence":0},` +
		`{"speaker":"Speaker 12","name":"Ghost","confidence":0.9},{"speaker":"Speaker ?","name":"","confidence":0}],` +
		`"summary":"Budget approved.","decisions":[],"action_items":[]}`
	if err := e.s.SummarizeHour(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	ps, err := e.st.Participants(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range ps {
		got = append(got, p.Speaker+"="+p.Name)
	}
	if want := []string{"Speaker 1=", "Speaker 2=", "Speaker 9=Bob", "You="}; !reflect.DeepEqual(got, want) {
		t.Fatalf("participants = %v, want %v", got, want)
	}
}

// Enrollment reads the meeting's cached diarization, and every chunk-local
// speaker under the label contributes, weighted by its overlap.
func TestEnrollUsesCachedTurnsOfEveryLocalSpeaker(t *testing.T) {
	e := newEnv(t)
	m := enrollFixture(e)
	e.dz.by["1300-system"] = diar{
		// "first point" (2–8 s) is split between local speakers 0 and 2.
		turns: []diarize.Turn{tn(0, 5, 0), tn(5, 9, 2), tn(9, 21, 1)},
		embs:  map[int][]float32{0: {2, 0, 0}, 1: {0, 0, 5}, 2: {0, 4, 0}},
	}
	// Cache both chunks' diarization as a pass over hour 13 would.
	for _, name := range []string{"1300-system", "1305-system"} {
		path := filepath.Join(e.paths.Audio(day), name+".m4a")
		key, err := statKey(path)
		if err != nil {
			t.Fatal(err)
		}
		d := e.dz.by[name]
		e.p.saveChunk(day, path, &chunkEntry{Version: cacheVersion, Key: key, Model: e.p.Cfg.WhisperModel, Diarized: true, Turns: d.turns, Embeddings: d.embs})
	}
	e.dz.errs["1300-system"] = errors.New("must not diarize again")
	e.dz.errs["1305-system"] = errors.New("must not diarize again")
	if err := e.p.EnrollSpeaker(context.Background(), m, "Speaker 1", "Ana"); err != nil {
		t.Fatal(err)
	}
	if len(e.dz.calls) != 0 {
		t.Fatalf("diarized %v despite the cache", e.dz.calls)
	}
	b, err := os.ReadFile(filepath.Join(e.paths.Voices(), "ana.json"))
	if err != nil {
		t.Fatal(err)
	}
	var prof struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.Unmarshal(b, &prof); err != nil {
		t.Fatal(err)
	}
	// 1300: 3 s of local 0 (x) and 3 s of local 2 (y) → (0.707, 0.707, 0);
	// 1305: y. Mean: (0.354, 0.854, 0).
	want := []float64{math.Sqrt2 / 4, (math.Sqrt2/2 + 1) / 2, 0}
	if len(prof.Embeddings) != 1 || len(prof.Embeddings[0]) != 3 {
		t.Fatalf("enrolled %v", prof.Embeddings)
	}
	for i, x := range prof.Embeddings[0] {
		if math.Abs(float64(x)-want[i]) > 1e-4 {
			t.Fatalf("enrolled %v, want %v", prof.Embeddings[0], want)
		}
	}
	if !slices.Contains(speakers(mustLoad(t, e, 13).Transcript), "Ana: first point") {
		t.Fatalf("not relabelled")
	}
}

func mustLoad(t *testing.T, e *env, hour int) *HourDoc {
	t.Helper()
	d, err := Load(e.paths.DerivedHour(day, hour))
	if err != nil {
		t.Fatal(err)
	}
	return d
}
