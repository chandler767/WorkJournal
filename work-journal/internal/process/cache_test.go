package process

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// resetCounts forgets the fakes' recorded calls.
func (e *env) resetCounts() {
	e.tr.calls, e.tr.wavs, e.dz.calls, e.ocr.files = nil, nil, nil, nil
}

func TestTranscribeCacheHitSkipsWhisperDiarizerAndOCR(t *testing.T) {
	e := newEnv(t)
	transcribeFixture(e)
	ctx := context.Background()
	first, err := e.p.Transcribe(ctx, day, 13)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.dz.calls) != 2 || len(e.ocr.files) != 1 {
		t.Fatalf("first pass: diarized %v, ocr %v", e.dz.calls, e.ocr.files)
	}
	e.resetCounts()

	second, err := e.p.Transcribe(ctx, day, 13)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.tr.calls) != 0 || len(e.dz.calls) != 0 || len(e.ocr.files) != 0 {
		t.Fatalf("cache miss: whisper %v, diarizer %v, ocr %v", e.tr.calls, e.dz.calls, e.ocr.files)
	}
	if got, want := speakers(second.Transcript), speakers(first.Transcript); !reflect.DeepEqual(got, want) {
		t.Fatalf("cached transcript = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(second.Screens, first.Screens) {
		t.Fatalf("cached screens = %+v, want %+v", second.Screens, first.Screens)
	}
	if !reflect.DeepEqual(second.Embeddings, first.Embeddings) {
		t.Fatalf("cached embeddings = %v, want %v", second.Embeddings, first.Embeddings)
	}
}

func TestTranscribeCacheInvalidatesOnMtimeChange(t *testing.T) {
	e := newEnv(t)
	transcribeFixture(e)
	ctx := context.Background()
	if _, err := e.p.Transcribe(ctx, day, 13); err != nil {
		t.Fatal(err)
	}
	e.resetCounts()

	touched := old.Add(time.Second)
	for _, f := range []string{"audio/1305-system.m4a", "screens/131000.jpg"} {
		if err := os.Chtimes(filepath.Join(e.paths.RawDay(day), f), touched, touched); err != nil {
			t.Fatal(err)
		}
	}
	e.tr.utts["1305-system"] = []fu{{1, 4, "changed words"}}
	doc, err := e.p.Transcribe(ctx, day, 13)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.tr.calls, []string{"1305-system"}) || !reflect.DeepEqual(e.dz.calls, []string{"1305-system"}) {
		t.Fatalf("recomputed whisper %v, diarizer %v; want only 1305-system", e.tr.calls, e.dz.calls)
	}
	if !reflect.DeepEqual(e.ocr.files, []string{"131000.jpg"}) {
		t.Fatalf("re-OCRed %v", e.ocr.files)
	}
	want := []string{"me: mic hello", "Speaker 1: first voice", "Speaker 2: second voice", "Speaker 2: changed words"}
	if got := speakers(doc.Transcript); !reflect.DeepEqual(got, want) {
		t.Fatalf("transcript = %q, want %q", got, want)
	}
}

// The cache holds voice embeddings, so it must live outside derived/ (which
// Claude may read), and nothing under derived/ may carry an embedding.
func TestEmbeddingsNeverWrittenUnderDerived(t *testing.T) {
	e := newEnv(t)
	transcribeFixture(e)
	ctx := context.Background()
	if err := e.p.ProcessPending(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.paths.CacheDay(day)); err != nil {
		t.Fatalf("no cache written: %v", err)
	}
	derived := filepath.Join(e.paths.Root, "derived")
	if strings.HasPrefix(e.paths.CacheDay(day), derived) {
		t.Fatalf("cache %s is under derived/", e.paths.CacheDay(day))
	}
	cached := false
	filepath.WalkDir(e.paths.Cache(), func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(path)
			cached = cached || strings.Contains(string(b), `"embeddings"`)
		}
		return nil
	})
	if !cached {
		t.Fatal("cache holds no embeddings; the test would prove nothing")
	}
	n := 0
	err := filepath.WalkDir(derived, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		n++
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// A JSON key, not the test's temp dir name in a file path.
		if strings.Contains(strings.ToLower(string(b)), `"embedding`) {
			t.Errorf("%s contains an embedding field", path)
		}
		return nil
	})
	if err != nil || n == 0 {
		t.Fatalf("walk derived: %d files, %v", n, err)
	}
}
