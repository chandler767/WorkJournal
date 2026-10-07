package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"

	"workjournal/internal/capture"
	"workjournal/internal/config"
	"workjournal/internal/diarize"
	"workjournal/internal/journal"
	"workjournal/internal/media"
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
	Cfg        func() config.Config
	Paths      config.Paths
	Store      *store.Store
	Pipeline   *process.Pipeline
	Summarizer *process.SkillSummarizer
	Runner     skills.Runner
	Control    capture.Control
	FreeGB     func(path string) (float64, error)
	Now        func() time.Time // nil: time.Now
	Logf       func(string, ...any)

	// heavy serializes the pipeline and journal jobs (ProcessPending,
	// ReprocessHour, CloseDay, Rollup): the scheduler, the dashboard and the
	// CLI may trigger them concurrently and they share hours and files.
	heavy sync.Mutex

	// sendErr is the last Control send error DiskCheck reported ("" after a
	// successful send); a repeat of it is retried quietly.
	diskMu  sync.Mutex
	sendErr string
}

var _ scheduler.Jobs = (*Jobs)(nil)

func (j *Jobs) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

// pipeline returns a per-call copy of the pipeline and summarizer built from
// the live config, so settings saved on the dashboard (rules, categories,
// whisper model) apply without a restart. The shared components are not
// mutated.
func (j *Jobs) pipeline() *process.Pipeline {
	cfg := j.Cfg()
	p := *j.Pipeline
	p.Cfg = cfg
	if j.Summarizer != nil {
		s := *j.Summarizer
		s.Cfg = cfg
		p.Summarize = &s
	}
	if w, ok := p.Transcriber.(media.Whisper); ok {
		w.Model = modelPath(j.Paths, cfg.WhisperModel)
		p.Transcriber = w
	}
	return &p
}

func (j *Jobs) ProcessPending(ctx context.Context) error {
	j.heavy.Lock()
	defer j.heavy.Unlock()
	return j.pipeline().ProcessPending(ctx)
}

// ReprocessHour resets the hour to pending and runs it through the pipeline
// again. Once retention removed the day's raw media it summarizes the hour
// again from its derived doc, and returns scheduler.ErrMediaExpired only when
// that is gone too.
func (j *Jobs) ReprocessHour(ctx context.Context, day string, hour int) error {
	j.heavy.Lock()
	defer j.heavy.Unlock()
	if _, err := os.Stat(j.Paths.RawDay(day)); errors.Is(err, fs.ErrNotExist) {
		return j.resummarize(ctx, day, hour)
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
	p := j.pipeline()
	doc, err := p.Transcribe(ctx, day, hour)
	if err != nil {
		return err
	}
	if doc.Status != store.StatusTranscribed || p.Summarize == nil {
		return nil
	}
	return p.Summarize.SummarizeHour(ctx, doc)
}

// resummarize runs the summarizer again on the hour's derived doc (raw
// media already removed). Without a derived doc there is nothing left.
func (j *Jobs) resummarize(ctx context.Context, day string, hour int) error {
	doc, err := process.Load(j.Paths.DerivedHour(day, hour))
	if errors.Is(err, fs.ErrNotExist) {
		return scheduler.ErrMediaExpired
	}
	if err != nil {
		return err
	}
	p := j.pipeline()
	if p.Summarize == nil {
		return errors.New("no summarizer configured")
	}
	doc.Day, doc.Hour, doc.Status = day, hour, store.StatusTranscribed
	return p.Summarize.SummarizeHour(ctx, doc)
}

// CloseDay processes pending hours first, at most once per hour (the
// scheduler's hourly marker is shared), then writes the day's journal.
func (j *Jobs) CloseDay(ctx context.Context, day string) error {
	j.heavy.Lock()
	defer j.heavy.Unlock()
	pending := func(ctx context.Context) error {
		return scheduler.ProcessOnce(ctx, j.Store, j.now(), j.pipeline().ProcessPending)
	}
	return journal.CloseDay(ctx, j.Runner, j.Store, j.Paths, day, pending)
}

func (j *Jobs) Rollup(ctx context.Context, week string) error {
	j.heavy.Lock()
	defer j.heavy.Unlock()
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
// command is sent only when the state changes, and the state is recorded only
// once the recorder accepted it, so a failed send is retried next tick. A
// send error is reported once, not on every retry. The
// recorder (re)starts in normal mode; the launcher resets the state then.
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
	j.diskMu.Lock()
	defer j.diskMu.Unlock()
	retrying := j.sendErr != ""
	if !retrying {
		j.Logf("disk: %.1f GB free, recorder %s → %s", free, prev, next)
	}
	if err := j.Control.Send(next); err != nil {
		err = fmt.Errorf("disk: send %s: %w", next, err)
		if err.Error() == j.sendErr {
			return nil // already reported; retried every tick
		}
		j.sendErr = err.Error()
		return err
	}
	j.sendErr = ""
	return j.Store.SetKV(kvDiskState, next)
}

// EnrollSpeaker learns the voice of a meeting speaker the user named and
// relabels that speaker's utterances (process.Pipeline.EnrollSpeaker). It
// returns errDiarizationUnavailable when diarization is off or the speaker
// models are not loaded, and scheduler.ErrMediaExpired when the meeting's
// raw audio is gone.
func (j *Jobs) EnrollSpeaker(ctx context.Context, meetingID, speaker, name string) error {
	if j.Cfg().Diarization == "off" {
		return errDiarizationUnavailable
	}
	j.heavy.Lock()
	defer j.heavy.Unlock()
	p := j.pipeline()
	if _, noop := p.Diarizer.(diarize.NoopDiarizer); noop || p.Diarizer == nil {
		return errDiarizationUnavailable
	}
	m, ok, err := j.Store.Meeting(meetingID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("meeting %s not found", meetingID)
	}
	err = p.EnrollSpeaker(ctx, m, speaker, name)
	if errors.Is(err, process.ErrNoRawAudio) {
		return scheduler.ErrMediaExpired
	}
	return err
}
