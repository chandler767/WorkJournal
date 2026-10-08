package process

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"workjournal/internal/config"
	"workjournal/internal/diarize"
	"workjournal/internal/media"
	"workjournal/internal/store"
)

const day = "2026-10-07"

func at(h, m, s int) time.Time { return time.Date(2026, 10, 7, h, m, s, 0, time.Local) }

// fake utterance spec: offsets in seconds from chunk start.
type fu struct {
	from, to float64
	text     string
}

type fakeTranscriber struct {
	utts  map[string][]fu // by chunk base name, e.g. "1300-mic"
	errs  map[string]error
	calls []string
	wavs  []string
}

func (f *fakeTranscriber) Transcribe(ctx context.Context, wav string, start time.Time, track string) ([]media.Utterance, error) {
	if _, err := os.Stat(wav); err != nil {
		return nil, fmt.Errorf("fake transcriber: wav missing: %w", err)
	}
	base := strings.TrimSuffix(filepath.Base(wav), ".wav")
	f.calls = append(f.calls, base)
	f.wavs = append(f.wavs, wav)
	if err := f.errs[base]; err != nil {
		return nil, err
	}
	var out []media.Utterance
	for _, u := range f.utts[base] {
		sp := ""
		if track == "mic" {
			sp = "me"
		}
		out = append(out, media.Utterance{
			Start: start.Add(time.Duration(u.from * float64(time.Second))),
			End:   start.Add(time.Duration(u.to * float64(time.Second))),
			Track: track, Speaker: sp, Text: u.text,
		})
	}
	return out, nil
}

type diar struct {
	turns []diarize.Turn
	embs  map[int][]float32
}

type fakeDiarizer struct {
	by    map[string]diar
	calls []string
}

func (f *fakeDiarizer) Diarize(ctx context.Context, wav string) ([]diarize.Turn, map[int][]float32, error) {
	base := strings.TrimSuffix(filepath.Base(wav), ".wav")
	f.calls = append(f.calls, base)
	d := f.by[base]
	return d.turns, d.embs, nil
}

type fakeOCR struct {
	texts map[string]string // by file base, e.g. "131000.jpg"
	files []string
}

func (f *fakeOCR) OCR(ctx context.Context, files []string) ([]media.ScreenText, error) {
	var out []media.ScreenText
	for _, file := range files {
		f.files = append(f.files, filepath.Base(file))
		text := f.texts[filepath.Base(file)]
		if text == "" {
			continue
		}
		ts, err := time.ParseInLocation("2006-01-02 150405", day+" "+strings.TrimSuffix(filepath.Base(file), ".jpg"), time.Local)
		if err != nil {
			return nil, err
		}
		out = append(out, media.ScreenText{Time: ts, File: file, Text: text})
	}
	return out, nil
}

type fakeSummarizer struct {
	calls []HourKey
	err   error
}

func (f *fakeSummarizer) SummarizeHour(ctx context.Context, doc *HourDoc) error {
	f.calls = append(f.calls, HourKey{doc.Day, doc.Hour})
	return f.err
}

type env struct {
	t     *testing.T
	p     *Pipeline
	paths config.Paths
	st    *store.Store
	tr    *fakeTranscriber
	dz    *fakeDiarizer
	ocr   *fakeOCR
	sum   *fakeSummarizer
	bad   map[string]bool // chunk names afconvert fails on
	mu    sync.Mutex
	logs  []string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	paths := config.Paths{Root: t.TempDir()}
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.DB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Default()
	cfg.DataDir = paths.Root
	cfg.Diarization = "sherpa" // the shipped default; set explicitly because these tests exercise diarization
	e := &env{
		t: t, paths: paths, st: st,
		tr:  &fakeTranscriber{utts: map[string][]fu{}, errs: map[string]error{}},
		dz:  &fakeDiarizer{by: map[string]diar{}},
		ocr: &fakeOCR{texts: map[string]string{}},
		sum: &fakeSummarizer{},
		bad: map[string]bool{},
	}
	e.p = &Pipeline{
		Cfg: cfg, Paths: paths, Store: st,
		Transcriber: e.tr, OCR: e.ocr, Diarizer: e.dz,
		Voices:    &diarize.Voices{Dir: paths.Voices(), Threshold: cfg.VoiceMatchThreshold},
		Run:       e.run,
		Now:       func() time.Time { return at(15, 10, 0) },
		Summarize: e.sum,
		Logf: func(format string, args ...any) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.logs = append(e.logs, fmt.Sprintf(format, args...))
		},
	}
	return e
}

// run fakes afconvert: it writes the output file unless the input is bad.
func (e *env) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name != "afconvert" {
		return nil, fmt.Errorf("unexpected command %s", name)
	}
	in, out := args[len(args)-2], args[len(args)-1]
	if e.bad[filepath.Base(in)] {
		return nil, fmt.Errorf("afconvert: cannot read %s", in)
	}
	return nil, os.WriteFile(out, []byte("RIFF"), 0o644)
}

// file creates an empty raw file under raw/<day>/ with the given mtime.
func (e *env) file(rel string, mtime time.Time) {
	e.t.Helper()
	path := filepath.Join(e.paths.RawDay(day), rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		e.t.Fatal(err)
	}
}

// ev is one activity event at h:m on day.
func ev(h, m int, typ string, appTitle ...string) string {
	return evAt(at(h, m, 0), typ, appTitle...)
}

// evAt is one activity event at t.
func evAt(t time.Time, typ string, appTitle ...string) string {
	r := map[string]string{"ts": t.Format(time.RFC3339), "type": typ}
	if len(appTitle) > 0 {
		r["app"] = appTitle[0]
	}
	if len(appTitle) > 1 {
		r["title"] = appTitle[1]
	}
	b, _ := json.Marshal(r)
	return string(b)
}

func (e *env) activity(mtime time.Time, lines ...string) {
	e.t.Helper()
	e.activityOn(day, mtime, lines...)
}

func (e *env) activityOn(d string, mtime time.Time, lines ...string) {
	e.t.Helper()
	path := e.paths.Activity(d)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) hourStatus(hour int) (store.HourRecord, bool) {
	e.t.Helper()
	h, ok, err := e.st.Hour(day, hour)
	if err != nil {
		e.t.Fatal(err)
	}
	return h, ok
}

func tn(start, end float64, speaker int) diarize.Turn {
	return diarize.Turn{Start: start, End: end, Speaker: speaker}
}
