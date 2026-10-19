package media

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type call struct {
	name string
	args []string
}

func TestToWav16kArgs(t *testing.T) {
	var got call
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		got = call{name, args}
		return nil, os.WriteFile(args[len(args)-1], canonicalHeader(0), 0o644)
	}
	out := filepath.Join(t.TempDir(), "out.wav")
	if err := ToWav16k(context.Background(), run, "in.m4a", out); err != nil {
		t.Fatal(err)
	}
	want := []string{"-f", "WAVE", "-d", "LEI16@16000", "-c", "1", "in.m4a", out}
	if got.name != "afconvert" || !reflect.DeepEqual(got.args, want) {
		t.Fatalf("got %s %v", got.name, got.args)
	}
}

// ConvertToWav16k only runs afconvert; normalizing the header is a separate
// step (NormalizeWav16k) whose failure must not cost the transcript.
func TestConvertToWav16kDoesNotNormalize(t *testing.T) {
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, os.WriteFile(args[len(args)-1], []byte("not a wav"), 0o644)
	}
	out := filepath.Join(t.TempDir(), "out.wav")
	if err := ConvertToWav16k(context.Background(), run, "in.m4a", out); err != nil {
		t.Fatalf("convert: %v", err)
	}
	if err := ToWav16k(context.Background(), run, "in.m4a", out); err == nil {
		t.Fatal("ToWav16k accepted a WAV it cannot normalize")
	}
	if b, _ := os.ReadFile(out); string(b) != "not a wav" {
		t.Fatalf("output changed: %q", b)
	}
}

func whisperRunner(t *testing.T, calls *[]call) Runner {
	fixture, err := os.ReadFile("testdata/whisper.json")
	if err != nil {
		t.Fatal(err)
	}
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, call{name, args})
		for i, a := range args {
			if a == "-of" {
				return nil, os.WriteFile(args[i+1]+".json", fixture, 0o644)
			}
		}
		return nil, fmt.Errorf("no -of")
	}
}

func TestWhisperParsesOffsetsAndDropsBlank(t *testing.T) {
	var calls []call
	w := Whisper{Run: whisperRunner(t, &calls), Bin: "whisper-cli", Model: "m.bin"}
	wav := t.TempDir() + "/0900-system.wav"
	start := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	us, err := w.Transcribe(context.Background(), wav, start, "system")
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"-m", "m.bin", "-f", wav, "-oj", "-of", strings.TrimSuffix(wav, ".wav"), "-np"}
	if calls[0].name != "whisper-cli" || !reflect.DeepEqual(calls[0].args, wantArgs) {
		t.Fatalf("got %v", calls[0])
	}
	if len(us) != 2 {
		t.Fatalf("want 2 utterances, got %+v", us)
	}
	if us[0].Text != "Hello." || !us[0].Start.Equal(start) || !us[0].End.Equal(start.Add(2*time.Second)) {
		t.Errorf("u0 = %+v", us[0])
	}
	if us[1].Text != "Let's ship the release today." ||
		!us[1].Start.Equal(start.Add(60250*time.Millisecond)) || !us[1].End.Equal(start.Add(64750*time.Millisecond)) {
		t.Errorf("u1 = %+v", us[1])
	}
	if us[0].Track != "system" || us[0].Speaker != "" {
		t.Errorf("track/speaker = %q/%q", us[0].Track, us[0].Speaker)
	}
}

func TestWhisperMicIsMe(t *testing.T) {
	var calls []call
	w := Whisper{Run: whisperRunner(t, &calls), Bin: "whisper-cli", Model: "m.bin"}
	us, err := w.Transcribe(context.Background(), t.TempDir()+"/a.wav", time.Now(), "mic")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range us {
		if u.Speaker != "me" || u.Track != "mic" {
			t.Errorf("%+v", u)
		}
	}
}

func TestWhisperRunnerErrorPropagates(t *testing.T) {
	w := Whisper{Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("boom")
	}, Bin: "whisper-cli"}
	if _, err := w.Transcribe(context.Background(), t.TempDir()+"/a.wav", time.Now(), "mic"); err == nil {
		t.Fatal("want error")
	}
}

