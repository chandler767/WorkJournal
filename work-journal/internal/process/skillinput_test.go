package process

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"workjournal/internal/activity"
	"workjournal/internal/media"
	"workjournal/internal/skills"
)

// chromeLines is window chrome that is on every screenshot.
var chromeLines = []string{"Claude", "File", "Edit", "View", "+ New", "WorkJournal", "Skills guide", "Chandler Mayo • Tessl"}

// repetitiveHour is 300 screenshots over one hour, each the same chrome plus
// one line of content that changes every 30 screenshots.
func repetitiveHour() []media.ScreenText {
	var out []media.ScreenText
	for i := 0; i < 300; i++ {
		lines := append([]string{}, chromeLines...)
		lines = append(lines, fmt.Sprintf("Editing section %d of the plan", i/30))
		// OCR noise in case and spacing must not defeat the comparison.
		if i%2 == 1 {
			lines[1] = "  FILE "
		}
		out = append(out, media.ScreenText{Time: at(10, 0, 0).Add(time.Duration(i*12) * time.Second), Text: strings.Join(lines, "\n")})
	}
	return out
}

func TestCompactOCRKeepsEachDistinctLineOnce(t *testing.T) {
	got := compactOCR(repetitiveHour(), 30000)
	all := strings.Join(got, "\n")
	if n := len(all); n > 30000 {
		t.Fatalf("compacted OCR is %d chars, over the cap", n)
	}
	count := map[string]int{}
	for _, l := range strings.Split(all, "\n") {
		count[strings.ToLower(strings.TrimSpace(l))]++
	}
	for _, l := range chromeLines {
		if c := count[strings.ToLower(l)]; c != 1 {
			t.Errorf("%q kept %d times, want once", l, c)
		}
	}
	for s := 0; s < 10; s++ {
		l := strings.ToLower(fmt.Sprintf("Editing section %d of the plan", s))
		if c := count[l]; c != 1 {
			t.Errorf("%q kept %d times, want once", l, c)
		}
	}
	if len(got) != 10 {
		t.Errorf("got %d entries, want 10 (screenshots with nothing new are dropped): %q", len(got), got)
	}
}

func TestCompactOCRRepeatsLineAfterItLeftTheWindow(t *testing.T) {
	var ss []media.ScreenText
	for i := 0; i < 15; i++ {
		text := fmt.Sprintf("line %d", i)
		if i == 0 || i == 14 {
			text += "\nbuild failed"
		}
		ss = append(ss, media.ScreenText{Time: at(10, i, 0), Text: text})
	}
	got := strings.Join(compactOCR(ss, 30000), "\n")
	if c := strings.Count(got, "build failed"); c != 2 {
		t.Fatalf("a line gone for more than %d screenshots is kept again; count %d in %q", ocrWindow, c, got)
	}
}

func TestCompactOCRSamplesEvenlyToCap(t *testing.T) {
	var ss []media.ScreenText
	for i := 0; i < 100; i++ {
		ss = append(ss, media.ScreenText{Time: at(10, 0, 0).Add(time.Duration(i*30) * time.Second), Text: fmt.Sprintf("%03d %s", i, strings.Repeat("x", 796))})
	}
	got := compactOCR(ss, 8000)
	total := 0
	for _, g := range got {
		total += len(g)
	}
	if total > 8000 || total < 6400 {
		t.Fatalf("total %d chars, want close to but not over 8000", total)
	}
	if !strings.HasPrefix(got[0], "000 ") || !strings.HasPrefix(got[len(got)-1], "099 ") {
		t.Fatalf("first/last screenshots not kept: %q … %q", got[0][:4], got[len(got)-1][:4])
	}
	// Even: no gap between kept screenshots is more than twice the average.
	prev := 0
	for _, g := range got[1:] {
		var n int
		fmt.Sscanf(g, "%d", &n)
		if gap := n - prev; gap > 2*99/(len(got)-1)+1 {
			t.Fatalf("uneven sampling: gap %d between %d and %d", gap, prev, n)
		}
		prev = n
	}
}

func TestCompactOCRTruncatesSingleHugeScreenshot(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&b, "distinct line %d\n", i)
	}
	got := compactOCR([]media.ScreenText{{Time: at(10, 0, 0), Text: b.String()}}, 5000)
	total := 0
	for _, g := range got {
		total += len(g)
	}
	if total == 0 || total > 5000 {
		t.Fatalf("total %d, want 1..5000", total)
	}
}

