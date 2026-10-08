package activity

import (
	"errors"
	"io/fs"
	"testing"
	"time"

	"workjournal/internal/config"
)

var loc = time.FixedZone("T", -7*3600)

func at(h, m int) time.Time { return time.Date(2026, 10, 7, h, m, 0, 0, loc) }

func ev(h, m int, typ string, kv ...string) Event {
	e := Event{TS: at(h, m), Type: typ}
	if len(kv) > 0 {
		e.App = kv[0]
	}
	if len(kv) > 1 {
		e.Title = kv[1]
	}
	return e
}

func check(t *testing.T, got []Segment, want []Segment) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d segments %+v, want %d %+v", len(got), got, len(want), want)
	}
	for i := range want {
		g, w := got[i], want[i]
		if !g.Start.Equal(w.Start) || !g.End.Equal(w.End) || g.App != w.App || g.Title != w.Title || g.Kind != w.Kind {
			t.Errorf("seg %d: got %+v want %+v", i, g, w)
		}
	}
}

func TestSegmentsBasic(t *testing.T) {
	evs := []Event{ev(14, 0, "start"), ev(14, 0, "app", "A", "a"), ev(14, 20, "app", "B", "b")}
	got := Segments(evs, at(14, 0), at(15, 0))
	check(t, got, []Segment{
		{at(14, 0), at(14, 20), "A", "a", KindActive},
		{at(14, 20), at(15, 0), "B", "b", KindActive},
	})
}

func TestSegmentsClipToHour(t *testing.T) {
	evs := []Event{ev(13, 30, "start"), ev(13, 30, "app", "A", ""), ev(14, 20, "app", "B", ""), ev(15, 10, "app", "C", "")}
	got := Segments(evs, at(14, 0), at(15, 0))
	check(t, got, []Segment{
		{at(14, 0), at(14, 20), "A", "", KindActive},
		{at(14, 20), at(15, 0), "B", "", KindActive},
	})
}

func TestSegmentsIdleAndPause(t *testing.T) {
	evs := []Event{
		ev(14, 0, "start"), ev(14, 0, "app", "A", "t"),
		ev(14, 10, "idle"), ev(14, 20, "active"),
		ev(14, 30, "pause"), ev(14, 40, "resume"),
	}
	got := Segments(evs, at(14, 0), at(15, 0))
	check(t, got, []Segment{
		{at(14, 0), at(14, 10), "A", "t", KindActive},
		{at(14, 10), at(14, 20), "A", "t", KindIdle},
		{at(14, 20), at(14, 30), "A", "t", KindActive},
		{at(14, 30), at(14, 40), "", "", KindPaused},
		{at(14, 40), at(15, 0), "A", "t", KindActive},
	})
}

func TestSegmentsGapAfterStop(t *testing.T) {
	evs := []Event{ev(14, 0, "start"), ev(14, 0, "app", "A", ""), ev(14, 20, "stop"), ev(14, 40, "start")}
	got := Segments(evs, at(14, 0), at(15, 0))
	check(t, got, []Segment{
		{at(14, 0), at(14, 20), "A", "", KindActive},
		{at(14, 20), at(14, 40), "", "", KindGap},
		{at(14, 40), at(15, 0), "A", "", KindActive},
	})
}

func TestSegmentsSleepIsGap(t *testing.T) {
	// No fresh app event after wake: last app carries forward.
	evs := []Event{ev(14, 0, "start"), ev(14, 0, "app", "A", "x"), ev(14, 10, "sleep"), ev(14, 30, "wake")}
	got := Segments(evs, at(14, 0), at(15, 0))
	check(t, got, []Segment{
		{at(14, 0), at(14, 10), "A", "x", KindActive},
		{at(14, 10), at(14, 30), "", "", KindGap},
		{at(14, 30), at(15, 0), "A", "x", KindActive},
	})
	// Trailing stop is a gap to the end; no events at all is a gap.
	got = Segments([]Event{ev(14, 0, "start"), ev(14, 0, "app", "A", ""), ev(14, 50, "stop")}, at(14, 0), at(15, 0))
	check(t, got, []Segment{{at(14, 0), at(14, 50), "A", "", KindActive}, {at(14, 50), at(15, 0), "", "", KindGap}})
	got = Segments(nil, at(14, 0), at(15, 0))
	check(t, got, []Segment{{at(14, 0), at(15, 0), "", "", KindGap}})
	// Time before the first event is a gap.
	got = Segments([]Event{ev(14, 10, "start"), ev(14, 10, "app", "A", "")}, at(14, 0), at(15, 0))
	check(t, got, []Segment{{at(14, 0), at(14, 10), "", "", KindGap}, {at(14, 10), at(15, 0), "A", "", KindActive}})
}

