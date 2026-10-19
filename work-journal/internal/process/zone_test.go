package process

import (
	"context"
	"reflect"
	"testing"
	"time"

	"workjournal/internal/activity"
	"workjournal/internal/media"
)

// inNewYork makes the Mac's zone America/New_York for the test: the user
// flew home after recording days in London.
func inNewYork(t *testing.T) *time.Location {
	t.Helper()
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	prev := time.Local
	time.Local = ny
	t.Cleanup(func() { time.Local = prev })
	return ny
}

var bst = time.FixedZone("", 3600)

func london(h, m, s int) time.Time { return time.Date(2026, 10, 7, h, m, s, 0, bst) }

// londonDay records hour 10 of day in London (+01:00): a Zoom call from
// 10:05 with a chunk and a screenshot at its start.
func londonDay(e *env, old time.Time) {
	e.activity(old,
		evAt(london(10, 0, 0), "start"), evAt(london(10, 0, 0), "app", "Code", "main.go"),
		evAt(london(10, 5, 0), "app", "zoom.us", "Zoom Meeting"), evAt(london(10, 40, 0), "app", "Code", "main.go"))
	for _, f := range []string{"audio/1005-mic.m4a", "audio/1005-system.m4a", "screens/100600.jpg"} {
		e.file(f, old)
	}
	e.tr.utts["1005-mic"] = []fu{{0, 60, "hello from London"}}
	e.tr.utts["1005-system"] = []fu{{0, 60, "hi there"}}
	e.ocr.texts["100600.jpg"] = "agenda"
}

func TestRecordingZoneAlignsAudioWithActivity(t *testing.T) {
	ny := inNewYork(t)
	e := newEnv(t)
	e.p.Cfg.Diarization = "off"
	old := time.Date(2026, 10, 7, 18, 0, 0, 0, ny)
	e.p.Now = func() time.Time { return time.Date(2026, 10, 7, 18, 30, 0, 0, ny) }
	londonDay(e, old)

	got, err := e.p.PendingHours()
	if err != nil {
		t.Fatal(err)
	}
	if want := []HourKey{{day, 10}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PendingHours = %+v, want %+v (hours of the London wall clock)", got, want)
	}
	doc, err := e.p.Transcribe(context.Background(), day, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Transcript) == 0 || !doc.Transcript[0].Start.Equal(london(10, 5, 0)) {
		t.Fatalf("transcript = %+v, want speech from 10:05 London time", doc.Transcript)
	}
	if h := doc.Transcript[0].Start.Hour(); h != 10 {
		t.Errorf("utterance wall hour %d, want 10", h)
	}
	var zoom *activity.Segment
	for i, g := range doc.Segments {
		if g.App == "zoom.us" {
			zoom = &doc.Segments[i]
		}
	}
	if zoom == nil || !zoom.Start.Equal(london(10, 5, 0)) {
		t.Fatalf("segments = %+v, want zoom.us from 10:05 London time", doc.Segments)
	}
	if u := doc.Transcript[0]; !(u.Start.Before(zoom.End) && zoom.Start.Before(u.End)) {
		t.Errorf("utterance %v–%v does not overlap the call %v–%v", u.Start, u.End, zoom.Start, zoom.End)
	}
	if len(doc.Screens) != 1 || !doc.Screens[0].Time.Equal(london(10, 6, 0)) {
		t.Errorf("screens = %+v, want one at 10:06 London time", doc.Screens)
	}
	if len(doc.Meetings) != 1 || doc.Meetings[0].ID != "2026-10-07-1005-zoom-us" || !doc.Meetings[0].Start.Equal(london(10, 5, 0)) {
		t.Fatalf("meetings = %+v", doc.Meetings)
	}
	m, ok, err := e.st.Meeting("2026-10-07-1005-zoom-us")
	if err != nil || !ok {
		t.Fatalf("meeting record: %v %v", ok, err)
	}
	if _, off := m.Start.Zone(); off != 3600 || m.Start.Hour() != 10 || m.Start.Minute() != 5 {
		t.Errorf("stored meeting start %v, want 10:05+01:00", m.Start)
	}
}

func TestZoneChangeMidDay(t *testing.T) {
	ny := inNewYork(t)
	e := newEnv(t)
	old := time.Date(2026, 10, 7, 20, 0, 0, 0, ny)
	nyT := func(h, m int) time.Time { return time.Date(2026, 10, 7, h, m, 0, 0, ny) }
	// London until the flight; New York from 15:10.
	e.activity(old,
		evAt(london(9, 0, 0), "start"), evAt(london(9, 0, 0), "app", "Code"),
		evAt(nyT(15, 10), "start"), evAt(nyT(15, 10), "app", "Arc"))
	e.file("audio/0930-mic.m4a", old)
	e.file("audio/1530-mic.m4a", old)
	cs, err := e.p.chunks(day)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || !cs[0].start.Equal(london(9, 30, 0)) || !cs[1].start.Equal(nyT(15, 30)) {
		t.Fatalf("chunks = %+v, want 09:30+01:00 and 15:30-04:00", cs)
	}
	s, _, err := hourBounds(e.paths, day, 9)
	if err != nil || !s.Equal(london(9, 0, 0)) {
		t.Errorf("hour 9 starts %v (%v), want 09:00+01:00", s, err)
	}
	s, _, err = hourBounds(e.paths, day, 16)
	if err != nil || !s.Equal(nyT(16, 0)) {
		t.Errorf("hour 16 starts %v (%v), want 16:00-04:00", s, err)
	}
}

// A London day transcribed before this fix cached its speech at the New
// York reading of the chunk names (10:05 EDT); running it again re-bases the
// cached speech on the recorded clock instead of transcribing again.
func TestCachedSpeechRebasedOnRecordingZone(t *testing.T) {
	ny := inNewYork(t)
	e := newEnv(t)
	e.p.Cfg.Diarization = "off"
	old := time.Date(2026, 10, 7, 18, 0, 0, 0, ny)
	londonDay(e, old)
	for _, name := range []string{"1005-mic", "1005-system"} {
		path := e.paths.Audio(day) + "/" + name + ".m4a"
		key, err := statKey(path)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Date(2026, 10, 7, 10, 5, 0, 0, ny) // the old, shifted reading
		track := name[5:]
		e.p.saveChunk(day, path, &chunkEntry{Version: cacheVersion, Key: key, Model: e.p.Cfg.WhisperModel,
			Utterances: []media.Utterance{{Start: start.Add(time.Second), End: start.Add(20 * time.Second), Track: track, Speaker: map[string]string{"mic": "me"}[track], Text: "cached " + track}}})
	}
	doc, err := e.p.Transcribe(context.Background(), day, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.tr.calls) != 0 {
		t.Errorf("transcribed again: %v", e.tr.calls)
	}
	if len(doc.Transcript) == 0 || !doc.Transcript[0].Start.Equal(london(10, 5, 1)) || !doc.Transcript[0].End.Equal(london(10, 5, 20)) {
		t.Fatalf("transcript = %+v, want cached speech at 10:05:01 London time", doc.Transcript)
	}
}