func TestOCRBatchesAndDedups(t *testing.T) {
	var files []string
	for i := 0; i < 120; i++ {
		files = append(files, fmt.Sprintf("/d/raw/2026-10-07/screens/%02d%02d00.jpg", 10+i/60, i%60))
	}
	var calls []call
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, call{name, args})
		type e struct {
			File string `json:"file"`
			Text string `json:"text"`
		}
		var out []e
		for _, f := range args[1:] {
			out = append(out, e{f, "same"})
		}
		return json.Marshal(out)
	}
	o := RecorderOCR{Run: run, Bin: "recorder"}
	st, err := o.OCR(context.Background(), files)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 {
		t.Fatalf("want 3 calls, got %d", len(calls))
	}
	if calls[0].args[0] != "ocr" || len(calls[0].args) != 51 || len(calls[2].args) != 21 {
		t.Errorf("args lens %d %d", len(calls[0].args), len(calls[2].args))
	}
	if len(st) != 1 || st[0].File != files[0] {
		t.Fatalf("want 1 deduped entry, got %d", len(st))
	}
}

func TestOCRTimeFromFilename(t *testing.T) {
	fixture, err := os.ReadFile("testdata/ocr.json")
	if err != nil {
		t.Fatal(err)
	}
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) { return fixture, nil }
	o := RecorderOCR{Run: run, Bin: "recorder"}
	st, err := o.OCR(context.Background(), []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(st) != 2 {
		t.Fatalf("want 2 (dedup, empty dropped, bogus skipped), got %+v", st)
	}
	want := time.Date(2026, 10, 7, 10, 15, 0, 0, time.Local)
	if !st[0].Time.Equal(want) || st[0].Text != "Terminal\n$ go test" {
		t.Errorf("st0 = %+v", st[0])
	}
	if st[1].Text != "Slack" || !st[1].Time.Equal(want.Add(30*time.Second)) {
		t.Errorf("st1 = %+v", st[1])
	}
}

func TestParseChunkName(t *testing.T) {
	cases := []struct {
		name       string
		hh, mm, ss int
		track      string
		ok         bool
	}{
		{"0905-system.m4a", 9, 5, 0, "system", true},
		{"2359-mic.m4a", 23, 59, 0, "mic", true},
		{"090507-mic.m4a", 9, 5, 7, "mic", true},
		{"090507-system.m4a", 9, 5, 7, "system", true},
		{"2405-mic.m4a", 0, 0, 0, "", false},
		{"0960-mic.m4a", 0, 0, 0, "", false},
		{"0905-other.m4a", 0, 0, 0, "", false},
		{"0905-mic.wav", 0, 0, 0, "", false},
		{"905-mic.m4a", 0, 0, 0, "", false},
		{"090561-mic.m4a", 0, 0, 0, "", false},
		{"ab05-mic.m4a", 0, 0, 0, "", false},
		// Collision suffix (the recorder's third choice when HHMM and HHMMSS are taken).
		{"090507-1-mic.m4a", 9, 5, 7, "mic", true},
		{"090507-12-system.m4a", 9, 5, 7, "system", true},
		{"0905-1-mic.m4a", 0, 0, 0, "", false},
		{"090507--mic.m4a", 0, 0, 0, "", false},
		{"090507-x-mic.m4a", 0, 0, 0, "", false},
		{"090507-1234-mic.m4a", 0, 0, 0, "", false},
	}
	for _, c := range cases {
		hh, mm, ss, tr, ok := ParseChunkName(c.name)
		if hh != c.hh || mm != c.mm || ss != c.ss || tr != c.track || ok != c.ok {
			t.Errorf("%s: got %d %d %d %q %v", c.name, hh, mm, ss, tr, ok)
		}
	}
}

func TestChunkStart(t *testing.T) {
	loc := time.FixedZone("x", 3600)
	ts, tr, err := ChunkStart("2026-10-07", "090507-mic.m4a", loc)
	if err != nil || tr != "mic" || !ts.Equal(time.Date(2026, 10, 7, 9, 5, 7, 0, loc)) {
		t.Fatalf("%v %q %v", ts, tr, err)
	}
	if _, _, err := ChunkStart("2026-10-07", "bad.m4a", loc); err == nil {
		t.Error("want error for bad name")
	}
	if _, _, err := ChunkStart("nope", "0905-mic.m4a", loc); err == nil {
		t.Error("want error for bad day")
	}
}

