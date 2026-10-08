package process

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"workjournal/internal/diarize"
	"workjournal/internal/media"
)

// The processing cache (Paths.CacheDay) keeps the expensive per-file results
// so the in-progress hour can be summarized every few minutes and the final
// pass does not transcribe again:
//
//	cache/<day>/audio/<chunk>.json  whisper utterances (before speaker
//	                                labelling) and, for system chunks, the
//	                                diarizer turns and per-speaker embeddings
//	cache/<day>/ocr.json            OCR text per screenshot
//
// Each entry carries the source file's size and mtime; a mismatch means the
// file changed and the entry is recomputed. The cache holds voice embeddings,
// so it lives outside derived/ (which Claude may read) and is private (0700
// directories, 0600 files).

// cacheVersion changes when the entry layout or its meaning changes.
const cacheVersion = 1

// fileKey identifies one version of a source file.
type fileKey struct {
	Size    int64 `json:"size"`
	ModTime int64 `json:"mtime_ns"`
}

func statKey(path string) (fileKey, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileKey{}, err
	}
	return fileKey{Size: fi.Size(), ModTime: fi.ModTime().UnixNano()}, nil
}

// chunkEntry is the cached result of one audio chunk.
type chunkEntry struct {
	Version    int               `json:"version"`
	Key        fileKey           `json:"key"`
	Model      string            `json:"whisper_model"`
	Utterances []media.Utterance `json:"utterances"`
	Diarized   bool              `json:"diarized"` // Turns/Embeddings hold a successful diarization
	Turns      []diarize.Turn    `json:"turns,omitempty"`
	Embeddings map[int][]float32 `json:"embeddings,omitempty"`
}

func (p *Pipeline) chunkCachePath(day, chunkPath string) string {
	name := filepath.Base(chunkPath)
	return filepath.Join(p.Paths.CacheDay(day), "audio", strings.TrimSuffix(name, filepath.Ext(name))+".json")
}

// loadChunk returns the cached entry for the chunk when it matches key and
// the configured whisper model.
func (p *Pipeline) loadChunk(day, chunkPath string, key fileKey) (*chunkEntry, bool) {
	b, err := os.ReadFile(p.chunkCachePath(day, chunkPath))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			p.logf("process: cache %s: %v", chunkPath, err)
		}
		return nil, false
	}
	var e chunkEntry
	if err := json.Unmarshal(b, &e); err != nil {
		p.logf("process: cache %s unreadable (%v); recomputing", chunkPath, err)
		return nil, false
	}
	if e.Version != cacheVersion || e.Key != key || e.Model != p.Cfg.WhisperModel {
		return nil, false
	}
	return &e, true
}

// saveChunk stores e; a failure only costs a recomputation later.
func (p *Pipeline) saveChunk(day, chunkPath string, e *chunkEntry) {
	if err := writePrivateJSON(p.chunkCachePath(day, chunkPath), e); err != nil {
		p.logf("process: cache %s: %v", chunkPath, err)
	}
}

// ocrEntry is the cached OCR text of one screenshot ("" when it has none).
type ocrEntry struct {
	Key  fileKey `json:"key"`
	Text string  `json:"text"`
}

type ocrCache struct {
	Version int                 `json:"version"`
	Files   map[string]ocrEntry `json:"files"` // by screenshot file name
}

func (p *Pipeline) ocrCachePath(day string) string {
	return filepath.Join(p.Paths.CacheDay(day), "ocr.json")
}

func (p *Pipeline) loadOCRCache(day string) *ocrCache {
	c := &ocrCache{Version: cacheVersion, Files: map[string]ocrEntry{}}
	b, err := os.ReadFile(p.ocrCachePath(day))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			p.logf("process: ocr cache %s: %v", day, err)
		}
		return c
	}
	var got ocrCache
	if err := json.Unmarshal(b, &got); err != nil || got.Version != cacheVersion || got.Files == nil {
		p.logf("process: ocr cache %s unusable; recomputing", day)
		return c
	}
	return &got
}

func (p *Pipeline) saveOCRCache(day string, c *ocrCache) {
	if err := writePrivateJSON(p.ocrCachePath(day), c); err != nil {
		p.logf("process: ocr cache %s: %v", day, err)
	}
}

// writePrivateJSON writes v to path atomically, owner-only.
func writePrivateJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".cache-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write cache: %w", err)
	}
	return nil
}
