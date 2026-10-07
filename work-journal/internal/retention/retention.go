// Package retention deletes raw media past its retention window.
package retention

import (
	"errors"
	"fmt"
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

// Sweep deletes raw/<day>/ directories older than days before now. A day that
// still has an hour in pending or transcribed is kept, since its raw media is
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
		if unprocessed(hours) || hasUnrecordedMedia(p, name, hours) {
			continue
		}
		if rerr := os.RemoveAll(p.RawDay(name)); rerr != nil {
			errs = append(errs, rerr)
			continue
		}
		removed = append(removed, name)
	}
	sort.Strings(removed)
	return removed, errors.Join(errs...)
}

func unprocessed(hours []store.HourRecord) bool {
	for _, h := range hours {
		if h.Status == store.StatusPending || h.Status == store.StatusTranscribed {
			return true
		}
	}
	return false
}

// hasUnrecordedMedia reports whether the day's raw audio or screenshots cover
// an hour that has no done or needs_review record, i.e. was never processed.
func hasUnrecordedMedia(p config.Paths, day string, hours []store.HourRecord) bool {
	settled := map[int]bool{}
	for _, h := range hours {
		if h.Status == store.StatusDone || h.Status == store.StatusNeedsReview {
			settled[h.Hour] = true
		}
	}
	for hour := range rawHours(p, day) {
		if !settled[hour] {
			return true
		}
	}
	return false
}

// rawHours returns the hours that have audio chunks or screenshots on disk.
func rawHours(p config.Paths, day string) map[int]bool {
	out := map[int]bool{}
	if entries, err := os.ReadDir(p.Audio(day)); err == nil {
		for _, e := range entries {
			if hh, _, _, _, ok := media.ParseChunkName(e.Name()); ok {
				out[hh] = true
			}
		}
	}
	if entries, err := os.ReadDir(p.Screens(day)); err == nil {
		for _, e := range entries {
			base, ok := strings.CutSuffix(e.Name(), ".jpg")
			if !ok || len(base) != 6 {
				continue
			}
			if hh, err := strconv.Atoi(base[:2]); err == nil && hh >= 0 && hh <= 23 {
				out[hh] = true
			}
		}
	}
	return out
}
