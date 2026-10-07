//go:build sherpa

package diarize

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// modelsDir is where the integration test looks for the sherpa models:
// $WORKJOURNAL_MODELS, else ~/WorkJournal/models.
func modelsDir(t *testing.T) string {
	if d := os.Getenv("WORKJOURNAL_MODELS"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	return filepath.Join(home, "WorkJournal", "models")
}

// sayPCM speaks text with a macOS voice and returns 16 kHz mono 16-bit samples.
func sayPCM(t *testing.T, dir, voice, text string, n int) []byte {
	t.Helper()
	aiff := filepath.Join(dir, fmt.Sprintf("%s-%d.aiff", voice, n))
	wav := filepath.Join(dir, fmt.Sprintf("%s-%d.wav", voice, n))
	if out, err := exec.Command("say", "-v", voice, "-o", aiff, text).CombinedOutput(); err != nil {
		t.Skipf("say -v %s: %v: %s", voice, err, out)
	}
	if out, err := exec.Command("afconvert", "-f", "WAVE", "-d", "LEI16@16000", "-c", "1", aiff, wav).CombinedOutput(); err != nil {
		t.Fatalf("afconvert: %v: %s", err, out)
	}
	b, err := os.ReadFile(wav)
	if err != nil {
		t.Fatal(err)
	}
	// afconvert may add chunks before "data"; find it.
	for i := 12; i+8 <= len(b); {
		id, size := string(b[i:i+4]), int(binary.LittleEndian.Uint32(b[i+4:i+8]))
		if id == "data" {
			return b[i+8 : min(i+8+size, len(b))]
		}
		i += 8 + size + size%2
	}
	t.Fatalf("no data chunk in %s", wav)
	return nil
}

func writeWav(t *testing.T, path string, pcm []byte) {
	t.Helper()
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+len(pcm)))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)     // PCM
	binary.LittleEndian.PutUint16(h[22:], 1)     // mono
	binary.LittleEndian.PutUint32(h[24:], 16000) // rate
	binary.LittleEndian.PutUint32(h[28:], 32000) // byte rate
	binary.LittleEndian.PutUint16(h[32:], 2)     // block align
	binary.LittleEndian.PutUint16(h[34:], 16)    // bits
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(pcm)))
	if err := os.WriteFile(path, append(h, pcm...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSherpaDiarizerTwoVoices(t *testing.T) {
	seg, emb := ModelPaths(modelsDir(t))
	for _, m := range []string{seg, emb} {
		if _, err := os.Stat(m); err != nil {
			t.Skipf("model missing (run `journal setup`): %v", err)
		}
	}
	dir := t.TempDir()
	lines := []struct{ voice, text string }{
		{"Samantha", "Good morning everyone, let's start with the checkout redesign and the timeline for the beta."},
		{"Daniel", "Thanks. The payment form is finished, but the address validation still needs another pass."},
		{"Samantha", "Okay. Can we ship the beta on Thursday if the validation work lands by Wednesday afternoon?"},
		{"Daniel", "I think so. I will pair with the backend team tomorrow and report back at the standup."},
		{"Samantha", "Great, then let's also review the analytics events before we invite the first customers."},
		{"Daniel", "Agreed. I will write up the event list and share it in the channel later today."},
	}
	gap := make([]byte, 2*16000*3/10) // 0.3 s of silence
	var pcm []byte
	var truth []string // voice per turn
	var bounds [][2]float64
	for i, l := range lines {
		start := float64(len(pcm)) / 32000
		pcm = append(pcm, sayPCM(t, dir, l.voice, l.text, i)...)
		bounds = append(bounds, [2]float64{start, float64(len(pcm)) / 32000})
		truth = append(truth, l.voice)
		pcm = append(pcm, gap...)
	}
	wav := filepath.Join(dir, "two.wav")
	writeWav(t, wav, pcm)

	d, err := NewSherpaDiarizer(seg, emb)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	turns, embs, err := d.Diarize(context.Background(), wav)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("audio %.1f s, %d turns", float64(len(pcm))/32000, len(turns))
	for _, tr := range turns {
		t.Logf("  %6.2f–%6.2f speaker %d", tr.Start, tr.End, tr.Speaker)
	}
	speakers := map[int]bool{}
	for _, tr := range turns {
		speakers[tr.Speaker] = true
	}
	if len(speakers) != 2 || len(embs) != 2 {
		t.Fatalf("found %d speakers and %d embeddings, want 2 and 2", len(speakers), len(embs))
	}
	t.Logf("cosine between the two speakers: %.3f", Cosine(embs[0], embs[1]))

	// Each ground-truth turn's dominant speaker id must follow its voice.
	byVoice := map[string]int{}
	for i, b := range bounds {
		ov := map[int]float64{}
		for _, tr := range turns {
			if o := min(b[1], tr.End) - max(b[0], tr.Start); o > 0 {
				ov[tr.Speaker] += o
			}
		}
		best, bestOv := -1, 0.0
		for sp, o := range ov {
			if o > bestOv {
				best, bestOv = sp, o
			}
		}
		if id, ok := byVoice[truth[i]]; ok && id != best {
			t.Errorf("turn %d (%s) labelled %d, earlier %s turns were %d", i, truth[i], best, truth[i], id)
		}
		byVoice[truth[i]] = best
	}
	if byVoice["Samantha"] == byVoice["Daniel"] {
		t.Errorf("both voices got speaker %d", byVoice["Samantha"])
	}
}
