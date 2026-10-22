package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Canonical WAV parameters of ToWav16k's output.
const (
	wavRate     = 16000
	wavChannels = 1
	wavBits     = 16
)

// Format tags of a WAV fmt chunk.
const (
	wavFormatPCM        = 0x0001
	wavFormatExtensible = 0xFFFE
)

// ksPCMSubformat is KSDATAFORMAT_SUBTYPE_PCM, the subformat GUID of an
// extensible fmt chunk holding integer PCM.
var ksPCMSubformat = []byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x10, 0x00, 0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71}

// NormalizeWav16k rewrites the WAV at path as a canonical PCM WAV: a 44-byte
// header (RIFF/WAVE, a 16-byte fmt chunk with format tag 1, then the data
// chunk) followed by the same samples. afconvert writes
// WAVE_FORMAT_EXTENSIBLE headers (40-byte fmt chunk, tag 0xFFFE) and extra
// chunks (FLLR, ...) that sherpa-onnx's reader rejects. The input must be
// 16 kHz mono 16-bit integer PCM, plain or extensible; anything else is an
// error and leaves the file untouched. The file is replaced atomically.
func NormalizeWav16k(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	off, n, canonical, err := parseWav16k(f, fi.Size())
	if err != nil {
		return fmt.Errorf("wav %s: %w", path, err)
	}
	if canonical {
		return nil
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".wav-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(canonicalHeader(n)); err != nil {
		tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, io.NewSectionReader(f, off, n)); err != nil {
		tmp.Close()
		return fmt.Errorf("wav %s: %w", path, err)
	}
	if n%2 == 1 {
		if _, err := tmp.Write([]byte{0}); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := tmp.Chmod(fi.Mode().Perm()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// parseWav16k walks the RIFF chunks of a WAV of size bytes and returns the
// offset and length of its sample data. canonical reports that the file
// already is exactly canonicalHeader followed by the samples.
func parseWav16k(r io.ReaderAt, size int64) (off, n int64, canonical bool, err error) {
	le := binary.LittleEndian
	head := make([]byte, 12)
	if _, err := r.ReadAt(head, 0); err != nil {
		return 0, 0, false, errors.New("too short for a RIFF header")
	}
	if string(head[0:4]) != "RIFF" || string(head[8:12]) != "WAVE" {
		return 0, 0, false, errors.New("not a RIFF/WAVE file")
	}
	var fmtSeen bool
	pos := int64(12)
	for pos+8 <= size {
		ch := make([]byte, 8)
		if _, err := r.ReadAt(ch, pos); err != nil {
			return 0, 0, false, err
		}
		id, csize := string(ch[0:4]), int64(le.Uint32(ch[4:8]))
		body := pos + 8
		switch id {
		case "fmt ":
			if csize < 16 || body+csize > size {
				return 0, 0, false, fmt.Errorf("bad fmt chunk size %d", csize)
			}
			b := make([]byte, csize)
			if _, err := r.ReadAt(b, body); err != nil {
				return 0, 0, false, err
			}
			if err := checkFmt16k(b); err != nil {
				return 0, 0, false, err
			}
			fmtSeen = true
		case "data":
			if !fmtSeen {
				return 0, 0, false, errors.New("data chunk before fmt chunk")
			}
			// A writer that never finalized the header may overstate the
			// size; keep what the file holds, in whole samples.
			n = min(csize, size-body)
			n -= n % (wavChannels * wavBits / 8)
			canonical = body == 44 && n == csize && size == 44+n && le.Uint32(head[4:8]) == uint32(36+n)
			if canonical {
				hdr := make([]byte, 44)
				if _, err := r.ReadAt(hdr, 0); err != nil {
					return 0, 0, false, err
				}
				canonical = bytes.Equal(hdr, canonicalHeader(n))
			}
			return body, n, canonical, nil
		}
		pos = body + csize + csize%2
	}
	if !fmtSeen {
		return 0, 0, false, errors.New("no fmt chunk")
	}
	return 0, 0, false, errors.New("no data chunk")
}

// checkFmt16k accepts a fmt chunk body describing 16 kHz mono 16-bit integer
// PCM, plain or WAVE_FORMAT_EXTENSIBLE with the PCM subformat.
func checkFmt16k(b []byte) error {
	le := binary.LittleEndian
	tag, ch, rate, bits := le.Uint16(b[0:]), le.Uint16(b[2:]), le.Uint32(b[4:]), le.Uint16(b[14:])
	switch tag {
	case wavFormatPCM:
	case wavFormatExtensible:
		if len(b) < 40 || !bytes.Equal(b[24:40], ksPCMSubformat) {
			return errors.New("extensible format is not integer PCM")
		}
		if valid := le.Uint16(b[18:]); valid != 0 && valid != wavBits {
			return fmt.Errorf("%d valid bits per sample, want %d", valid, wavBits)
		}
	default:
		return fmt.Errorf("format tag %#x is not PCM", tag)
	}
	if ch != wavChannels || rate != wavRate || bits != wavBits {
		return fmt.Errorf("%d ch %d Hz %d-bit, want %d ch %d Hz %d-bit", ch, rate, bits, wavChannels, wavRate, wavBits)
	}
	return nil
}

// canonicalHeader is the 44-byte header of a 16 kHz mono 16-bit PCM WAV
// with n bytes of samples.
func canonicalHeader(n int64) []byte {
	le := binary.LittleEndian
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	le.PutUint32(h[4:], uint32(36+n+n%2))
	copy(h[8:], "WAVEfmt ")
	le.PutUint32(h[16:], 16)
	le.PutUint16(h[20:], wavFormatPCM)
	le.PutUint16(h[22:], wavChannels)
	le.PutUint32(h[24:], wavRate)
	le.PutUint32(h[28:], wavRate*wavChannels*wavBits/8)
	le.PutUint16(h[32:], wavChannels*wavBits/8)
	le.PutUint16(h[34:], wavBits)
	copy(h[36:], "data")
	le.PutUint32(h[40:], uint32(n))
	return h
}

// ReadWav16k returns the sample bytes of a 16 kHz mono 16-bit PCM WAV,
// plain or extensible.
func ReadWav16k(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	off, n, _, err := parseWav16k(f, fi.Size())
	if err != nil {
		return nil, fmt.Errorf("wav %s: %w", path, err)
	}
	b := make([]byte, n)
	if _, err := f.ReadAt(b, off); err != nil {
		return nil, fmt.Errorf("wav %s: %w", path, err)
	}
	return b, nil
}

// WriteWav16k writes pcm (16 kHz mono 16-bit little-endian samples) as a
// canonical PCM WAV.
func WriteWav16k(path string, pcm []byte) error {
	b := append(canonicalHeader(int64(len(pcm))), pcm...)
	if len(pcm)%2 == 1 {
		b = append(b, 0)
	}
	return os.WriteFile(path, b, 0o644)
}

// Wav16kDuration returns how long the 16 kHz mono 16-bit PCM WAV at path
// plays, from its data chunk's size.
func Wav16kDuration(path string) (time.Duration, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	_, n, _, err := parseWav16k(f, fi.Size())
	if err != nil {
		return 0, fmt.Errorf("wav %s: %w", path, err)
	}
	return time.Duration(n) * time.Second / (wavRate * wavChannels * wavBits / 8), nil
}
