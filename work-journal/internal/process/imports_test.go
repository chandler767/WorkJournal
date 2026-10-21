package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"workjournal/internal/activity"
	"workjournal/internal/diarize"
	"workjournal/internal/store"
)

// sixMinWav is a canonical 16 kHz mono 16-bit PCM WAV header whose data
// chunk says 6 minutes of samples, as afconvert writes for a 6-minute memo.
// Only the header is written: Wav16kDuration reads the size from it.
func sixMinWav(path string) error {
	n := uint32(6 * 60 * 16000 * 2)
	h := []byte("RIFF\x00\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00\x80\x3e\x00\x00\x00\x7d\x00\x00\x02\x00\x10\x00data")
	le := []byte{byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)}
	h = append(h, le...)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(h); err != nil {
		return err
	}
	return f.Truncate(int64(len(h)) + int64(n))
}

// importEnv is newEnv whose afconvert writes a 6-minute WAV, with the
// summarize-meeting and privacy-filter skills faked.
func importEnv(t *testing.T) (*env, *fakeSkills) {
	t.Helper()
	e := newEnv(t)
	e.p.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		in, out := args[len(args)-2], args[len(args)-1]
		if e.bad[filepath.Base(in)] {
			return nil, errors.New("afconvert: cannot read " + in)
		}
		return nil, sixMinWav(out)
	}
	fk := &fakeSkills{out: map[string]string{
		"summarize-meeting": `{"participants":[],"summary":"Talked about speaker clustering.","decisions":[],"action_items":[]}`,
	}, errs: map[string]error{}}
	e.p.Summarize = &SkillSummarizer{Runner: fk, Store: e.st, Paths: e.paths, Cfg: e.p.Cfg, Logf: t.Logf}
	return e, fk
}

