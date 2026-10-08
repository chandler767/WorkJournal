// Package scheduler drives the periodic jobs of `journal serve`.
package scheduler

import (
	"context"
	"errors"
	"strconv"
	"time"

	"workjournal/internal/config"
	"workjournal/internal/store"
)

// ErrMediaExpired is returned by ReprocessHour when the hour's raw media has
// already been removed by the retention sweep and nothing derived is left.
var ErrMediaExpired = errors.New("raw media expired")

// Jobs is the work the scheduler triggers.
type Jobs interface {
	ProcessPending(ctx context.Context) error
	// ProcessCurrent summarizes the in-progress hour so far.
	ProcessCurrent(ctx context.Context) error
	// ProcessNow asks the recorder to finish its current files, then
	// processes finished hours and the in-progress hour; it returns the
	// time processed up to.
	ProcessNow(ctx context.Context) (time.Time, error)
	ReprocessHour(ctx context.Context, day string, hour int) error
	CloseDay(ctx context.Context, day string) error
	Rollup(ctx context.Context, week string) error
	Retain(ctx context.Context) error
	DiskCheck(ctx context.Context) error
	EnrollSpeaker(ctx context.Context, meetingID, speaker, name string) error
}

// KV keys holding the last run of each job.
const (
	kvProcessHour         = "last_process_hour"
	kvPartialRun          = "last_partial_run"
	kvRetainHour          = "last_retain_hour"
	kvCloseDay            = "last_close_day"
	kvCloseAttemptHour    = "last_close_attempt_hour"
	kvRollupWeek          = "last_rollup_week"
	kvRollupAttemptHour   = "last_rollup_attempt_hour"
	kvCloseFailuresPrefix = "close_failures:"
)

// SettleDelay is how far into an hour the previous hour counts as finished:
// the recorder may still be writing the last chunk or screenshot of the
// previous hour just after the boundary, and the pipeline skips an hour
// whose files are fresh.
const SettleDelay = 2 * time.Minute

// maxCloseFailures is how many failed closes a day gets before the catch-up
// moves past it; the dashboard then offers a manual retry.
const maxCloseFailures = 3

// rollupCatchUpWeeks bounds how many missed weekly rollups are caught up.
const rollupCatchUpWeeks = 4

// CloseFailuresKey is the KV key counting failed closes of day.
func CloseFailuresKey(day string) string { return kvCloseFailuresPrefix + day }

// Scheduler runs jobs based on the time passed to Tick.
type Scheduler struct {
	Cfg   func() config.Config
	Jobs  Jobs
	Store *store.Store
	Logf  func(string, ...any)
	// HasDailyJournal reports whether day has a journal entry; a week is
	// rolled up only when one of its days has. Nil means assume yes.
	HasDailyJournal func(day string) bool
}

func (s *Scheduler) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// Run ticks immediately (covering wake from sleep) and then every interval
// until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context, every time.Duration) {
	s.Tick(ctx, time.Now())
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.Tick(ctx, now)
		}
	}
}

// Tick runs whatever is due at now. A failing job is logged and does not
// stop the others. Every job is attempted at most once per local hour, so a
// persistent failure is retried hourly rather than every tick.
func (s *Scheduler) Tick(ctx context.Context, now time.Time) {
	cfg := s.Cfg()
	s.step("disk check", s.Jobs.DiskCheck(ctx))
	s.process(ctx, now)
	s.partial(ctx, now, cfg)
	s.closeDays(ctx, now, cfg)
	s.rollups(ctx, now, cfg)
}

func (s *Scheduler) step(name string, err error) bool {
	if err != nil {
		s.logf("scheduler: %s: %v", name, err)
		return false
	}
	return true
}

func (s *Scheduler) kv(key string) string { return getKV(s.Store, key, s.logf) }

func getKV(st *store.Store, key string, logf func(string, ...any)) string {
	v, _, err := st.GetKV(key)
	if err != nil && logf != nil {
		logf("scheduler: read %s: %v", key, err)
	}
	return v
}

