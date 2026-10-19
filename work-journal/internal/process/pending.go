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
		clock, err := p.dayClock(day)
		if err != nil {
			return nil, err
		}
		files := p.hourFiles(clock)
		hours := make([]int, 0, len(files))
		for h := range files {
			hours = append(hours, h)
		}
		sort.Ints(hours)
		for _, h := range hours {
			start, _, err := clock.bounds(h)
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
func (p *Pipeline) hourFiles(c *dayClock) map[int][]string {
	day := c.day
	files := map[int][]string{}
	for _, c := range c.chunks {
		files[c.start.Hour()] = append(files[c.start.Hour()], c.path)
	}
	for _, s := range c.shots {
		files[s.t.Hour()] = append(files[s.t.Hour()], s.path)
	}
	events := c.events
	for _, e := range events {
		t := e.TS // its own offset: the recorded wall clock
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
	return files
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

// stateEvents returns the previous day's activity events followed by day's.
// The recorder only logs changes, so the previous day establishes the state
// (app, exclusion, sleep, …) in effect at midnight; Segments clips to its
// own range, so the earlier events never become segments of day.
func (p *Pipeline) stateEvents(c *dayClock) ([]activity.Event, error) {
	d, err := time.ParseInLocation("2006-01-02", c.day, time.Local)
	if err != nil {
		return nil, fmt.Errorf("bad day %q: %w", c.day, err)
	}
	prev, err := p.events(config.DayKey(d.AddDate(0, 0, -1)))
	if err != nil {
		return nil, err
	}
	return append(prev, c.events...), nil
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

// dayClock is one day's activity events, its recording zone, and its raw
// audio chunks and screenshots placed on that clock, read once per scan or
// hour build.
type dayClock struct {
	day    string
	zones  activity.ZoneTimeline
	events []activity.Event // the day's own (see stateEvents for the state)
	chunks []chunk
	shots  []shot
}

// readDayClock reads day's activity log (a missing one has no events) and
// lists its raw files; it also returns the count of malformed log lines.
func readDayClock(paths config.Paths, day string) (*dayClock, int, error) {
	if _, err := time.Parse("2006-01-02", day); err != nil {
		return nil, 0, fmt.Errorf("bad day %q: %w", day, err)
	}
	evs, skipped, err := activity.ReadEvents(paths.Activity(day))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, 0, err
	}
	c := &dayClock{day: day, zones: activity.NewZoneTimeline(evs, time.Local), events: evs}
	if c.chunks, err = listChunks(paths.Audio(day), day, c.zones); err != nil {
		return nil, 0, err
	}
	if c.shots, err = listShots(paths.Screens(day), day, c.zones); err != nil {
		return nil, 0, err
	}
	return c, skipped, nil
}

func (p *Pipeline) dayClock(day string) (*dayClock, error) {
	c, skipped, err := readDayClock(p.Paths, day)
	if err == nil && skipped > 0 {
		p.logf("process: %s: skipped %d malformed activity lines", day, skipped)
	}
	return c, err
}

// bounds is the absolute window of the recorded hour (see
// activity.ZoneTimeline.HourBounds), widened by the day's raw files.
func (c *dayClock) bounds(hour int) (time.Time, time.Time, error) {
	walls := make([]time.Time, 0, len(c.chunks)+len(c.shots))
	for _, ch := range c.chunks {
		walls = append(walls, ch.start)
	}
	for _, s := range c.shots {
		walls = append(walls, s.t)
	}
	return c.zones.HourBounds(c.day, hour, walls)
}

// chunks lists the day's audio chunks, ordered by start time.
func (p *Pipeline) chunks(day string) ([]chunk, error) {
	c, err := p.dayClock(day)
	if err != nil {
		return nil, err
	}
	return c.chunks, nil
}

func listChunks(dir, day string, zones activity.ZoneTimeline) ([]chunk, error) {
	names, err := readNames(dir)
	if err != nil {
		return nil, err
	}
	var out []chunk
	for _, n := range names {
		wall, _, err := media.ChunkStart(day, n, time.UTC)
		if err != nil {
			continue // not an audio chunk
		}
		start, track, _ := media.ChunkStart(day, n, zones.At(wall))
		out = append(out, chunk{path: filepath.Join(dir, n), start: start, track: track})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].start.Before(out[j].start) })
	return out, nil
}

// listShots lists the day's screenshots (HHMMSS.jpg), ordered by time.
func listShots(dir, day string, zones activity.ZoneTimeline) ([]shot, error) {
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
		wall, err := time.ParseInLocation("2006-01-02 150405", day+" "+stem, time.UTC)
		if err != nil {
			continue
		}
		t, _ := time.ParseInLocation("2006-01-02 150405", day+" "+stem, zones.At(wall))
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

// inHour is the chunks of the recorded hour [start, end): start reads h:00
// on its clock, and a window widened over a zone change (see
// dayClock.bounds) still takes only the chunks named in hour h.
func inHour(cs []chunk, start, end time.Time) []chunk {
	var out []chunk
	for _, c := range cs {
		if !c.start.Before(start) && c.start.Before(end) && c.start.Hour() == start.Hour() {
			out = append(out, c)
		}
	}
	return out
}