func TestExecRunnerIncludesStderr(t *testing.T) {
	_, err := ExecRunner(context.Background(), "sh", "-c", "echo oops >&2; exit 3")
	if err == nil || !strings.Contains(err.Error(), "sh") || !strings.Contains(err.Error(), "oops") {
		t.Fatalf("err = %v", err)
	}
	out, err := ExecRunner(context.Background(), "echo", "hi")
	if err != nil || strings.TrimSpace(string(out)) != "hi" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestOCRFilesKeepsEveryText(t *testing.T) {
	files := []string{"/d/raw/2026-10-07/screens/100000.jpg", "/d/raw/2026-10-07/screens/100010.jpg", "/d/raw/2026-10-07/screens/100020.jpg"}
	texts := map[string]string{files[0]: " same ", files[1]: "same", files[2]: ""}
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		type e struct {
			File string `json:"file"`
			Text string `json:"text"`
		}
		var out []e
		for _, f := range args[1:] {
			out = append(out, e{f, texts[f]})
		}
		return json.Marshal(out)
	}
	var o FileOCR = RecorderOCR{Run: run, Bin: "recorder", Batch: 2}
	got, err := o.OCRFiles(context.Background(), files)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{files[0]: "same", files[1]: "same", files[2]: ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OCRFiles = %v, want %v", got, want)
	}
}

func TestWhisperDropsNonSpeechTags(t *testing.T) {
	texts := []string{"[INAUDIBLE]", "[MUSIC]", "(silence)", " [ Silence ] ", "*laughs*", "[MUSIC] (applause)",
		"Ok.", "(laughs) yes, ship it", "Hi", "[sic] that's right", "5 * 3 is *fifteen*", "*", "()"}
	type seg struct {
		Offsets struct {
			From int64 `json:"from"`
			To   int64 `json:"to"`
		} `json:"offsets"`
		Text string `json:"text"`
	}
	var out struct {
		Transcription []seg `json:"transcription"`
	}
	for _, s := range texts {
		out.Transcription = append(out.Transcription, seg{Text: s})
	}
	body, _ := json.Marshal(out)
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		for i, a := range args {
			if a == "-of" {
				return nil, os.WriteFile(args[i+1]+".json", body, 0o644)
			}
		}
		return nil, fmt.Errorf("no -of")
	}
	us, err := Whisper{Run: run, Bin: "whisper-cli"}.Transcribe(context.Background(), t.TempDir()+"/a.wav", time.Now(), "mic")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, u := range us {
		got = append(got, u.Text)
	}
	want := []string{"Ok.", "(laughs) yes, ship it", "Hi", "[sic] that's right", "5 * 3 is *fifteen*"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("kept %q, want %q", got, want)
	}
}

func TestDropHallucinations(t *testing.T) {
	base := time.Date(2026, 10, 8, 14, 0, 0, 0, time.UTC)
	u := func(from, to float64, text string) Utterance {
		return Utterance{Start: base.Add(time.Duration(from * float64(time.Second))), End: base.Add(time.Duration(to * float64(time.Second))), Track: "system", Text: text}
	}
	in := []Utterance{
		u(0, 2, "You"),                       // isolated: dropped
		u(10, 12, " Thank you. "),            // isolated: dropped
		u(20, 22, "Thanks for watching!"),    // isolated: dropped
		u(30, 31, "you"),                     // two candidates near each other, no real speech: both dropped
		u(32, 33, "Bye."),                    //
		u(40, 45, "So the plan is to ship."), // real speech kept
		u(47, 48, "You"),                     // within 3 s of real speech: kept
		u(60, 62, "Thank you very much."),    // not a whole-text match: kept
		u(70, 71, "Please subscribe"),        // isolated: dropped
		u(80, 81, "thank you for watching"),  // isolated: dropped
		u(100, 104, "Okay, next item."),      // real speech
		u(90, 97, "you"),                     // ends 3 s before real speech (out of order input): kept
	}
	got := DropHallucinations(in)
	var texts []string
	for _, g := range got {
		texts = append(texts, g.Text)
	}
	want := []string{"So the plan is to ship.", "You", "Thank you very much.", "Okay, next item.", "you"}
	if !reflect.DeepEqual(texts, want) {
		t.Fatalf("kept %q, want %q", texts, want)
	}
}