func (s *Scheduler) mark(key, val string) {
	s.step("write "+key, s.Store.SetKV(key, val))
}

// settled reports whether now is at least SettleDelay into its hour.
func settled(now time.Time) bool {
	start := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())
	return !now.Before(start.Add(SettleDelay))
}

// process runs ProcessPending and Retain at most once per hour each. A marker
// advances once the job was attempted, success or failure, so a persistently
// failing hour is retried next hour rather than every minute. Only a cancelled
// context leaves the marker unadvanced. ProcessPending waits until the hour
// has settled so the previous hour's last files are no longer fresh.
func (s *Scheduler) process(ctx context.Context, now time.Time) {
	if settled(now) {
		s.hourly(ctx, now, kvProcessHour, "process pending", s.Jobs.ProcessPending)
	}
	s.hourly(ctx, now, kvRetainHour, "retain", s.Jobs.Retain)
}

func (s *Scheduler) hourly(ctx context.Context, now time.Time, key, name string, job func(context.Context) error) {
	hour := hourKey(now)
	if s.kv(key) == hour {
		return
	}
	s.step(name, job(ctx))
	if ctx.Err() == nil {
		s.mark(key, hour)
	}
}

// partial summarizes the in-progress hour once per process_interval_minutes
// slot of the clock after the first (with 15: at :15, :30 and :45; the :00
// slot would find almost nothing of the new hour). Finished hours are left to
// the hourly run, which bounds their retries to one an hour. The attempt is
// recorded even when it fails, so a failure waits for the next slot. An
// interval of 60 disables partial runs.
func (s *Scheduler) partial(ctx context.Context, now time.Time, cfg config.Config) {
	every := cfg.ProcessIntervalMinutes
	if every <= 0 || every >= 60 || now.Minute() < every {
		return
	}
	slot := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute()/every*every, 0, 0, now.Location())
	if last, err := time.Parse(time.RFC3339, s.kv(kvPartialRun)); err == nil && !last.Before(slot) {
		return
	}
	s.step("process current", s.Jobs.ProcessCurrent(ctx))
	if ctx.Err() == nil {
		s.mark(kvPartialRun, now.Format(time.RFC3339))
	}
}

// ProcessOnce runs job unless ProcessPending was already attempted in now's
// hour, sharing the scheduler's hourly marker. The marker is only set once
// the hour has settled, so a run in the first minutes of an hour (a manual
// close) does not stop the hourly run from processing the previous hour.
func ProcessOnce(ctx context.Context, st *store.Store, now time.Time, job func(context.Context) error) error {
	hour := hourKey(now)
	v, _, err := st.GetKV(kvProcessHour)
	if err != nil {
		return err
	}
	if v == hour {
		return nil
	}
	jerr := job(ctx)
	if ctx.Err() == nil && settled(now) {
		if err := st.SetKV(kvProcessHour, hour); err != nil {
			return errors.Join(jerr, err)
		}
	}
	return jerr
}

// attemptGuard lets a job make one attempt per hour under key. take reports
// whether the attempt may go ahead; done records it once made.
type attemptGuard struct {
	s     *Scheduler
	key   string
	hour  string
	taken bool
}

func (g *attemptGuard) take() bool {
	if g.taken {
		return true
	}
	if g.s.kv(g.key) == g.hour {
		return false
	}
	g.taken = true
	return true
}

func (g *attemptGuard) done(ctx context.Context) {
	if g.taken && ctx.Err() == nil {
		g.s.mark(g.key, g.hour)
	}
}

// closeDays closes missed days (oldest first) and then today once the close
// time plus SettleDelay has passed. Close attempts are made at most once per
// hour; a day failing maxCloseFailures times is skipped so later days proceed.
func (s *Scheduler) closeDays(ctx context.Context, now time.Time, cfg config.Config) {
	if !settled(now) {
		return
	}
	today := config.DayKey(now)
	last := s.kv(kvCloseDay)
	if last >= today {
		return
	}
	g := &attemptGuard{s: s, key: kvCloseAttemptHour, hour: hourKey(now)}
	defer g.done(ctx)
	if last != "" && !s.catchUp(ctx, now, last, g) {
		return
	}
	if now.Before(closeInstant(now, cfg).Add(SettleDelay)) || !g.take() {
		return
	}
	s.closeOne(ctx, today)
}

