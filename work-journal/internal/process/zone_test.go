package process

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
	w, err := hourBounds(e.paths, day, 9)
	if err != nil || len(w) != 1 || !w.start().Equal(london(9, 0, 0)) {
		t.Errorf("hour 9 = %v (%v), want from 09:00+01:00", w, err)
	}
	w, err = hourBounds(e.paths, day, 16)
	if err != nil || len(w) != 2 || !w[1].Start.Equal(nyT(16, 0)) || !w.end().Equal(nyT(17, 0)) {
		t.Errorf("hour 16 = %v (%v), want London's 16:00 piece and 16:00–17:00 -04:00", w, err)
	}
	// Hour 15 holds the change: London's whole 15:00 hour, then New York's
	// from the 15:10 change.
	w, err = hourBounds(e.paths, day, 15)
	if err != nil || len(w) != 2 || !w[0].Start.Equal(london(15, 0, 0)) || !w[0].End.Equal(london(16, 0, 0)) ||
		!w[1].Start.Equal(nyT(15, 10)) || !w[1].End.Equal(nyT(16, 0)) {
		t.Errorf("hour 15 = %v (%v), want 15:00–16:00 +01:00 and 15:10–16:00 -04:00", w, err)
	}
	if in := inHour(cs, day, 15); len(in) != 1 || !in[0].start.Equal(nyT(15, 30)) || !w.contains(in[0].start) {
		t.Errorf("hour 15 chunks = %+v, want the 1530 chunk inside the hour", in)
	}
	e.p.Cfg.Diarization = "off"
	doc, err := e.p.Transcribe(context.Background(), day, 15)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(doc.Segments, func(g activity.Segment) bool { return g.App == "Arc" && g.Start.Equal(nyT(15, 10)) }) {
		t.Errorf("hour 15 segments = %+v, want Arc from 15:10-04:00", doc.Segments)
	}
}

