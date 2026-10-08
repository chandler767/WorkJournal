// Package retention deletes raw media past its retention window.
package retention

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"workjournal/internal/config"
	"workjournal/internal/media"
	"workjournal/internal/store"
)

// Sweep deletes raw/<day>/ directories older than days before now, together
// with the day's processing cache (cache/<day>/). A day that still has an
// hour in pending, transcribed or partial is kept, since its raw media is
// still needed; directories whose names are not dates are ignored. It returns
// the removed day keys, oldest first.
func Sweep(p config.Paths, st *store.Store, now time.Time, days int) ([]string, error) {
	rawRoot := filepath.Dir(p.RawDay("x"))
	entries, err := os.ReadDir(rawRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cutoff := config.DayKey(now.AddDate(0, 0, -days))
	var removed []string
	var errs []error
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() {
			continue
		}
		if _, perr := time.Parse("2006-01-02", name); perr != nil || name >= cutoff {
			continue
		}
		hours, herr := st.HoursOn(name)
		if herr != nil {
			errs = append(errs, fmt.Errorf("hours on %s: %w", name, herr))
			continue
		}
		if unprocessed(hours) {
			continue
		}
		keep, merr := hasUnrecordedMedia(p, name, hours)
		if merr != nil {
			errs = append(errs, fmt.Errorf("keeping %s: %w", name, merr))
			continue
		}
		if keep {
			continue
		}
		if rerr := os.RemoveAll(p.RawDay(name)); rerr != nil {
			errs = append(errs, rerr)
			continue
		}
		if rerr := os.RemoveAll(p.CacheDay(name)); rerr != nil {
			errs = append(errs, rerr)
		}
		removed = append(removed, name)
	}
	sort.Strings(removed)
	return removed, errors.Join(errs...)
}

func unprocessed(hours []store.HourRecord) bool {
	for _, h := range hours {
		switch h.Status {
		case store.StatusPending, store.StatusTranscribed, store.StatusPartial:
			return true
		}
	}
	return false
}

// hasUnrecordedMedia reports whether the day's raw audio or screenshots cover
// an hour that has no done or needs_review record, i.e. was never processed.
// It fails safe: unreadable media dirs or unrecognised file names are returned
// as an error so the caller keeps the day.
func hasUnrecordedMedia(p config.Paths, day string, hours []store.HourRecord) (bool, error) {
	settled := map[int]bool{}
	for _, h := range hours {
		if h.Status == store.StatusDone || h.Status == store.StatusNeedsReview {
			settled[h.Hour] = true
		}
	}
	raw, err := rawHours(p, day)
	if err != nil {
		return true, err
	}
	for hour := range raw {
		if !settled[hour] {
			return true, nil
		}
	}
	return false, nil
}

// rawHours returns the hours that have audio chunks or screenshots on disk.
// A missing dir is fine; any other read error or unknown entry is an error.
func rawHours(p config.Paths, day string) (map[int]bool, error) {
	out := map[int]bool{}
	for _, d := range []struct {
		dir   string
		parse func(name string) (int, bool)
	}{{p.Audio(day), parseAudioHour}, {p.Screens(day), parseScreenHour}} {
		entries, err := os.ReadDir(d.dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.Name() == ".DS_Store" {
				continue
			}
			hh, ok := d.parse(e.Name())
			if !ok || e.IsDir() {
				return nil, fmt.Errorf("unrecognised media entry %s", filepath.Join(d.dir, e.Name()))
			}
			out[hh] = true
		}
	}
	return out, nil
}

func parseAudioHour(name string) (int, bool) {
	hh, _, _, _, ok := media.ParseChunkName(name)
	return hh, ok
}

func parseScreenHour(name string) (int, bool) {
	base, ok := strings.CutSuffix(name, ".jpg")
	if !ok || len(base) != 6 {
		return 0, false
	}
	for _, c := range base {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	hh, _ := strconv.Atoi(base[:2])
	mm, _ := strconv.Atoi(base[2:4])
	ss, _ := strconv.Atoi(base[4:])
	return hh, hh <= 23 && mm <= 59 && ss <= 59
}
