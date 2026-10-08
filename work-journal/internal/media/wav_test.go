package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// riffChunk encodes one RIFF chunk, padded to an even length.
func riffChunk(id string, body []byte) []byte {
	b := make([]byte, 8, 8+len(body)+1)
	copy(b, id)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(body)))
	b = append(b, body...)
	if len(body)%2 == 1 {
		b = append(b, 0)
	}
	return b
}

func riff(chunks ...[]byte) []byte {
	body := []byte("WAVE")
	for _, c := range chunks {
		body = append(body, c...)
	}
	return append(append([]byte("RIFF"), binary.LittleEndian.AppendUint32(nil, uint32(len(body)))...), body...)
}

// fmtBody is a fmt chunk body for tag/channels/rate/bits, with size bytes
// (16, 18 or 40; 40 is WAVE_FORMAT_EXTENSIBLE with subformat sub).
func fmtBody(tag, channels uint16, rate uint32, bits uint16, size int, sub []byte) []byte {
	b := make([]byte, size)
	le := binary.LittleEndian
	le.PutUint16(b[0:], tag)
	le.PutUint16(b[2:], channels)
	le.PutUint32(b[4:], rate)
	le.PutUint32(b[8:], rate*uint32(channels)*uint32(bits/8))
	le.PutUint16(b[12:], channels*bits/8)
	le.PutUint16(b[14:], bits)
	if size >= 18 {
		le.PutUint16(b[16:], uint16(size-18))
	}
	if size == 40 {
		le.PutUint16(b[18:], bits) // valid bits
		le.PutUint32(b[20:], 0x4)  // channel mask: front center
		copy(b[24:], sub)
	}
	return b
}

// pcmSubformat is KSDATAFORMAT_SUBTYPE_PCM.
var pcmSubformat = []byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x10, 0x00, 0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71}

// assertCanonical checks b is a 44-byte-header 16 kHz mono 16-bit PCM WAV
// and returns its samples.
func assertCanonical(t *testing.T, b []byte) []byte {
	t.Helper()
	le := binary.LittleEndian
	if len(b) < 44 || string(b[0:4]) != "RIFF" || string(b[8:16]) != "WAVEfmt " || string(b[36:40]) != "data" {
		t.Fatalf("not a canonical WAV header: % x", b[:min(len(b), 48)])
	}
	if got := le.Uint32(b[16:]); got != 16 {
		t.Errorf("fmt chunk size %d, want 16", got)
	}
	if got := le.Uint16(b[20:]); got != 1 {
		t.Errorf("format tag %#x, want 1 (PCM)", got)
	}
	if ch, rate, bits := le.Uint16(b[22:]), le.Uint32(b[24:]), le.Uint16(b[34:]); ch != 1 || rate != 16000 || bits != 16 {
		t.Errorf("channels/rate/bits = %d/%d/%d, want 1/16000/16", ch, rate, bits)
	}
	if br, ba := le.Uint32(b[28:]), le.Uint16(b[32:]); br != 32000 || ba != 2 {
		t.Errorf("byte rate/block align = %d/%d, want 32000/2", br, ba)
	}
	n := int(le.Uint32(b[40:]))
	if got := le.Uint32(b[4:]); int(got) != 36+n+n%2 {
		t.Errorf("RIFF size %d, want %d", got, 36+n+n%2)
	}
	if len(b) != 44+n+n%2 {
		t.Fatalf("file is %d bytes, header says %d samples bytes", len(b), n)
	}
	return b[44 : 44+n]
}