// bigHourDoc is an hour with 300 screenshots of mostly distinct, long text
// (well over the OCR cap even after compaction) and a modest transcript.
func bigHourDoc() *HourDoc {
	doc := &HourDoc{Day: day, Hour: 10, Segments: []activity.Segment{sg(activity.KindActive, "Code", "main.go", 10, 0, 60)}}
	for i := 0; i < 300; i++ {
		var b strings.Builder
		for j := 0; j < 40; j++ {
			fmt.Fprintf(&b, "screen %d line %d: %s\n", i, j, strings.Repeat("q", 40))
		}
		// One huge unbroken OCR line, as from a wide terminal.
		fmt.Fprintf(&b, "%d %s", i, strings.Repeat("w", 9000))
		doc.Screens = append(doc.Screens, media.ScreenText{Time: at(10, 0, 0).Add(time.Duration(i*12) * time.Second), Text: b.String()})
	}
	for i := 0; i < 50; i++ {
		doc.Transcript = append(doc.Transcript, media.Utterance{Start: at(10, i, 0), End: at(10, i, 5), Track: "mic", Speaker: "me", Text: fmt.Sprintf("utterance %d about the build", i)})
	}
	return doc
}

func TestSummarizeHourInputIsLineOrientedAndCapped(t *testing.T) {
	e := newSumEnv(t)
	e.s.Cfg.InputMaxBytes = 1 << 20 // the size guard is tested separately
	doc := bigHourDoc()
	e.transcribed(doc)
	e.fk.out["summarize-hour"] = hourOut
	e.fk.out["categorize-time"] = `{"segments":[{"index":0,"category":"Deep work","confidence":0.9}]}`
	if err := e.s.SummarizeHour(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(e.fk.calls[0].path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(b, []byte("\n"))
	if len(lines) < 50 {
		t.Fatalf("input has %d lines; want one record per line", len(lines))
	}
	for i, l := range lines {
		if len(l) > maxInputLine {
			t.Fatalf("line %d is %d bytes (max %d): %.80s…", i+1, len(l), maxInputLine, l)
		}
	}
	var in hourInput
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, o := range in.OCR {
		total += len(o)
	}
	if total > e.s.Cfg.OCRCharsPerHour || total == 0 {
		t.Fatalf("ocr total %d chars, cap %d", total, e.s.Cfg.OCRCharsPerHour)
	}
	if len(in.Transcript) != 50 {
		t.Fatalf("transcript has %d lines, want 50", len(in.Transcript))
	}
}

func TestSizeGuardReducesOCRBeforeTranscript(t *testing.T) {
	e := newSumEnv(t)
	e.s.Cfg.InputMaxBytes = 20000
	var logs []string
	e.s.Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	doc := bigHourDoc()
	e.transcribed(doc)
	e.fk.out["summarize-hour"] = hourOut
	e.fk.out["categorize-time"] = `{"segments":[{"index":0,"category":"Deep work","confidence":0.9}]}`
	if err := e.s.SummarizeHour(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(e.fk.calls[0].path)
	if len(b) > 20000 {
		t.Fatalf("input is %d bytes, over input_max_bytes", len(b))
	}
	var in hourInput
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatal(err)
	}
	if len(in.OCR) == 0 {
		t.Fatal("OCR dropped entirely although a smaller cap fits")
	}
	if len(in.Transcript) != 50 {
		t.Fatalf("transcript cut to %d lines; OCR must shrink first", len(in.Transcript))
	}

	// A transcript that alone exceeds the limit: OCR goes, everything else
	// stays, and it is logged.
	e.s.Cfg.InputMaxBytes = 2000
	logs = nil
	e.fk.calls = nil
	if err := e.s.SummarizeHour(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(e.fk.calls[0].path)
	in = hourInput{}
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatal(err)
	}
	if len(in.OCR) != 0 || len(in.Transcript) != 50 {
		t.Fatalf("ocr %d entries, transcript %d lines; want 0 and 50", len(in.OCR), len(in.Transcript))
	}
	if !strings.Contains(strings.Join(logs, "\n"), "summarize-hour input is") {
		t.Fatalf("oversized input not logged: %q", logs)
	}
}

func TestMeetingInputOCRIsCapped(t *testing.T) {
	e := newSumEnv(t)
	doc := bigHourDoc()
	m := activity.Meeting{ID: "2026-10-07-1000-zoom-us", App: "zoom.us", Start: at(10, 0, 0), End: at(10, 59, 0)}
	doc.Meetings, doc.EndedMeetings = []activity.Meeting{m}, []string{m.ID}
	in, _, _, err := e.s.meetingInput(doc, m)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, s := range in.Screens {
		total += len(s)
		if len(s) > ocrEntryMax {
			t.Fatalf("screens entry of %d chars, max %d", len(s), ocrEntryMax)
		}
	}
	if total == 0 || total > e.s.Cfg.OCRCharsPerHour {
		t.Fatalf("screens total %d, cap %d", total, e.s.Cfg.OCRCharsPerHour)
	}
}

func TestMarshalInputSplitsRecordsOverLines(t *testing.T) {
	var long []string
	for i := 0; i < 100; i++ {
		long = append(long, fmt.Sprintf("entry %d %s", i, strings.Repeat("z", 40)))
	}
	b, err := skills.MarshalInput(map[string]any{"a": long, "b": 1})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(b, []byte("\n")) < 100 {
		t.Fatalf("not one record per line: %s", b)
	}
}