// catchUp closes every day after last up to yesterday that has data. It
// reports false when it stopped early (a close failed or no attempt is left
// this hour), leaving the marker at the last day handled.
func (s *Scheduler) catchUp(ctx context.Context, now time.Time, last string, g *attemptGuard) bool {
	d, err := parseDay(last, now.Location())
	if err != nil {
		s.logf("scheduler: bad %s %q: %v", kvCloseDay, last, err)
		return false
	}
	yesterday := config.DayKey(now.AddDate(0, 0, -1))
	for d = d.AddDate(0, 0, 1); config.DayKey(d) <= yesterday; d = d.AddDate(0, 0, 1) {
		day := config.DayKey(d)
		hours, err := s.Store.HoursOn(day)
		if !s.step("hours on "+day, err) {
			return false
		}
		if len(hours) == 0 {
			s.mark(kvCloseDay, day)
			continue
		}
		if !g.take() || !s.closeOne(ctx, day) {
			return false
		}
	}
	return true
}

// closeOne closes day and reports whether the scheduler may move past it:
// on success, or once the day has failed maxCloseFailures times.
func (s *Scheduler) closeOne(ctx context.Context, day string) bool {
	err := s.Jobs.CloseDay(ctx, day)
	if err == nil {
		s.mark(kvCloseDay, day)
		if s.kv(CloseFailuresKey(day)) != "" {
			s.mark(CloseFailuresKey(day), "0")
		}
		return true
	}
	s.logf("scheduler: close %s: %v", day, err)
	if ctx.Err() != nil {
		return false
	}
	n, _ := strconv.Atoi(s.kv(CloseFailuresKey(day)))
	n++
	s.mark(CloseFailuresKey(day), strconv.Itoa(n))
	if n < maxCloseFailures {
		return false
	}
	s.logf("scheduler: close %s failed %d times; skipping it (retry from the dashboard)", day, n)
	s.mark(kvCloseDay, day)
	return true
}

// rollups writes the weekly rollup of every week (oldest first, at most
// rollupCatchUpWeeks back) whose rollup weekday's close time has passed,
// that is newer than the last rollup, whose rollup day has been closed and
// that has at least one daily journal. Attempts are made at most once per
// hour; a failure stops the walk until the next hour.
func (s *Scheduler) rollups(ctx context.Context, now time.Time, cfg config.Config) {
	latest := latestRollupDay(now, cfg)
	last := s.kv(kvRollupWeek)
	lastClose := s.kv(kvCloseDay)
	g := &attemptGuard{s: s, key: kvRollupAttemptHour, hour: hourKey(now)}
	defer g.done(ctx)
	for i := rollupCatchUpWeeks - 1; i >= 0; i-- {
		rday := latest.AddDate(0, 0, -7*i)
		week := config.WeekKey(rday)
		if last != "" && week <= last {
			continue
		}
		if lastClose < config.DayKey(rday) {
			return // rollup day not closed yet; later weeks neither
		}
		if !s.weekHasJournal(rday) {
			continue
		}
		if !g.take() {
			return
		}
		if !s.step("rollup "+week, s.Jobs.Rollup(ctx, week)) {
			return
		}
		s.mark(kvRollupWeek, week)
	}
}

func (s *Scheduler) weekHasJournal(inWeek time.Time) bool {
	if s.HasDailyJournal == nil {
		return true
	}
	monday := inWeek.AddDate(0, 0, -((int(inWeek.Weekday()) + 6) % 7))
	for i := range 7 {
		if s.HasDailyJournal(config.DayKey(monday.AddDate(0, 0, i))) {
			return true
		}
	}
	return false
}