// memo writes a phone memo to import and returns its path.
func memo(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("not really audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// ny is the phone's zone in these tests, not the Mac's.
var ny = time.FixedZone("EDT", -4*3600)

func TestImportNoteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := ImportNote{Title: "AI Tinkerers meetup", Context: "Maya demoed.\nI care about clustering.",
		Start: time.Date(2026, 10, 7, 18, 30, 0, 0, ny), File: "audio.m4a", Original: "New Recording 3.m4a",
		Imported: time.Date(2026, 10, 7, 21, 0, 0, 0, time.UTC)}
	if err := WriteImportNote(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadImportNote(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Start.Equal(want.Start) || got.Start.Format(time.RFC3339) != "2026-10-07T18:30:00-04:00" {
		t.Errorf("start = %v, want %v on its own clock", got.Start, want.Start)
	}
	got.Start, got.Imported = want.Start, want.Imported
	if !reflect.DeepEqual(got, want) {
		t.Errorf("note = %+v, want %+v", got, want)
	}
}

func TestImportStoresFileAndNote(t *testing.T) {
	e, _ := importEnv(t)
	start := time.Date(2026, 10, 7, 18, 30, 0, 0, ny)
	src := memo(t, "New Recording 3.m4a")
	m, err := Import(e.paths, e.st, ImportRequest{Path: src, Start: start, Title: " Meetup ", Context: "who was there"}, at(21, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "2026-10-07-1830-imported" || m.Day != day || m.Title != "Meetup" || m.App != activity.InPersonApp || !m.Imported {
		t.Errorf("meeting = %+v", m)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("source removed without Move: %v", err)
	}
	dir := e.paths.Import(m.ID)
	if b, err := os.ReadFile(filepath.Join(dir, "audio.m4a")); err != nil || string(b) != "not really audio" {
		t.Errorf("stored audio = %q, %v", b, err)
	}
	n, err := ReadImportNote(dir)
	if err != nil || n.Title != "Meetup" || n.Context != "who was there" || n.Original != "New Recording 3.m4a" || !n.Start.Equal(start) {
		t.Errorf("note = %+v, %v", n, err)
	}
	if r, ok, err := e.st.Import(m.ID); err != nil || !ok || r.Status != store.StatusPending {
		t.Errorf("import record = %+v %v %v", r, ok, err)
	}
	// A second recording at the same minute gets its own id and folder.
	m2, err := Import(e.paths, e.st, ImportRequest{Path: memo(t, "b.mp3"), Start: start, Move: true}, at(21, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if m2.ID != "2026-10-07-1830-imported-2" || m2.Title != importTitle {
		t.Errorf("second meeting = %+v", m2)
	}
}

func TestImportRejects(t *testing.T) {
	e, _ := importEnv(t)
	start := time.Date(2026, 10, 7, 18, 30, 0, 0, ny)
	for name, req := range map[string]ImportRequest{
		"video":          {Path: memo(t, "clip.mov"), Start: start},
		"no extension":   {Path: memo(t, "memo"), Start: start},
		"no start":       {Path: memo(t, "a.m4a")},
		"missing file":   {Path: filepath.Join(t.TempDir(), "gone.m4a"), Start: start},
		"title too long": {Path: memo(t, "a.m4a"), Start: start, Title: strings.Repeat("x", MaxImportTitle+1)},
	} {
		if _, err := Import(e.paths, e.st, req, at(21, 0, 0)); err == nil {
			t.Errorf("%s: imported", name)
		}
	}
	if ents, _ := os.ReadDir(e.paths.Imports()); len(ents) != 0 {
		t.Errorf("rejected imports left %d entries", len(ents))
	}
	if ms, _ := e.st.MeetingsOn(day); len(ms) != 0 {
		t.Errorf("rejected imports left meetings %+v", ms)
	}
	for _, ok := range []string{"a.M4A", "b.mp3", "c.wav", "d.caf"} {
		if err := CheckImportName(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
}

func TestImportedFilesAreNotChunks(t *testing.T) {
	e, _ := importEnv(t)
	e.activity(old, ev(13, 0, "start"))
	e.file("audio/1300-mic.m4a", old)
	if _, err := Import(e.paths, e.st, ImportRequest{Path: memo(t, "1305-mic.m4a"), Start: at(13, 5, 0)}, at(15, 0, 0)); err != nil {
		t.Fatal(err)
	}
	cs, err := e.p.chunks(day)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || filepath.Base(cs[0].path) != "1300-mic.m4a" {
		t.Errorf("chunks = %+v, want only the recorder's", cs)
	}
	hours, err := e.p.PendingHours()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(hours, []HourKey{{day, 13}}) {
		t.Errorf("pending hours = %v", hours)
	}
}

func TestProcessImportMakesMeetingOverRecording(t *testing.T) {
	e, fk := importEnv(t)
	start := time.Date(2026, 10, 7, 18, 30, 0, 0, ny)
	m, err := Import(e.paths, e.st, ImportRequest{Path: memo(t, "memo.m4a"), Start: start,
		Title: "AI Tinkerers meetup", Context: "Maya demoed. I care about clustering."}, at(21, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	e.tr.utts["audio"] = []fu{{40, 44, "the demo tonight is local transcription"}, {130, 133, "does diarization run on device"}}
	e.dz.by["audio"] = diar{turns: []diarize.Turn{tn(40, 44, 0), tn(130, 133, 1)}, embs: map[int][]float32{0: {1, 0}, 1: {0, 1}}}
	if err := e.p.ProcessImports(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, ok, err := e.st.Meeting(m.ID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if !got.Start.Equal(start) || !got.End.Equal(start.Add(6*time.Minute)) {
		t.Errorf("meeting %v-%v, want %v plus 6 minutes", got.Start, got.End, start)
	}
	if got.Start.Format("15:04 -0700") != "18:30 -0400" {
		t.Errorf("start %v not on the recording's clock", got.Start)
	}
	if !got.Imported || got.ImportStatus != store.StatusDone || got.ImportError != "" || got.SummaryJSON == "" {
		t.Errorf("meeting = %+v", got)
	}
	if ms, _ := e.st.MeetingsOn(day); len(ms) != 1 || ms[0].ID != m.ID {
		t.Errorf("meetings on %s = %+v", day, ms)
	}

	docs := MeetingDocs(e.paths, got)
	if len(docs) != 1 || len(docs[0].Transcript) != 2 {
		t.Fatalf("docs = %+v", docs)
	}
	speakers := []string{docs[0].Transcript[0].Speaker, docs[0].Transcript[1].Speaker}
	if !reflect.DeepEqual(speakers, []string{"Speaker 1", "Speaker 2"}) {
		t.Errorf("speakers = %v", speakers)
	}
	if u := docs[0].Transcript[1]; !u.Start.Equal(start.Add(130 * time.Second)) {
		t.Errorf("utterance at %v", u.Start)
	}

	// summarize-meeting gets the note as the user's own.
	if len(fk.calls) != 1 || fk.calls[0].skill != "summarize-meeting" {
		t.Fatalf("skills = %v", fk.skillsCalled())
	}
	in := fk.calls[0].input
	note, _ := in["user_note"].(map[string]any)
	if note["title"] != "AI Tinkerers meetup" || !reflect.DeepEqual(note["context_lines"], []any{"Maya demoed. I care about clustering."}) {
		t.Errorf("user_note = %v", in["user_note"])
	}
	if meet := in["meeting"].(map[string]any); meet["end"] != "2026-10-07T18:36:00-04:00" || meet["app"] != activity.InPersonApp {
		t.Errorf("meeting input = %v", meet)
	}
	if tr := in["transcript"].([]any); len(tr) != 2 {
		t.Errorf("transcript input = %v", tr)
	}

	hits, err := e.st.Search("diarization", 10)
	if err != nil || len(hits) != 1 || hits[0].Ref != "meeting/"+m.ID {
		t.Errorf("transcript search = %+v, %v", hits, err)
	}
	if hits, _ := e.st.Search("clustering", 10); len(hits) != 1 || hits[0].Kind != "meeting" {
		t.Errorf("summary search = %+v", hits)
	}
	// Hour processing of the same hour keeps the import's index entries
	// and its meeting.
	if err := e.st.ReplaceText(day, 18, nil); err != nil {
		t.Fatal(err)
	}
	if hits, _ := e.st.Search("diarization", 10); len(hits) != 1 {
		t.Errorf("hour reindex dropped the import's text: %+v", hits)
	}

	// Processed imports are not run again.
	fk.calls = nil
	if err := e.p.ProcessImports(context.Background()); err != nil || len(fk.calls) != 0 {
		t.Errorf("second run: %v, skills %v", err, fk.skillsCalled())
	}
}

func TestProcessImportFailureKeepsRecording(t *testing.T) {
	e, fk := importEnv(t)
	m, err := Import(e.paths, e.st, ImportRequest{Path: memo(t, "memo.m4a"), Start: at(9, 0, 0)}, at(10, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	e.tr.errs["audio"] = errors.New("whisper crashed")
	if err := e.p.ProcessImports(context.Background()); err == nil {
		t.Fatal("no error")
	}
	got, _, _ := e.st.Meeting(m.ID)
	if got.ImportStatus != store.StatusPending || !strings.Contains(got.ImportError, "whisper crashed") {
		t.Errorf("meeting = %+v", got)
	}
	if _, err := os.Stat(filepath.Join(e.paths.Import(m.ID), "audio.m4a")); err != nil {
		t.Errorf("recording dropped: %v", err)
	}
	// Not retried within the hour...
	delete(e.tr.errs, "audio")
	if err := e.p.ProcessImports(context.Background()); err != nil || len(fk.calls) != 0 {
		t.Errorf("retried at once: %v %v", err, fk.skillsCalled())
	}
	// ...but after it, and the error clears.
	e.p.Now = func() time.Time { return at(16, 30, 0) }
	if err := e.p.ProcessImports(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _, _ = e.st.Meeting(m.ID)
	if got.ImportStatus != store.StatusDone || got.ImportError != "" {
		t.Errorf("after retry = %+v", got)
	}
}

func TestProcessImportUnreadableNeedsReview(t *testing.T) {
	e, _ := importEnv(t)
	m, err := Import(e.paths, e.st, ImportRequest{Path: memo(t, "memo.caf"), Start: at(9, 0, 0)}, at(10, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	e.bad["audio.caf"] = true
	if err := e.p.ProcessImports(context.Background()); err == nil {
		t.Fatal("no error")
	}
	got, _, _ := e.st.Meeting(m.ID)
	if got.ImportStatus != store.StatusNeedsReview || !strings.Contains(got.ImportError, "cannot read the audio") {
		t.Errorf("meeting = %+v", got)
	}
	if _, err := os.Stat(filepath.Join(e.paths.Import(m.ID), "audio.caf")); err != nil {
		t.Errorf("recording dropped: %v", err)
	}
}

func TestProcessImportRunsPrivacyFilter(t *testing.T) {
	e, fk := importEnv(t)
	e.p.Cfg.Diarization = "off"
	r := &privacyRunner{respond: dropping("bystander", 0.9, []string{"the table next to us"},
		map[string]string{"my card is 4417": "my card is [redacted]"})}
	e.p.Privacy = r
	m, err := Import(e.paths, e.st, ImportRequest{Path: memo(t, "memo.m4a"), Start: at(9, 58, 0)}, at(11, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	// Speech on both sides of 10:00 is filtered one hour at a time.
	e.tr.utts["audio"] = []fu{{10, 12, "kickoff"}, {60, 62, "the table next to us"}, {200, 203, "my card is 4417"}}
	if err := e.p.ProcessImports(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.inputs) != 2 || r.inputs[0].Hour != 9 || r.inputs[1].Hour != 10 {
		t.Errorf("privacy inputs = %+v", r.inputs)
	}
	got, _, _ := e.st.Meeting(m.ID)
	docs := MeetingDocs(e.paths, got)
	if len(docs) != 1 {
		t.Fatal(docs)
	}
	if texts := transcriptTexts(docs[0]); !reflect.DeepEqual(texts, []string{"kickoff", "my card is [redacted]"}) {
		t.Errorf("transcript = %v", texts)
	}
	if len(docs[0].Removed) != 1 {
		t.Errorf("removed = %+v", docs[0].Removed)
	}
	if tr := fk.calls[0].input["transcript"].([]any); len(tr) != 2 {
		t.Errorf("summary input = %v", tr)
	}
	if docs[0].Transcript[0].Speaker != "me" {
		t.Errorf("undiarized speech labelled %q", docs[0].Transcript[0].Speaker)
	}
}

func TestHourProcessingKeepsImportedMeeting(t *testing.T) {
	e, _ := importEnv(t)
	e.p.Cfg.Diarization = "off"
	e.activity(old, ev(13, 0, "start"), ev(13, 0, "app", "Code", "main.go"))
	e.file("audio/1300-mic.m4a", old)
	m, err := Import(e.paths, e.st, ImportRequest{Path: memo(t, "memo.m4a"), Start: at(13, 10, 0)}, at(14, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.p.ProcessImports(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.p.Transcribe(context.Background(), day, 13); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := e.st.Meeting(m.ID); !ok {
		t.Fatal("hour processing deleted the imported meeting")
	}
}
