package media

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ToWav16k converts in to a 16 kHz mono 16-bit WAV at out using afconvert.
func ToWav16k(ctx context.Context, run Runner, in, out string) error {
	_, err := run(ctx, "afconvert", "-f", "WAVE", "-d", "LEI16@16000", "-c", "1", in, out)
	return err
}

// ParseChunkName parses HHMM-<track>.m4a or HHMMSS-<track>.m4a where track is
// "system" or "mic". ss is 0 for the 4-digit form.
func ParseChunkName(name string) (hh, mm, ss int, track string, ok bool) {
	base, found := strings.CutSuffix(name, ".m4a")
	if !found {
		return 0, 0, 0, "", false
	}
	prefix, track, found := strings.Cut(base, "-")
	if !found || (track != "system" && track != "mic") {
		return 0, 0, 0, "", false
	}
	if len(prefix) != 4 && len(prefix) != 6 {
		return 0, 0, 0, "", false
	}
	for _, c := range prefix {
		if c < '0' || c > '9' {
			return 0, 0, 0, "", false
		}
	}
	hh, _ = strconv.Atoi(prefix[0:2])
	mm, _ = strconv.Atoi(prefix[2:4])
	if len(prefix) == 6 {
		ss, _ = strconv.Atoi(prefix[4:6])
	}
	if hh > 23 || mm > 59 || ss > 59 {
		return 0, 0, 0, "", false
	}
	return hh, mm, ss, track, true
}

// ChunkStart returns the start time and track of an audio chunk file name
// recorded on day ("YYYY-MM-DD") in loc.
func ChunkStart(day string, name string, loc *time.Location) (time.Time, string, error) {
	d, err := time.ParseInLocation("2006-01-02", day, loc)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("chunk day %q: %w", day, err)
	}
	hh, mm, ss, track, ok := ParseChunkName(name)
	if !ok {
		return time.Time{}, "", fmt.Errorf("unrecognised audio chunk name %q", name)
	}
	return time.Date(d.Year(), d.Month(), d.Day(), hh, mm, ss, 0, loc), track, nil
}