func TestNormalizeWavRewritesExtensibleHeader(t *testing.T) {
	samples := []byte{1, 0, 2, 0, 0xff, 0x7f, 0, 0x80, 9, 9}
	in := riff(
		riffChunk("FLLR", make([]byte, 7)), // odd size: padded
		riffChunk("fmt ", fmtBody(0xFFFE, 1, 16000, 16, 40, pcmSubformat)),
		riffChunk("fact", []byte{5, 0, 0, 0}),
		riffChunk("data", samples),
		riffChunk("LIST", []byte("INFOxxxx")),
	)
	path := filepath.Join(t.TempDir(), "x.wav")
	if err := os.WriteFile(path, in, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := NormalizeWav16k(path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := assertCanonical(t, b); !bytes.Equal(got, samples) {
		t.Fatalf("samples = % x, want % x", got, samples)
	}
}

func TestNormalizeWavAcceptsPlainAndFmt18(t *testing.T) {
	samples := []byte{1, 2, 3, 4}
	for _, size := range []int{16, 18} {
		path := filepath.Join(t.TempDir(), "x.wav")
		os.WriteFile(path, riff(riffChunk("fmt ", fmtBody(1, 1, 16000, 16, size, nil)), riffChunk("data", samples)), 0o644)
		if err := NormalizeWav16k(path); err != nil {
			t.Fatalf("fmt %d: %v", size, err)
		}
		b, _ := os.ReadFile(path)
		if got := assertCanonical(t, b); !bytes.Equal(got, samples) {
			t.Fatalf("fmt %d: samples = % x", size, got)
		}
	}
}

func TestNormalizeWavRejectsOtherFormats(t *testing.T) {
	floatSub := append([]byte{0x03}, pcmSubformat[1:]...)
	cases := map[string][]byte{
		"not riff":        []byte("RIFX...."),
		"truncated":       []byte("RIFF"),
		"stereo":          riff(riffChunk("fmt ", fmtBody(1, 2, 16000, 16, 16, nil)), riffChunk("data", []byte{1, 2, 3, 4})),
		"44.1 kHz":        riff(riffChunk("fmt ", fmtBody(1, 1, 44100, 16, 16, nil)), riffChunk("data", []byte{1, 2})),
		"24-bit":          riff(riffChunk("fmt ", fmtBody(1, 1, 16000, 24, 16, nil)), riffChunk("data", []byte{1, 2, 3})),
		"float":           riff(riffChunk("fmt ", fmtBody(3, 1, 16000, 16, 18, nil)), riffChunk("data", []byte{1, 2})),
		"ext float":       riff(riffChunk("fmt ", fmtBody(0xFFFE, 1, 16000, 16, 40, floatSub)), riffChunk("data", []byte{1, 2})),
		"no data":         riff(riffChunk("fmt ", fmtBody(1, 1, 16000, 16, 16, nil))),
		"no fmt":          riff(riffChunk("data", []byte{1, 2})),
		"data before fmt": riff(riffChunk("data", []byte{1, 2}), riffChunk("fmt ", fmtBody(1, 1, 16000, 16, 16, nil))),
	}
	for name, in := range cases {
		path := filepath.Join(t.TempDir(), "x.wav")
		os.WriteFile(path, in, 0o644)
		err := NormalizeWav16k(path)
		if err == nil {
			t.Errorf("%s: want error", name)
			continue
		}
		if !strings.Contains(err.Error(), "x.wav") {
			t.Errorf("%s: error %q does not name the file", name, err)
		}
		if b, _ := os.ReadFile(path); !bytes.Equal(b, in) {
			t.Errorf("%s: file changed on error", name)
		}
	}
}

// The recorder writes AAC .m4a; afconvert turns that into an extensible WAV
// sherpa-onnx cannot read. ToWav16k must hand back a canonical header.
func TestToWav16kRealM4AIsCanonical(t *testing.T) {
	for _, tool := range []string{"say", "afconvert"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	dir := t.TempDir()
	aiff, m4a, wav := filepath.Join(dir, "x.aiff"), filepath.Join(dir, "x.m4a"), filepath.Join(dir, "x.wav")
	if out, err := exec.Command("say", "-o", aiff, "Checking the recorder audio format.").CombinedOutput(); err != nil {
		t.Skipf("say: %v: %s", err, out)
	}
	if out, err := exec.Command("afconvert", "-f", "m4af", "-d", "aac", aiff, m4a).CombinedOutput(); err != nil {
		t.Fatalf("afconvert to m4a: %v: %s", err, out)
	}
	if err := ToWav16k(context.Background(), ExecRunner, m4a, wav); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(wav)
	if err != nil {
		t.Fatal(err)
	}
	if samples := assertCanonical(t, b); len(samples) < 16000 {
		t.Fatalf("only %d bytes of samples", len(samples))
	}
}
