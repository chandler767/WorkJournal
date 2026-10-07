// Package retention deletes raw media past its retention window.
package retention

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"workjournal/internal/config"
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
		if unprocessed(hours) {
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