func TestExcludedSpansDropTitles(t *testing.T) {
	evs := []Event{
		ev(14, 0, "start"), ev(14, 0, "app", "Bank", "Secret acct"),
		ev(14, 10, "excluded"), ev(14, 20, "included"),
	}
	got := Segments(evs, at(14, 0), at(15, 0))
	check(t, got, []Segment{
		{at(14, 0), at(14, 10), "Bank", "Secret acct", KindActive},
		{at(14, 10), at(14, 20), "", "", KindExcluded},
		{at(14, 20), at(15, 0), "Bank", "Secret acct", KindActive},
	})
}

func TestReadEventsSkipsMalformed(t *testing.T) {
	evs, skipped, err := ReadEvents("testdata/malformed.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || skipped != 3 {
		t.Fatalf("got %d events, %d skipped; want 2, 3", len(evs), skipped)
	}
	if evs[1].App != "Arc" || evs[1].Bundle != "company.thebrowser.Browser" || evs[1].Title != "Docs" {
		t.Errorf("bad event: %+v", evs[1])
	}
	_, _, err = ReadEvents("testdata/nope.jsonl")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("want ErrNotExist, got %v", err)
	}
}

func TestDetectMeetingByApp(t *testing.T) {
	segs := []Segment{
		{at(9, 0), at(10, 0), "Code", "", KindActive},
		{at(10, 0), at(10, 45), "zoom.us", "Zoom Meeting", KindActive},
	}
	ms := DetectMeetings(segs, nil, config.Default())
	if len(ms) != 1 {
		t.Fatalf("got %+v", ms)
	}
	m := ms[0]
	if !m.Start.Equal(at(10, 0)) || !m.End.Equal(at(10, 45)) || m.App != "zoom.us" || m.ID != "2026-10-07-1000-zoom-us" {
		t.Errorf("bad meeting %+v", m)
	}
}

func TestDetectMeetingByTitle(t *testing.T) {
	segs := []Segment{{at(11, 0), at(11, 30), "Arc", "Standup - Google Meet", KindActive}}
	ms := DetectMeetings(segs, nil, config.Default())
	if len(ms) != 1 || ms[0].App != "Arc" {
		t.Fatalf("got %+v", ms)
	}
}

func TestDetectMeetingByTwoWaySpeech(t *testing.T) {
	segs := []Segment{{at(13, 0), at(14, 0), "Arc", "call", KindActive}}
	sp := []SpeechSpan{
		{at(13, 10), at(13, 14), "mic"},
		{at(13, 11), at(13, 15), "system"}, // overlap 13:11-13:14 = 3 min
	}
	ms := DetectMeetings(segs, sp, config.Default())
	if len(ms) != 1 {
		t.Fatalf("got %+v", ms)
	}
	m := ms[0]
	if !m.Start.Equal(at(13, 11)) || !m.End.Equal(at(13, 14)) || m.App != "Arc" || m.ID != "2026-10-07-1311-arc" {
		t.Errorf("bad meeting %+v", m)
	}
	// No app context: "call".
	ms = DetectMeetings(nil, sp, config.Default())
	if len(ms) != 1 || ms[0].App != "call" {
		t.Errorf("got %+v", ms)
	}
}

func TestNoMeetingForOneWayAudio(t *testing.T) {
	segs := []Segment{{at(13, 0), at(13, 20), "Arc", "YouTube", KindActive}}
	sp := []SpeechSpan{{at(13, 0), at(13, 20), "system"}}
	if ms := DetectMeetings(segs, sp, config.Default()); len(ms) != 0 {
		t.Errorf("got %+v", ms)
	}
	// Too little overlap is not a meeting either.
	sp = append(sp, SpeechSpan{at(13, 5), at(13, 6), "mic"})
	if ms := DetectMeetings(segs, sp, config.Default()); len(ms) != 0 {
		t.Errorf("got %+v", ms)
	}
}

func TestMeetingsMergeShortGaps(t *testing.T) {
	segs := []Segment{
		{at(10, 0), at(10, 20), "zoom.us", "", KindActive},
		{at(10, 21), at(10, 40), "zoom.us", "", KindActive}, // 1 min gap: merge
		{at(10, 50), at(11, 0), "zoom.us", "", KindActive},  // 10 min gap: separate
	}
	ms := DetectMeetings(segs, nil, config.Default())
	if len(ms) != 2 || !ms[0].Start.Equal(at(10, 0)) || !ms[0].End.Equal(at(10, 40)) || !ms[1].Start.Equal(at(10, 50)) {
		t.Fatalf("got %+v", ms)
	}
	// Two-way speech regions less than 2 min apart count together.
	sp := []SpeechSpan{
		{at(12, 0), at(12, 2), "mic"}, {at(12, 0), at(12, 2), "system"},
		{at(12, 3), at(12, 4), "mic"}, {at(12, 3), at(12, 4), "system"},
	}
	ms = DetectMeetings(nil, sp, config.Default())
	if len(ms) != 1 || !ms[0].End.Equal(at(12, 4)) {
		t.Fatalf("got %+v", ms)
	}
}

