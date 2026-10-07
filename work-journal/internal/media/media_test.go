package media

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
		return nil, nil
	}
	if err := ToWav16k(context.Background(), run, "in.m4a", "out.wav"); err != nil {
		t.Fatal(err)
	}
	want := []string{"-f", "WAVE", "-d", "LEI16@16000", "-c", "1", "in.m4a", "out.wav"}
	if got.name != "afconvert" || !reflect.DeepEqual(got.args, want) {
		t.Fatalf("got %s %v", got.name, got.args)
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