func TestDropHallucinationsConsidersBothTracks(t *testing.T) {
	base := time.Date(2026, 10, 8, 14, 0, 0, 0, time.UTC)
	u := func(from, to float64, track, text string) Utterance {
		return Utterance{Start: base.Add(time.Duration(from * float64(time.Second))), End: base.Add(time.Duration(to * float64(time.Second))), Track: track, Text: text}
	}
	in := []Utterance{
		u(0, 5, "system", "Here is the final number."),
		u(6, 7, "mic", "Thank you."), // the other side just spoke: real
		u(30, 31, "mic", "Bye."),     // nothing on either track: hallucination
		u(50, 52, "system", "you"),   // isolated on both tracks
		u(60, 64, "mic", "See you then."),
		u(65, 66, "system", "Bye."), // reply to the mic: real
	}
	var got []string
	for _, k := range DropHallucinations(in) {
		got = append(got, k.Track+":"+k.Text)
	}
	want := []string{"system:Here is the final number.", "mic:Thank you.", "mic:See you then.", "system:Bye."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("kept %q, want %q", got, want)
	}
}

// Whisper keeps silence phrases: whether one is isolated depends on the other
// track too, so the pipeline filters the whole hour (DropHallucinations).
func TestWhisperKeepsSilencePhrasesForHourFilter(t *testing.T) {
	type seg struct {
		Offsets struct {
			From int64 `json:"from"`
			To   int64 `json:"to"`
		} `json:"offsets"`
		Text string `json:"text"`
	}
	var out struct {
		Transcription []seg `json:"transcription"`
	}
	add := func(from, to int64, text string) {
		var s seg
		s.Offsets.From, s.Offsets.To, s.Text = from, to, text
		out.Transcription = append(out.Transcription, s)
	}
	add(0, 2000, " You")
	add(30000, 35000, " Let's review the doc.")
	add(36000, 37000, " Thank you.")
	add(120000, 121000, " you")
	body, _ := json.Marshal(out)
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		for i, a := range args {
			if a == "-of" {
				return nil, os.WriteFile(args[i+1]+".json", body, 0o644)
			}
		}
		return nil, fmt.Errorf("no -of")
	}
	us, err := Whisper{Run: run, Bin: "whisper-cli"}.Transcribe(context.Background(), t.TempDir()+"/a.wav", time.Now(), "system")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, u := range us {
		got = append(got, u.Text)
	}
	want := []string{"You", "Let's review the doc.", "Thank you.", "you"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("kept %q, want %q", got, want)
	}
}

// A screenshot's HHMMSS name is the wall clock where it was taken: the
// day's activity log gives that zone, whatever the Mac's zone is now.
func TestOCRTimeInRecordingZone(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	prev := time.Local
	time.Local = ny
	t.Cleanup(func() { time.Local = prev })
	dayDir := filepath.Join(t.TempDir(), "2026-10-07")
	if err := os.MkdirAll(filepath.Join(dayDir, "screens"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dayDir, "activity.jsonl"), []byte(`{"ts":"2026-10-07T09:00:00+01:00","type":"start"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dayDir, "screens", "101500.jpg")
	out, _ := json.Marshal([]map[string]string{{"file": file, "text": "Terminal"}})
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) { return out, nil }
	st, err := RecorderOCR{Run: run, Bin: "recorder"}.OCR(context.Background(), []string{file})
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 7, 10, 15, 0, 0, time.FixedZone("", 3600)); len(st) != 1 || !st[0].Time.Equal(want) {
		t.Fatalf("got %+v, want 10:15+01:00", st)
	}
}