// Spring forward overnight in New York: the last event before sleep is
// EST, the wake at 08:10 is EDT. Hour 8 is EDT's and keeps its chunk.
func TestWakeAfterSpringForward(t *testing.T) {
	ny := inNewYork(t)
	e := newEnv(t)
	const d = "2027-03-14"
	old := time.Date(2027, 3, 14, 12, 0, 0, 0, ny)
	e.p.Now = func() time.Time { return old.Add(time.Hour) }
	e.activityOn(d, old,
		evAt(time.Date(2027, 3, 14, 0, 30, 0, 0, time.FixedZone("", -5*3600)), "sleep"),
		evAt(time.Date(2027, 3, 14, 8, 10, 0, 0, time.FixedZone("", -4*3600)), "wake"),
		evAt(time.Date(2027, 3, 14, 8, 10, 0, 0, time.FixedZone("", -4*3600)), "app", "Code"))
	path := filepath.Join(e.paths.Audio(d), "0815-mic.m4a")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(path, old, old)
	w, err := hourBounds(e.paths, d, 8)
	if from := time.Date(2027, 3, 14, 8, 10, 0, 0, ny); err != nil || len(w) != 1 || !w.start().Equal(from) || !w.end().Equal(from.Add(50*time.Minute)) {
		t.Fatalf("hour 8 = %v (%v), want 08:10–09:00 EDT", w, err)
	}
	cs, err := e.p.chunks(d)
	if err != nil || len(cs) != 1 || len(inHour(cs, d, 8)) != 1 || !w.contains(cs[0].start) {
		t.Errorf("chunks = %+v (%v), want 0815 in hour 8", cs, err)
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

// rebase shifts a whole cache entry by one amount: a chunk recorded across
// the spring-forward change keeps its later speech where it was, and a
// second pass changes nothing.
func TestRebaseOneShiftPerChunk(t *testing.T) {
	ny := inNewYork(t)
	est, edt := time.FixedZone("", -5*3600), time.FixedZone("", -4*3600)
	start := time.Date(2027, 3, 14, 1, 55, 0, 0, ny) // 01:55 EST
	us := []media.Utterance{
		{Start: time.Date(2027, 3, 14, 1, 56, 0, 0, est), End: time.Date(2027, 3, 14, 1, 57, 0, 0, est)},
		{Start: time.Date(2027, 3, 14, 3, 5, 0, 0, edt), End: time.Date(2027, 3, 14, 3, 6, 0, 0, edt)}, // 02:05 EST
	}
	want := []time.Time{us[0].Start, us[1].Start}
	rebase(us, start)
	for i := range us {
		if !us[i].Start.Equal(want[i]) || us[i].End.Sub(us[i].Start) != time.Minute {
			t.Errorf("utterance %d moved to %v, want %v", i, us[i].Start, want[i])
		}
	}
	// The London chunk cached at its New York reading moves by 5 h, once.
	lon := london(10, 5, 0)
	us = []media.Utterance{{Start: time.Date(2026, 10, 7, 10, 5, 1, 0, edt), End: time.Date(2026, 10, 7, 10, 5, 9, 0, edt)}}
	rebase(us, lon)
	rebase(us, lon)
	if !us[0].Start.Equal(london(10, 5, 1)) || !us[0].End.Equal(london(10, 5, 9)) {
		t.Errorf("London utterance at %v–%v, want 10:05:01–10:05:09 +01:00", us[0].Start, us[0].End)
	}
}

// A flight home: London until 16:30 BST, asleep in the air, New York from
// 15:10 EDT. Wall hours 15 and 16 each hold both clocks. Every instant
// belongs to one hour: no segment is in two hour docs, active minutes add
// up to the time observed, and the flight's gap is filed under the wall
// hours it covered (only its first 30 min, 16:00–16:30 BST, are in hour 16).
func TestFlightHoursPartitionTime(t *testing.T) {
	ny := inNewYork(t)
	e := newEnv(t)
	e.p.Cfg.Diarization = "off"
	nyT := func(h, m int) time.Time { return time.Date(2026, 10, 7, h, m, 0, 0, ny) }
	e.activity(nyT(20, 0),
		evAt(london(13, 0, 0), "start"), evAt(london(13, 0, 0), "app", "Code"),
		evAt(london(14, 0, 0), "app", "Arc"), evAt(london(15, 0, 0), "app", "Slack"),
		evAt(london(16, 0, 0), "app", "Docs"), evAt(london(16, 30, 0), "sleep"),
		evAt(nyT(15, 10), "wake"), evAt(nyT(15, 10), "app", "Mail"),
		evAt(nyT(16, 0), "app", "Notes"), evAt(nyT(16, 30), "stop"))
	var all []activity.Segment
	active := time.Duration(0)
	gapIn := map[int]time.Duration{}
	activeIn := map[int]time.Duration{}
	for h := 13; h <= 16; h++ {
		doc, err := e.p.Transcribe(context.Background(), day, h)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range doc.Segments {
			for _, o := range all {
				if g.Start.Before(o.End) && o.Start.Before(g.End) {
					t.Errorf("hour %d segment %v–%v %s overlaps %v–%v %s", h, g.Start, g.End, g.App, o.Start, o.End, o.App)
				}
			}
			all = append(all, g)
			switch g.Kind {
			case activity.KindActive:
				active += g.End.Sub(g.Start)
				activeIn[h] += g.End.Sub(g.Start)
			case activity.KindGap:
				gapIn[h] += g.End.Sub(g.Start)
			}
		}
	}
	if want := 290 * time.Minute; active != want {
		t.Errorf("active time %v over the hours, want %v (210 min London + 80 min New York)", active, want)
	}
	if want := map[int]time.Duration{13: time.Hour, 14: time.Hour, 15: 110 * time.Minute, 16: time.Hour}; !reflect.DeepEqual(activeIn, want) {
		t.Errorf("active per hour %v, want %v", activeIn, want)
	}
	if gapIn[15] != 0 || gapIn[16] != time.Hour {
		t.Errorf("gap per hour %v, want 0 in 15 and 60 min in 16 (30 of flight, 30 after stop)", gapIn)
	}
	// Nothing of the flight's middle (17:00–20:00 BST) is in these hours.
	for _, g := range all {
		if g.Start.Before(london(20, 0, 0)) && london(17, 0, 0).Before(g.End) {
			t.Errorf("segment %v–%v %s inside the flight", g.Start, g.End, g.Kind)
		}
	}
}
