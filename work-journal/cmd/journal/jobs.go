package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"workjournal/internal/capture"
	"workjournal/internal/config"
	"workjournal/internal/journal"
	"workjournal/internal/process"
	"workjournal/internal/retention"
	"workjournal/internal/scheduler"
	"workjournal/internal/skills"
	"workjournal/internal/store"
)

// kvDiskState holds the disk guard state last sent to the recorder.
const kvDiskState = "disk_state"

var errDiarizationUnavailable = errors.New("diarization unavailable")

// Jobs implements scheduler.Jobs on top of the pipeline, the journal skills
// and the recorder's control socket.
type Jobs struct {
	Cfg      func() config.Config
	Paths    config.Paths
	Store    *store.Store
	Pipeline *process.Pipeline
	Runner   skills.Runner
	Control  capture.Control
	FreeGB   func(path string) (float64, error)
	Now      func() time.Time // nil: time.Now
	Logf     func(string, ...any)
}

var _ scheduler.Jobs = (*Jobs)(nil)

func (j *Jobs) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

func (j *Jobs) ProcessPending(ctx context.Context) error {
	return j.Pipeline.ProcessPending(ctx)
}

// ReprocessHour resets the hour to pending and runs it through the pipeline
// again. It returns scheduler.ErrMediaExpired once retention removed the day.
func (j *Jobs) ReprocessHour(ctx context.Context, day string, hour int) error {
	if _, err := os.Stat(j.Paths.RawDay(day)); errors.Is(err, fs.ErrNotExist) {
		return scheduler.ErrMediaExpired
	} else if err != nil {
		return err
	}
	rec, _, err := j.Store.Hour(day, hour)
	if err != nil {
		return err
	}
	rec.Day, rec.Hour, rec.Status, rec.UpdatedAt = day, hour, store.StatusPending, j.now()
	if err := j.Store.UpsertHour(rec); err != nil {
		return err
	}
	doc, err := j.Pipeline.Transcribe(ctx, day, hour)
	if err != nil {
		return err
	}
	if doc.Status != store.StatusTranscribed || j.Pipeline.Summarize == nil {
		return nil
	}
	return j.Pipeline.Summarize.SummarizeHour(ctx, doc)
}

func (j *Jobs) CloseDay(ctx context.Context, day string) error {
	return journal.CloseDay(ctx, j.Runner, j.Store, j.Paths, day, j.Pipeline.ProcessPending)
}

func (j *Jobs) Rollup(ctx context.Context, week string) error {
	return journal.Rollup(ctx, j.Runner, j.Store, j.Paths, week)
}

func (j *Jobs) Retain(ctx context.Context) error {
	removed, err := retention.Sweep(j.Paths, j.Store, j.now(), j.Cfg().RetentionDays)
	if len(removed) > 0 {
		j.Logf("retention: removed raw media for %v", removed)
	}
	return err
}

// DiskCheck tells the recorder to shed screenshots or audio when free space
// falls below min_free_gb, and to return to normal once it recovers. A
// command is sent only when the state changes; the new state is recorded
// even if the recorder can't be reached.
func (j *Jobs) DiskCheck(ctx context.Context) error {
	free, err := j.FreeGB(j.Paths.Root)
	if err != nil {
		return fmt.Errorf("free space: %w", err)
	}
	prev, ok, err := j.Store.GetKV(kvDiskState)
	if err != nil {
		return err
	}
	if !ok {
		prev = retention.StateNormal // the recorder starts in normal
	}
	next, send := retention.DiskAction(prev, free, float64(j.Cfg().MinFreeGB))
	if !send {
		return nil
	}
	j.Logf("disk: %.1f GB free, recorder %s → %s", free, prev, next)
	if err := j.Control.Send(next); err != nil {
		j.Logf("disk: send %s: %v", next, err)
	}
	return j.Store.SetKV(kvDiskState, next)
}

// EnrollSpeaker would store a voice embedding for a named speaker. No
// diarizer ships yet (Ruling 28), so there is never an embedding to enrol.
func (j *Jobs) EnrollSpeaker(ctx context.Context, meetingID, speaker, name string) error {
	return errDiarizationUnavailable
}