func TestMeetingNotSplitByIdle(t *testing.T) {
	segs := []Segment{
		{at(10, 0), at(10, 10), "zoom.us", "Zoom Meeting", KindActive},
		{at(10, 10), at(10, 30), "zoom.us", "Zoom Meeting", KindIdle},
		{at(10, 30), at(10, 45), "zoom.us", "Zoom Meeting", KindActive},
	}
	ms := DetectMeetings(segs, nil, config.Default())
	if len(ms) != 1 || !ms[0].Start.Equal(at(10, 0)) || !ms[0].End.Equal(at(10, 45)) {
		t.Fatalf("got %+v", ms)
	}
}

func TestSlackOnlyMeetingWhenHuddle(t *testing.T) {
	segs := []Segment{
		{at(9, 0), at(9, 20), "Slack", "general", KindActive},
		{at(11, 0), at(11, 10), "Slack", "Huddle with Priya", KindActive},
	}
	ms := DetectMeetings(segs, nil, config.Default())
	if len(ms) != 1 || !ms[0].Start.Equal(at(11, 0)) {
		t.Fatalf("got %+v", ms)
	}
}

// micTalk returns mic speech spans of on seconds every period seconds over
// [from, to).
func micTalk(from, to time.Time, on, period int) []SpeechSpan {
	var out []SpeechSpan
	for t := from; t.Before(to); t = t.Add(time.Duration(period) * time.Second) {
		e := t.Add(time.Duration(on) * time.Second)
		if e.After(to) {
			e = to
		}
		out = append(out, SpeechSpan{t, e, "mic"})
	}
	return out
}

func TestDetectInPersonMeetingFromDenseMicSpeech(t *testing.T) {
	// Screen locked, then idle: neither prevents detection.
	segs := []Segment{
		{at(14, 0), at(14, 5), "loginwindow", "", KindActive},
		{at(14, 5), at(14, 30), "loginwindow", "", KindIdle},
	}
	sp := micTalk(at(14, 2), at(14, 12), 6, 10)                 // 60% speech for 10 min
	sp = append(sp, SpeechSpan{at(14, 2), at(14, 3), "system"}) // a stray system blip
	ms := DetectMeetings(segs, sp, config.Default())
	if len(ms) != 1 {
		t.Fatalf("got %+v", ms)
	}
	m := ms[0]
	if InPersonApp != "In person" || m.App != InPersonApp || m.ID != "2026-10-07-1402-in-person" {
		t.Errorf("bad meeting %+v", m)
	}
	if !m.Start.Equal(at(14, 2)) || m.End.Before(at(14, 11)) || m.End.After(at(14, 12)) {
		t.Errorf("bounds %v-%v", m.Start, m.End)
	}
}

func TestNoInPersonMeetingFromSparseOrShortSpeech(t *testing.T) {
	sparse := micTalk(at(9, 0), at(9, 30), 5, 60) // ~8% speech
	if ms := DetectMeetings(nil, sparse, config.Default()); len(ms) != 0 {
		t.Errorf("sparse: got %+v", ms)
	}
	short := micTalk(at(9, 0), at(9, 3), 8, 10) // dense but 3 minutes
	if ms := DetectMeetings(nil, short, config.Default()); len(ms) != 0 {
		t.Errorf("short: got %+v", ms)
	}
	// System speech alone never makes an in-person meeting.
	sys := []SpeechSpan{{at(9, 0), at(9, 20), "system"}}
	if ms := DetectMeetings(nil, sys, config.Default()); len(ms) != 0 {
		t.Errorf("system: got %+v", ms)
	}
}

func TestInPersonRegionsMergeAcrossShortGap(t *testing.T) {
	sp := micTalk(at(15, 0), at(15, 6), 7, 10)
	sp = append(sp, micTalk(at(15, 7).Add(30*time.Second), at(15, 14), 7, 10)...) // ~90 s gap
	ms := DetectMeetings(nil, sp, config.Default())
	if len(ms) != 1 || !ms[0].Start.Equal(at(15, 0)) || ms[0].End.Before(at(15, 13)) || ms[0].App != InPersonApp {
		t.Fatalf("got %+v", ms)
	}
	// A 10-minute gap keeps them apart.
	sp = micTalk(at(15, 0), at(15, 6), 7, 10)
	sp = append(sp, micTalk(at(15, 16), at(15, 22), 7, 10)...)
	if ms := DetectMeetings(nil, sp, config.Default()); len(ms) != 2 {
		t.Fatalf("got %+v", ms)
	}
}

