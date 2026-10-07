// Package scheduler drives the periodic jobs of `journal serve`.
package scheduler

import (
	"context"
	"errors"
	"time"

	"workjournal/internal/config"
	"workjournal/internal/store"
)

// ErrMediaExpired is returned by ReprocessHour when the hour's raw media has
// already been removed by the retention sweep.
var ErrMediaExpired = errors.New("raw media expired")

// Jobs is the work the scheduler triggers.
type Jobs interface {
	ProcessPending(ctx context.Context) error
	ReprocessHour(ctx context.Context, day string, hour int) error
	CloseDay(ctx context.Context, day string) error
	Rollup(ctx context.Context, week string) error
	Retain(ctx context.Context) error
	DiskCheck(ctx context.Context) error
	EnrollSpeaker(ctx context.Context, meetingID, speaker, name string) error
}

// KV keys holding the last completed run of each job.
const (
	kvProcessHour = "last_process_hour"
	kvRetainHour  = "last_retain_hour"
	kvCloseDay    = "last_close_day"
	kvRollupWeek  = "last_rollup_week"
)

// Scheduler runs jobs based on the time passed to Tick.
type Scheduler struct {
	Cfg   func() config.Config
	Jobs  Jobs
	Store *store.Store
	Logf  func(string, ...any)
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

// Tick runs whatever is due at now. A failing job is logged and does not stop
// the others; its marker only advances on success, so it is retried next tick.
func (s *Scheduler) Tick(ctx context.Context, now time.Time) {
	cfg := s.Cfg()
	s.step("disk check", s.Jobs.DiskCheck(ctx))
	s.process(ctx, now)
	closedToday := s.closeDays(ctx, now, cfg)
	if closedToday && isRollupDay(now, cfg) {
		s.rollup(ctx, now)
	}
}

func (s *Scheduler) step(name string, err error) bool {
	if err != nil {
		s.logf("scheduler: %s: %v", name, err)
		return false
	}
	return true
}

func (s *Scheduler) kv(key string) string {
	v, _, err := s.Store.GetKV(key)
	if err != nil {
		s.logf("scheduler: read %s: %v", key, err)
	}
	return v
}

func (s *Scheduler) mark(key, val string) {
	s.step("write "+key, s.Store.SetKV(key, val))
}

// process runs ProcessPending and Retain at most once per hour each. A marker
// advances once the job was attempted, success or failure, so a persistently
// failing hour is retried next hour rather than every minute. Only a cancelled
// context leaves the marker unadvanced.
func (s *Scheduler) process(ctx context.Context, now time.Time) {
	s.hourly(ctx, now, kvProcessHour, "process pending", s.Jobs.ProcessPending)
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

// closeDays closes missed days (oldest first) and then today once the close
// time has passed. It reports whether today is closed.
func (s *Scheduler) closeDays(ctx context.Context, now time.Time, cfg config.Config) bool {
	today := config.DayKey(now)
	last := s.kv(kvCloseDay)
	if last >= today {
		return last == today
	}
	if last != "" && !s.catchUp(ctx, now, last) {
		return false
	}
	if now.Before(closeInstant(now, cfg)) {
		return false
	}
	if !s.step("close "+today, s.Jobs.CloseDay(ctx, today)) {
		return false
	}
	s.mark(kvCloseDay, today)
	return true
}

// catchUp closes every day after last up to yesterday that has data. It
// reports false when a close failed, leaving the marker at the last success.
func (s *Scheduler) catchUp(ctx context.Context, now time.Time, last string) bool {
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
		if len(hours) > 0 && !s.step("close "+day, s.Jobs.CloseDay(ctx, day)) {
			return false
		}
		s.mark(kvCloseDay, day)
	}
	return true
}

func (s *Scheduler) rollup(ctx context.Context, now time.Time) {
	week := config.WeekKey(now)
	if s.kv(kvRollupWeek) == week {
		return
	}
	if s.step("rollup "+week, s.Jobs.Rollup(ctx, week)) {
		s.mark(kvRollupWeek, week)
	}
}
