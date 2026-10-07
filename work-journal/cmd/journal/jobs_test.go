package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"workjournal/internal/capture"
	"workjournal/internal/config"
	"workjournal/internal/scheduler"
	"workjournal/internal/store"
)

type fakeControl struct {
	mu      sync.Mutex
	sent    []string
	sendErr error
}

func (f *fakeControl) Pause() error                    { return f.Send("pause") }
func (f *fakeControl) Resume() error                   { return f.Send("resume") }
func (f *fakeControl) Status() (capture.Status, error) { return capture.Status{}, errors.New("no recorder") }
func (f *fakeControl) Send(c string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, c)
	return f.sendErr
}

func (f *fakeControl) sentCmds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

var _ scheduler.Jobs = (*Jobs)(nil)

func newJobs(t *testing.T) (*Jobs, *fakeControl, *float64) {
	t.Helper()
	p := config.Paths{Root: t.TempDir()}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Default()
	cfg.DataDir = p.Root
	cfg.MinFreeGB = 10
	ctl := &fakeControl{}
	free := new(float64)
	*free = 50
	j := &Jobs{
		Cfg:     func() config.Config { return cfg },
		Paths:   p,
		Store:   st,
		Control: ctl,
		FreeGB:  func(string) (float64, error) { return *free, nil },
		Now:     func() time.Time { return time.Date(2026, 10, 7, 12, 0, 0, 0, time.Local) },
		Logf:    t.Logf,
	}
	return j, ctl, free
}

func diskState(t *testing.T, j *Jobs) string {
	t.Helper()
	v, _, err := j.Store.GetKV("disk_state")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestDiskCheckSendsOnlyOnChange(t *testing.T) {
	j, ctl, free := newJobs(t)
	ctx := context.Background()

	// Plenty of space and no stored state: recorder already starts normal.
	if err := j.DiskCheck(ctx); err != nil {
		t.Fatal(err)
	}
	if len(ctl.sent) != 0 {
		t.Fatalf("sent %v with plenty of space", ctl.sent)
	}

	*free = 6 // below 10, above 5
	j.DiskCheck(ctx)
	j.DiskCheck(ctx)
	if want := []string{"screens_off"}; !equal(ctl.sent, want) {
		t.Fatalf("sent %v, want %v", ctl.sent, want)
	}
	if s := diskState(t, j); s != "screens_off" {
		t.Fatalf("disk_state = %q", s)
	}

	*free = 2
	j.DiskCheck(ctx)
	*free = 20
	j.DiskCheck(ctx)
	j.DiskCheck(ctx)
	if want := []string{"screens_off", "audio_off", "normal"}; !equal(ctl.sent, want) {
		t.Fatalf("sent %v, want %v", ctl.sent, want)
	}
	if s := diskState(t, j); s != "normal" {
		t.Fatalf("disk_state = %q", s)
	}
}

func TestDiskCheckControlErrorStillUpdatesKV(t *testing.T) {
	j, ctl, free := newJobs(t)
	ctl.sendErr = errors.New("dial recorder: no such file")
	*free = 6
	if err := j.DiskCheck(context.Background()); err != nil {
		t.Fatalf("control error returned: %v", err)
	}
	if s := diskState(t, j); s != "screens_off" {
		t.Fatalf("disk_state = %q", s)
	}
}

func TestDiskCheckFreeGBError(t *testing.T) {
	j, ctl, _ := newJobs(t)
	j.FreeGB = func(string) (float64, error) { return 0, errors.New("statfs failed") }
	if err := j.DiskCheck(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if len(ctl.sent) != 0 || diskState(t, j) != "" {
		t.Fatalf("acted on a failed measurement: sent %v", ctl.sent)
	}
}

func TestEnrollSpeakerDiarizationOff(t *testing.T) {
	j, _, _ := newJobs(t)
	cfg := j.Cfg()
	cfg.Diarization = "off"
	j.Cfg = func() config.Config { return cfg }
	err := j.EnrollSpeaker(context.Background(), "m1", "Speaker 1", "Ana")
	if err == nil || err.Error() != "diarization unavailable" {
		t.Fatalf("err = %v", err)
	}
}

func TestReprocessHourMediaExpired(t *testing.T) {
	j, _, _ := newJobs(t)
	err := j.ReprocessHour(context.Background(), "2026-09-01", 9)
	if !errors.Is(err, scheduler.ErrMediaExpired) {
		t.Fatalf("err = %v", err)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
