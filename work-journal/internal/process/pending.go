package process

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"workjournal/internal/activity"
	"workjournal/internal/config"
	"workjournal/internal/media"
	"workjournal/internal/store"
)

// freshAge is how old every raw file of an hour must be before the hour is
// processed; the recorder may still be writing a younger file.
const freshAge = 30 * time.Second

type chunk struct {
	path  string
	start time.Time
	track string
}

type shot struct {
	path string
	t    time.Time
}

// PendingHours returns, oldest first, the hours that have raw data, are not
// `done` or `needs_review`, end no later than the start of the current
// local hour, and whose raw files are all at least 30 s old. The day's
// activity.jsonl counts as a file of the latest hour with data only.
func (p *Pipeline) PendingHours() ([]HourKey, error) {
	now := p.now()
	current := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, time.Local)
	rawRoot := filepath.Dir(p.Paths.RawDay("x"))
	ents, err := os.ReadDir(rawRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []HourKey
	for _, e := range ents {
		if _, err := time.ParseInLocation("2006-01-02", e.Name(), time.Local); err != nil || !e.IsDir() {
			continue
		}
		day := e.Name()
		files, err := p.hourFiles(day)
		if err != nil {
			return nil, err
		}
		hours := make([]int, 0, len(files))
		for h := range files {
			hours = append(hours, h)
		}
		sort.Ints(hours)
		for _, h := range hours {
			start, _, err := hourBounds(day, h)
			if err != nil {
				return nil, err
			}
			if !start.Before(current) {
				continue
			}
			rec, ok, err := p.Store.Hour(day, h)
			if err != nil {
				return nil, err
			}
			if ok && (rec.Status == store.StatusDone || rec.Status == store.StatusNeedsReview) {
				continue
			}
			if fresh, err := anyFresh(files[h], now); err != nil {
				p.logf("process: skipping %s %02d: %v", day, h, err)
				continue
			} else if fresh {
				continue
			}
			out = append(out, HourKey{Day: day, Hour: h})
		}
	}
	return out, nil
}

// hourFiles maps each hour with raw data on day to the raw files belonging
// to it.
func (p *Pipeline) hourFiles(day string) (map[int][]string, error) {
	files := map[int][]string{}
	chunks, err := p.chunks(day)
	if err != nil {
		return nil, err
	}
	for _, c := range chunks {
		files[c.start.Hour()] = append(files[c.start.Hour()], c.path)
	}
	shots, err := p.shots(day)
	if err != nil {
		return nil, err
	}
	for _, s := range shots {
		files[s.t.Hour()] = append(files[s.t.Hour()], s.path)
	}
	events, err := p.events(day)
	if err != nil {
		return nil, err
	}
	for _, e := range events {
		t := e.TS.In(time.Local)
		if config.DayKey(t) != day {
			continue
		}
		if _, ok := files[t.Hour()]; !ok {
			files[t.Hour()] = nil
		}
	}
	if len(events) > 0 && len(files) > 0 {
		latest := -1
		for h := range files {
			latest = max(latest, h)
		}
		files[latest] = append(files[latest], p.Paths.Activity(day))
	}
	return files, nil
}

func anyFresh(paths []string, now time.Time) (bool, error) {
	for _, path := range paths {
		fi, err := os.Stat(path)
		if err != nil {
			return false, err
		}
		if now.Sub(fi.ModTime()) < freshAge {
			return true, nil
		}
	}
	return false, nil
}

// events reads the day's activity log; a missing log has no events.
func (p *Pipeline) events(day string) ([]activity.Event, error) {
	evs, skipped, err := activity.ReadEvents(p.Paths.Activity(day))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if skipped > 0 {
		p.logf("process: %s: skipped %d malformed activity lines", day, skipped)
	}
	return evs, nil
}

// chunks lists the day's audio chunks, ordered by start time.
func (p *Pipeline) chunks(day string) ([]chunk, error) {
	dir := p.Paths.Audio(day)
	names, err := readNames(dir)
	if err != nil {
		return nil, err
	}
	var out []chunk
	for _, n := range names {
		start, track, err := media.ChunkStart(day, n, time.Local)
		if err != nil {
			continue // not an audio chunk
		}
		out = append(out, chunk{path: filepath.Join(dir, n), start: start, track: track})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].start.Before(out[j].start) })
	return out, nil
}

// shots lists the day's screenshots (HHMMSS.jpg), ordered by time.
func (p *Pipeline) shots(day string) ([]shot, error) {
	dir := p.Paths.Screens(day)
	names, err := readNames(dir)
	if err != nil {
		return nil, err
	}
	var out []shot
	for _, n := range names {
		stem, ok := strings.CutSuffix(n, ".jpg")
		if !ok || len(stem) != 6 {
			continue
		}
		t, err := time.ParseInLocation("2006-01-02 150405", day+" "+stem, time.Local)
		if err != nil {
			continue
		}
		out = append(out, shot{path: filepath.Join(dir, n), t: t})
	}
	return out, nil
}

func readNames(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", dir, err)
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

func inHour(cs []chunk, start, end time.Time) []chunk {
	var out []chunk
	for _, c := range cs {
		if !c.start.Before(start) && c.start.Before(end) {
			out = append(out, c)
		}
	}
	return out
}
