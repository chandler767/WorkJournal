# Spike: local speaker diarization with sherpa-onnx from Go

Date: 2026-10-07. Machine: Apple Silicon (arm64), macOS 26.5.1, go1.27.1.
The spike code (`spike/diarize/`, its own Go module) was deleted after the measurements. This document is what is left.

## Decision: GO

Use sherpa-onnx for diarization. Task 8b implements `SherpaDiarizer` and switches the `Diarization` default from `"off"` to `"sherpa"` (see "Follow-ups" below).

Why:
- **Speed.** RTF is 0.02–0.04 on a 5-minute input with the chosen models, about 15× faster than the < 0.5× target. The models load in 0.05 s and peak RSS is about 390 MB.
- **2 speakers** (the common call): 56 of 56 turns were correct, with 0 % speaker confusion. This held with 0.3 s gaps between turns and with no gaps.
- **4 speakers:** 51 of 56 turns correct (90.7 % of speech frames). Clustering found 4 speakers on its own (threshold 0.7).
- **Real speech** (sherpa's 4-speaker Mandarin sample): the output matches sherpa's published reference segmentation exactly when told 4 clusters. Automatic clustering finds 4–5 speakers.
- **Embeddings separate speakers well.** Across chunks, the same speaker scores 0.66–0.80 cosine on real speech (0.90–0.99 on synthetic). Different speakers score at most 0.47 on real speech.
- The Go binding builds and tests with plain `go build`/`go test`. No DYLD variables are needed (details below).

Known weakness, which is why this is GO rather than an unconditional GO: in the 3-speaker synthetic test, the **segmentation model** (pyannote 3.0) merged back-to-back turns of two similar female TTS voices (Samantha en_US and Karen en_AU) into one segment. Accuracy fell to 75 % (41 of 56 turns). The embeddings were not the cause: they separate those two voices (0.55–0.67 vs ≥ 0.90 same-voice). The cause was segmentation. Changing the clustering threshold, `NumClusters`, or `MinDurationOn`/`Off` did not fix it. Expect some mislabeled turns when two similar voices alternate quickly. The cost is low: an utterance gets the wrong `Speaker N`, and `summarize-meeting` still sees the whole transcript.

## Models (downloaded to `~/WorkJournal/models/`)

| File | URL | Size (bytes) | Use |
|---|---|---|---|
| `sherpa-onnx-pyannote-segmentation-3-0.tar.bz2` → `sherpa-onnx-pyannote-segmentation-3-0/model.onnx` | https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-segmentation-models/sherpa-onnx-pyannote-segmentation-3-0.tar.bz2 | 6,958,444 (tar); model.onnx 5,992,913; model.int8.onnx 1,540,506 | **Chosen:** segmentation `model.onnx` (fp32) |
| `nemo_en_titanet_small.onnx` | https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-recongition-models/nemo_en_titanet_small.onnx | 40,257,283 | **Chosen:** embedding (dim 192) |
| `3dspeaker_speech_eres2net_base_sv_zh-cn_3dspeaker_16k.onnx` | https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-recongition-models/3dspeaker_speech_eres2net_base_sv_zh-cn_3dspeaker_16k.onnx | 39,593,761 | Compared, not chosen (dim 512) |
| `0-four-speakers-zh.wav` (test sample, 56.9 s, 16 kHz mono) | https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-segmentation-models/0-four-speakers-zh.wav | 1,819,586 | Test input only |

Note that `recongition` is misspelled in the upstream release tag. Use the URL exactly as written.

Why titanet-small over eres2net: it is about 2× faster (RTF 0.025 vs 0.051 at 2 threads) and it is an English model. It also scored the same or better in every diarization test; eres2net was worse at the clustering threshold of 0.5. The int8 segmentation model was not faster (RTF 0.032 vs 0.025) and found one extra spurious speaker, so use the fp32 `model.onnx`.

Paths in 8b: `filepath.Join(paths.Models(), "sherpa-onnx-pyannote-segmentation-3-0", "model.onnx")` and `filepath.Join(paths.Models(), "nemo_en_titanet_small.onnx")`.

## Go module and build notes

- Module: `github.com/k2-fsa/sherpa-onnx-go-macos v1.13.8` (package name `sherpa_onnx`, import as `sherpa`). It is about 67 MB in the module cache. It bundles prebuilt dylibs for both arm64 and amd64: `libsherpa-onnx-c-api.dylib` (4.2 MB) and `libonnxruntime.dylib` (29 MB).
- It uses **cgo**. `CGO_ENABLED=1` is already the default here (`cc` = Apple clang). The main `go build -o bin/journal ./cmd/journal` keeps working unchanged. The binary itself grows only about 4 MB because the libraries are linked dynamically.
- The module's `build_darwin_arm64.go` sets `-L ${SRCDIR}/lib/aarch64-apple-darwin -lsherpa-onnx-c-api -lonnxruntime -Wl,-rpath,${SRCDIR}/lib/aarch64-apple-darwin`. The binary therefore gets an **absolute LC_RPATH into the Go module cache** (`~/go/pkg/mod/github.com/k2-fsa/sherpa-onnx-go-macos@v1.13.8/lib/aarch64-apple-darwin`). As a result:
  - `go build`, `go test` and `go vet` work with **no DYLD_* variables and no extra flags**. This was verified with a `go test` that loads the titanet model.
  - The built `bin/journal` loads its dylibs from the module cache. If the cache is removed (`go clean -modcache`) or the binary is copied to another machine, it fails at launch with `dyld: Library not loaded: @rpath/libsherpa-onnx-c-api.dylib ... no LC_RPATH's found`. This was reproduced by deleting the rpath.
  - A relocatable option, verified working: copy `libsherpa-onnx-c-api.dylib` and `libonnxruntime.dylib` into `<prefix>/lib/`, then build with `go build -ldflags='-extldflags "-Wl,-rpath,@executable_path/../lib"'`. Plain `-ldflags "-r ..."` is rejected by the Go linker for this path. For a personal tool built in place, the default module-cache rpath is acceptable. Note this in the README/Makefile and do not add complexity.
- Every Go package that imports the binding needs cgo. Keep the import confined to one file (for example `internal/diarize/sherpa.go`) so the rest of the module stays pure Go. Unit tests for the pipeline keep using fakes or `NoopDiarizer`. A sherpa test should `t.Skip` when the model files are absent.
- No stderr noise was observed in normal runs. On failure, the C side prints its own message, such as `.../wave-reader.cc:ReadWave:355 Filename '...' does not exist`, and the Go constructor returns `nil`.

## Go API used (sherpa-onnx-go-macos v1.13.8)

- `sherpa.OfflineSpeakerDiarizationConfig{ Segmentation: sherpa.OfflineSpeakerSegmentationModelConfig{ Pyannote: sherpa.OfflineSpeakerSegmentationPyannoteModelConfig{Model}, NumThreads, Provider }, Embedding: sherpa.SpeakerEmbeddingExtractorConfig{Model, NumThreads, Provider}, Clustering: sherpa.FastClusteringConfig{NumClusters, Threshold}, MinDurationOn, MinDurationOff }`
- `sherpa.NewOfflineSpeakerDiarization(&cfg) *OfflineSpeakerDiarization`, which returns **nil** on a bad config or missing file. It is freed with `sherpa.DeleteOfflineSpeakerDiarization(sd)`. Other calls: `sd.SampleRate()` (16000) and `sd.Process(samples []float32) []OfflineSpeakerDiarizationSegment{Start, End float32; Speaker int; Confidence float32}`. The segments come back sorted by start time. **Speaker ids are not contiguous** (for example 0, 1, 2, 4, 7), so treat them as opaque keys. `Process` panics on an empty slice (`&samples[0]`), so guard against `len == 0`.
- `sherpa.NewSpeakerEmbeddingExtractor(&sherpa.SpeakerEmbeddingExtractorConfig{Model, NumThreads, Provider: "cpu"})` returns nil on error and is freed with `sherpa.DeleteSpeakerEmbeddingExtractor`. Other calls: `ex.Dim()` (192 for titanet-small) and `st := ex.CreateStream()`, freed with `sherpa.DeleteOnlineStream(st)`. Then `st.AcceptWaveform(16000, samples)`, `st.InputFinished()`, `ex.IsReady(st)` and `ex.Compute(st) []float32`. The embeddings are **not** unit-normalized, so normalize or use full cosine.
- `sherpa.ReadWave(path) *sherpa.Wave{Samples []float32; SampleRate int}` reads 16-bit PCM WAV into floats in [-1, 1] and returns nil on error. It reads only mono WAVs, so the WAV must be produced with `afconvert -f WAVE -d LEI16@16000 -c 1`, which is what the transcription step already does for whisper.
- Not needed: `SpeakerEmbeddingManager` (Register/Search/Verify) duplicates `diarize.Voices`, which already stores profiles as JSON. `sd.SetConfig` changes only clustering.
- Cancellation: `Process` is a blocking C call and cannot be cancelled mid-run. Check `ctx.Err()` before and after it. That is acceptable at about 10 s per 5-minute chunk.
- Concurrency: create the diarizer and extractor once and reuse them (load takes 0.05 s, so per-call creation is also acceptable). Do not call `Process` concurrently on one instance. The pipeline processes one hour at a time, so a `sync.Mutex` around `Diarize` is enough.

### Minimal sketch for Task 8b (`internal/diarize/sherpa.go`)

This sketch was compiled, vetted and run against `0-four-speakers-zh.wav` during the spike: 10 turns, 5 speaker embeddings. Missing model paths return an error.

```go
package diarize

import (
	"context"
	"errors"
	"fmt"
	"sync"

	sherpa "github.com/k2-fsa/sherpa-onnx-go-macos"
)

// SherpaDiarizer runs pyannote-3.0 segmentation + titanet-small embeddings locally.
type SherpaDiarizer struct {
	mu sync.Mutex
	sd *sherpa.OfflineSpeakerDiarization
	ex *sherpa.SpeakerEmbeddingExtractor
}

func NewSherpaDiarizer(segModel, embModel string) (*SherpaDiarizer, error) {
	cfg := sherpa.OfflineSpeakerDiarizationConfig{
		Segmentation: sherpa.OfflineSpeakerSegmentationModelConfig{
			Pyannote:   sherpa.OfflineSpeakerSegmentationPyannoteModelConfig{Model: segModel},
			NumThreads: 2, Provider: "cpu",
		},
		Embedding:      sherpa.SpeakerEmbeddingExtractorConfig{Model: embModel, NumThreads: 2, Provider: "cpu"},
		Clustering:     sherpa.FastClusteringConfig{NumClusters: -1, Threshold: 0.7},
		MinDurationOn:  0.3,
		MinDurationOff: 0.5,
	}
	sd := sherpa.NewOfflineSpeakerDiarization(&cfg)
	if sd == nil {
		return nil, fmt.Errorf("sherpa: cannot load diarization models %s, %s", segModel, embModel)
	}
	ex := sherpa.NewSpeakerEmbeddingExtractor(&cfg.Embedding)
	if ex == nil {
		sherpa.DeleteOfflineSpeakerDiarization(sd)
		return nil, fmt.Errorf("sherpa: cannot load embedding model %s", embModel)
	}
	return &SherpaDiarizer{sd: sd, ex: ex}, nil
}

func (d *SherpaDiarizer) Close() {
	sherpa.DeleteOfflineSpeakerDiarization(d.sd)
	sherpa.DeleteSpeakerEmbeddingExtractor(d.ex)
}

func (d *SherpaDiarizer) Diarize(ctx context.Context, wav string) ([]Turn, map[int][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	w := sherpa.ReadWave(wav)
	if w == nil {
		return nil, nil, fmt.Errorf("sherpa: cannot read %s", wav)
	}
	if w.SampleRate != 16000 {
		return nil, nil, fmt.Errorf("sherpa: %s is %d Hz, want 16000", wav, w.SampleRate)
	}
	if len(w.Samples) == 0 {
		return nil, nil, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	segs := d.sd.Process(w.Samples)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	// Renumber opaque sherpa ids to 0..n-1 in order of first appearance.
	ids := map[int]int{}
	audio := map[int][]float32{}
	var turns []Turn
	for _, s := range segs {
		id, ok := ids[s.Speaker]
		if !ok {
			id = len(ids)
			ids[s.Speaker] = id
		}
		turns = append(turns, Turn{Start: float64(s.Start), End: float64(s.End), Speaker: id})
		a, b := int(s.Start*16000), min(int(s.End*16000), len(w.Samples))
		if a < b && len(audio[id]) < 30*16000 { // cap ~30 s per speaker
			audio[id] = append(audio[id], w.Samples[a:b]...)
		}
	}
	embs := map[int][]float32{}
	for id, a := range audio {
		st := d.ex.CreateStream()
		st.AcceptWaveform(16000, a)
		st.InputFinished()
		if d.ex.IsReady(st) {
			embs[id] = d.ex.Compute(st)
		}
		sherpa.DeleteOnlineStream(st)
	}
	if len(turns) > 0 && len(embs) == 0 {
		return nil, nil, errors.New("sherpa: no speaker embeddings computed")
	}
	return turns, embs, nil
}
```

## Test inputs

1. **Real speech:** `0-four-speakers-zh.wav`, 56.9 s, 4 Mandarin speakers. Its ground truth is sherpa's published reference output for this file (segments 0.32–6.87 s A, 7.02–13.63 s B, 13.75–17.04 s C, 22.14–24.84 s A, 27.64–37.93 s D, 48.04–50.47 s C, 52.53–54.60 s A). This is not a human-labelled reference. The file was looped 5× (284 s) for timing.
2. **Synthetic English with exact ground truth:** 28 meeting-style sentences, spoken with macOS `say` voices Samantha (en_US F), Daniel (en_GB M), Karen (en_AU F) and Fred (en_US M). Each was converted with `afconvert -f WAVE -d LEI16@16000 -c 1` and concatenated in Go into 56 turns in an irregular speaker order, about 4 minutes per file:
   - `two` (Samantha and Daniel, 0.3 s gaps), `two-tight` (the same turns with no gaps), `three` (plus Karen), `four` (plus Fred).
   - Scoring was frame-level (10 ms) with a greedy one-to-one cluster→speaker mapping. "Turns ok" means the majority predicted label of a ground-truth turn maps to the right speaker. Most of the "false alarm" figure is the 0.3 s inter-turn gaps, which `MinDurationOff=0.5` fills on purpose, so it is harmless.

The human step (recording a real ≥ 5-minute call through `recorder run`) was replaced by these samples, per the controller.

## Measurements

### Speed (diarization `Process` only, fp32 segmentation, clustering threshold 0.7)

| Input | Embedding | Threads | Audio | Process | RTF | Peak RSS |
|---|---|---|---|---|---|---|
| zh × 5 | titanet-small | 1 | 284 s | 9.07 s | 0.032 | 388 MB |
| zh × 5 | titanet-small | 2 | 284 s | 7.21 s | **0.025** | 387 MB |
| zh × 5 | titanet-small | 4 | 284 s | 6.29 s | 0.022 | 388 MB |
| zh × 5 | eres2net | 1 | 284 s | 22.90 s | 0.081 | 381 MB |
| zh × 5 | eres2net | 2 | 284 s | 14.57 s | 0.051 | 382 MB |
| zh × 5 | eres2net | 4 | 284 s | 10.68 s | 0.038 | 384 MB |
| zh × 5 (int8 seg) | titanet-small | 2 | 284 s | 9.03 s | 0.032 | 380 MB |
| `two` × 2 | titanet-small | 2 | 487 s | 17.54 s | 0.036 | — |

Model load takes 0.05–0.06 s. Per-speaker embedding of up to 30 s of audio adds well under 1 s: diarizing plus embedding a 2-minute chunk took 4.3–4.6 s in total. **2 threads is the right setting**: it leaves cores for whisper and the doubling from 2 to 4 threads gains little.

### Accuracy (titanet-small unless noted; `NumClusters=-1`)

| Input | Threshold 0.5 | Threshold 0.7 | Threshold 0.8–0.9 | Known count (`NumClusters=n`) |
|---|---|---|---|---|
| zh, 4 speakers (real) | 8 found | 5 found | **4 found** | 4 found, **identical to sherpa reference** (both models) |
| `two` | 3 found, 97.2 %, 55/56 turns | **2 found, 98.9 %, 0 % confusion, 56/56** | same as 0.7 | — |
| `two-tight` (no gaps) | 2, 98.6 %, 56/56 | 2, 98.6 %, 56/56 | same (1.0 collapses to 1 speaker) | — |
| `three` | 4 found, 75.0 %, 41/56 | 3 found, 75.4 %, 41/56 | 2 found, 70.5 % | 75.4 %, 41/56 (segmentation-limited, see above) |
| `four` | 5 found, 89.1 %, 50/56 | **4 found, 90.7 %, 51/56** | 3 found, 78.9 % | 78.9 % (n=4 did not help) |
| eres2net `two` / `three` / `four` | 79.4 % / 64.0 % / 68.5 % | 98.9 % / 80.1 % / 81.5 % | 98.9 % / 70.5 % / 77.0 % | — |

The brief's starting value of **clustering threshold 0.5 over-splits** on both models (8 speakers on 4-speaker real speech). Use **0.7**. It is the best on the synthetic 2- and 4-speaker files. On real speech it errs toward one extra speaker, which is the safer failure: an extra "Speaker N" can still be merged by voice matching, while two people merged into one cannot be split later. `NumClusters` stays -1 because the speaker count is not known in advance.

### Cosine similarity, same speaker across two different clips vs different speakers

Each pair compares embeddings from different audio, meaning different sentences or a different part of the call.

| Model | Real speech (zh, clips 2.2–6.5 s): same | Real: different | Synthetic TTS (4–14 s clips): same | Synthetic: different |
|---|---|---|---|---|
| titanet-small | **0.664 – 0.795** (n=4) | **0.060 – 0.471** (n=24) | 0.901 – 0.984 (n=12) | 0.059 – 0.668 (n=54; the max is Samantha vs Karen) |
| eres2net | 0.616 – 0.778 (n=4) | −0.070 – 0.234 (n=24) | 0.835 – 0.973 (n=12) | 0.102 – 0.697 (n=54) |

The pipeline-shaped test diarized two halves of a file separately, embedded each local cluster (concatenated turns, capped at 30 s), and compared clusters across the halves. Same speaker scored 0.968–0.996 on synthetic and **0.659 / 0.680** on real speech (2–9 s of audio per cluster). Different speakers scored at most 0.436 on synthetic and at most 0.355 on real speech.

### Recommended `voice_match_threshold`: **0.60** (down from 0.75), with titanet-small

- With 0.75, 3 of 4 real same-speaker pairs (0.664, 0.678, 0.743) and both cross-chunk real matches (0.659, 0.680) would **fail to match**. Known voices would rarely get named, and Ruling 17's cross-chunk linking would rarely fire.
- 0.60 sits above every real different-speaker score (max 0.471) and below every real same-speaker score (min 0.659). Real-speech scores rise with more audio, so enrolling profiles from at least 10 s of speech widens this margin. `Voices.Enroll` already appends embeddings.
- Caveat: two similar TTS voices reached 0.67. If real colleagues sound that alike, 0.60 could mismatch them. The evidence comes from 4 real (Mandarin) speakers plus 4 synthetic voices. Re-check after the first real recorded English call, which Task 8b's manual checklist should include. The threshold is already configurable.

## Follow-ups for Task 8b (not done here: this task commits only this document)

1. Add `internal/diarize/sherpa.go` (the sketch above) and the `github.com/k2-fsa/sherpa-onnx-go-macos v1.13.8` requirement to the main `go.mod`.
2. When `Diarization == "sherpa"`, build `SherpaDiarizer` with the two model paths under `paths.Models()`. If the models are missing, fall back to `NoopDiarizer` with a logged error, not a crash.
3. Make setup download the two model files from the URLs above (and extract the segmentation tarball).
4. Change the `config.Default()` value of `Diarization` from `"off"` to `"sherpa"`, undoing Ruling 28. Change `VoiceMatchThreshold` from 0.75 to 0.60, which also changes the default in `constraints.md` and `config.example.toml`. Re-enable voice naming (Ruling 26/28 guards).
5. Document in the README that `bin/journal` links the sherpa dylibs from the Go module cache, or use the `@executable_path/../lib` rpath build above.