func TestInPersonConfigKnobs(t *testing.T) {
	sp := micTalk(at(9, 0), at(9, 8), 3, 10) // 30% speech for 8 minutes
	c := config.Default()
	if ms := DetectMeetings(nil, sp, c); len(ms) != 0 {
		t.Errorf("default ratio: got %+v", ms)
	}
	c.InPersonSpeechRatio = 0.25
	if ms := DetectMeetings(nil, sp, c); len(ms) != 1 {
		t.Errorf("ratio 0.25: got %+v", ms)
	}
	c.InPersonMinMinutes = 10
	if ms := DetectMeetings(nil, sp, c); len(ms) != 0 {
		t.Errorf("min 10: got %+v", ms)
	}
}

func TestInPersonYieldsToAppAndTwoWayMeetings(t *testing.T) {
	segs := []Segment{{at(10, 0), at(10, 30), "zoom.us", "Zoom Meeting", KindActive}}
	sp := micTalk(at(10, 0), at(10, 30), 7, 10)
	ms := DetectMeetings(segs, sp, config.Default())
	if len(ms) != 1 || ms[0].App != "zoom.us" {
		t.Fatalf("app: got %+v", ms)
	}
	// Two-way speech (mic + system) is a call, not an in-person meeting.
	sp = append(micTalk(at(11, 0), at(11, 20), 7, 10), SpeechSpan{at(11, 0), at(11, 20), "system"})
	ms = DetectMeetings(nil, sp, config.Default())
	if len(ms) != 1 || ms[0].App != "call" {
		t.Fatalf("two-way: got %+v", ms)
	}
}

func TestSplitAtMeetings(t *testing.T) {
	segs := []Segment{
		{at(14, 0), at(14, 20), "loginwindow", "", KindIdle},
		{at(14, 20), at(14, 40), "Code", "a.go", KindActive},
		{at(14, 40), at(14, 50), "", "", KindPaused},
		{at(14, 50), at(15, 0), "Code", "a.go", KindActive},
	}
	ms := []Meeting{{Start: at(14, 10), End: at(14, 30), App: InPersonApp}, {Start: at(14, 45), End: at(14, 55), App: InPersonApp}}
	got := SplitAtMeetings(segs, ms)
	check(t, got, []Segment{
		{at(14, 0), at(14, 10), "loginwindow", "", KindIdle},
		{at(14, 10), at(14, 20), "loginwindow", "", KindIdle},
		{at(14, 20), at(14, 30), "Code", "a.go", KindActive},
		{at(14, 30), at(14, 40), "Code", "a.go", KindActive},
		{at(14, 40), at(14, 45), "", "", KindPaused},
		{at(14, 45), at(14, 50), "", "", KindPaused},
		{at(14, 50), at(14, 55), "Code", "a.go", KindActive},
		{at(14, 55), at(15, 0), "Code", "a.go", KindActive},
	})
	// Splitting again changes nothing.
	check(t, SplitAtMeetings(got, ms), got)
}

func TestInPersonSplitAroundShortCall(t *testing.T) {
	// A 50-minute conversation with a 3-minute call in the middle.
	sp := micTalk(at(10, 0), at(10, 50), 7, 10)
	sp = append(sp, SpeechSpan{at(10, 20), at(10, 23), "system"})
	ms := DetectMeetings(nil, sp, config.Default())
	if len(ms) != 3 {
		t.Fatalf("got %+v", ms)
	}
	a, call, b := ms[0], ms[1], ms[2]
	if a.App != InPersonApp || !a.Start.Equal(at(10, 0)) || !a.End.Equal(at(10, 20)) {
		t.Errorf("first part %+v", a)
	}
	if call.App != "call" || !call.Start.Equal(at(10, 20)) {
		t.Errorf("call %+v", call)
	}
	if b.App != InPersonApp || !b.Start.Equal(call.End) || b.End.Before(at(10, 49)) || b.ID == a.ID {
		t.Errorf("second part %+v (call ends %v)", b, call.End)
	}
	// A 6-minute conversation mostly covered by a call leaves no in-person meeting.
	sp = micTalk(at(11, 0), at(11, 6), 7, 10)
	sp = append(sp, SpeechSpan{at(11, 1), at(11, 5), "system"})
	ms = DetectMeetings(nil, sp, config.Default())
	if len(ms) != 1 || ms[0].App != "call" {
		t.Fatalf("got %+v", ms)
	}
}
