package process

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"workjournal/internal/activity"
	"workjournal/internal/config"
	"workjournal/internal/media"
	"workjournal/internal/skills"
	"workjournal/internal/store"
)

type skillCall struct {
	skill string
	path  string
	input map[string]any
}

// fakeSkills answers each skill with a canned JSON string or error. Like the
// real runner, an output that fails its Validator is ErrInvalidOutput.
type fakeSkills struct {
	out   map[string]string
	errs  map[string]error
	calls []skillCall
}

func (f *fakeSkills) Run(ctx context.Context, skill, inputPath string, out any) error {
	b, err := os.ReadFile(inputPath)
	if err != nil {
		return fmt.Errorf("fake runner: %w", err)
	}
	var in map[string]any
	if err := json.Unmarshal(b, &in); err != nil {
		return fmt.Errorf("fake runner: input %s: %w", inputPath, err)
	}
	f.calls = append(f.calls, skillCall{skill, inputPath, in})
	if err := f.errs[skill]; err != nil {
		return err
	}
	res, ok := f.out[skill]
	if !ok {
		return fmt.Errorf("fake runner: no output for %s", skill)
	}
	if err := json.Unmarshal([]byte(res), out); err != nil {
		return err
	}
	if v, ok := out.(skills.Validator); ok {
		if err := v.Validate(); err != nil {
			return fmt.Errorf("%w: %v", skills.ErrInvalidOutput, err)
		}
	}
	return nil
}

func (f *fakeSkills) skillsCalled() []string {
	var out []string
	for _, c := range f.calls {
		out = append(out, c.skill)
	}
	return out
}

type sumEnv struct {
	t     *testing.T
	paths config.Paths
	st    *store.Store
	fk    *fakeSkills
	s     *SkillSummarizer
}

func newSumEnv(t *testing.T) *sumEnv {
	t.Helper()
	paths := config.Paths{Root: t.TempDir()}
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.DB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Default()
	cfg.DataDir = paths.Root
	fk := &fakeSkills{out: map[string]string{}, errs: map[string]error{}}
	return &sumEnv{t: t, paths: paths, st: st, fk: fk,
		s: &SkillSummarizer{Runner: fk, Store: st, Paths: paths, Cfg: cfg, Logf: t.Logf}}
}

func sg(kind, app, title string, h, m1, m2 int) activity.Segment {
	return activity.Segment{Start: at(h, m1, 0), End: at(h, m2, 0), Kind: kind, App: app, Title: title}
}

const hourOut = `{"narrative":"","timeline":[],"notes":["Designed the checkout flow"],"main_focus":"Checkout design","action_items":[{"text":"Send mockups","owner":"You"}]}`

// transcribed stores the hour as the pipeline would before summarizing.
func (e *sumEnv) transcribed(doc *HourDoc) {
	e.t.Helper()
	doc.Status = store.StatusTranscribed
	for _, m := range doc.Meetings {
		if err := e.st.UpsertMeeting(store.MeetingRecord{ID: m.ID, Day: doc.Day, Start: m.Start, End: m.End, App: m.App, Title: m.Title}); err != nil {
			e.t.Fatal(err)
		}
	}
	if err := e.st.ReplaceSegments(doc.Day, doc.Hour, segmentRecords(doc.Day, doc.Hour, doc.Segments)); err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.ReplaceText(doc.Day, doc.Hour, textDocs(doc)); err != nil {
		e.t.Fatal(err)
	}
	if err := doc.Save(e.paths.DerivedHour(doc.Day, doc.Hour)); err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.UpsertHour(store.HourRecord{Day: doc.Day, Hour: doc.Hour, Status: store.StatusTranscribed, UpdatedAt: at(doc.Hour, 0, 0)}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *sumEnv) hour(h int) store.HourRecord {
	e.t.Helper()
	rec, ok, err := e.st.Hour(day, h)
	if err != nil || !ok {
		e.t.Fatalf("hour %d: ok=%v err=%v", h, ok, err)
	}
	return rec
}

func (e *sumEnv) segments(h int) []store.SegmentRecord {
	e.t.Helper()
	all, err := e.st.SegmentsOn(day)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []store.SegmentRecord
	for _, s := range all {
		if s.Hour == h {
			out = append(out, s)
		}
	}
	return out
}

func (e *sumEnv) search(q string) []store.SearchHit {
	e.t.Helper()
	hits, err := e.st.Search(q, 20)
	if err != nil {
		e.t.Fatal(err)
	}
	return hits
}

func TestSummarizerSkipsCategorizeWhenAllMatched(t *testing.T) {
	e := newSumEnv(t)
	e.s.Cfg.Rules = map[string]string{"Figma": "Design"}
	doc := &HourDoc{Day: day, Hour: 10, Segments: []activity.Segment{
		sg(activity.KindActive, "Figma", "Checkout flow", 10, 0, 40),
		sg(activity.KindIdle, "Figma", "Checkout flow", 10, 40, 50),
		sg(activity.KindExcluded, "", "", 10, 50, 60),
	}, Transcript: []media.Utterance{{Start: at(10, 5, 0), End: at(10, 5, 5), Track: "mic", Speaker: "me", Text: "I'll send the mockups"}}}
	e.transcribed(doc)
	e.fk.out["summarize-hour"] = hourOut

	if err := e.s.SummarizeHour(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	if got := e.fk.skillsCalled(); !reflect.DeepEqual(got, []string{"summarize-hour"}) {
		t.Fatalf("skills called = %v, want only summarize-hour", got)
	}
	rec := e.hour(10)
	if rec.Status != store.StatusDone {
		t.Fatalf("status = %q", rec.Status)
	}
	var hs skills.HourSummary
	if err := json.Unmarshal([]byte(rec.SummaryJSON), &hs); err != nil || hs.MainFocus != "Checkout design" {
		t.Fatalf("summary %q: %v", rec.SummaryJSON, err)
	}
	segs := e.segments(10)
	if len(segs) != 3 {
		t.Fatalf("segments = %+v", segs)
	}
	type cs struct{ cat, src string }
	got := []cs{{segs[0].Category, segs[0].Source}, {segs[1].Category, segs[1].Source}, {segs[2].Category, segs[2].Source}}
	want := []cs{{"Design", "rule"}, {"Break/Idle", "auto"}, {"", "auto"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("categories = %v, want %v", got, want)
	}
	if hits := e.search("checkout"); len(hits) != 1 || hits[0].Kind != "note" || hits[0].Hour != 10 {
		t.Fatalf("note hits = %+v", hits)
	}
	if hits := e.search("mockups"); len(hits) != 1 || hits[0].Kind != "transcript" {
		t.Fatalf("transcript must stay indexed alongside notes: %+v", hits)
	}
	saved, err := Load(e.paths.DerivedHour(day, 10))
	if err != nil || saved.Status != store.StatusDone {
		t.Fatalf("derived doc status = %v err=%v", saved, err)
	}
}

func TestSummarizerHourInputShape(t *testing.T) {
	e := newSumEnv(t)
	// The meeting lies in the gap so the zoom.us segment is left to categorize-time
	// (time inside a meeting is always Meetings).
	m := activity.Meeting{ID: "2026-10-07-1050-zoom-us", Start: at(10, 50, 0), End: at(10, 55, 0), App: "zoom.us", Title: "Zoom Meeting"}
	doc := &HourDoc{Day: day, Hour: 10, Segments: []activity.Segment{
		sg(activity.KindActive, "zoom.us", "Zoom Meeting", 10, 10, 30),
		sg(activity.KindExcluded, "", "", 10, 30, 40),
		sg(activity.KindIdle, "Code", "main.go", 10, 40, 45),
		sg(activity.KindGap, "", "", 10, 45, 60),
	},
		Transcript: []media.Utterance{
			{Start: at(10, 11, 0), End: at(10, 11, 3), Track: "mic", Speaker: "me", Text: "hello"},
			{Start: at(10, 12, 0), End: at(10, 12, 3), Track: "system", Speaker: "Speaker 1", Text: "hi"},
			{Start: at(10, 29, 58), End: at(10, 30, 2), Track: "mic", Speaker: "me", Text: "straddles exclusion"},
			{Start: at(10, 35, 0), End: at(10, 35, 4), Track: "system", Speaker: "Speaker 1", Text: "inside exclusion"},
		},
		Screens:  []media.ScreenText{{Time: at(10, 15, 0), File: "101500.jpg", Text: "Agenda"}},
		Meetings: []activity.Meeting{m},
	}
	e.transcribed(doc)
	e.fk.out["summarize-hour"] = hourOut
	e.fk.out["categorize-time"] = `{"segments":[{"index":0,"category":"Meetings","confidence":0.95}]}`

	if err := e.s.SummarizeHour(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	c := e.fk.calls[0]
	if c.skill != "summarize-hour" {
		t.Fatalf("first call %s", c.skill)
	}
	wantPath := filepath.Join(e.paths.Root, "derived", day, ".inputs", "10-summarize-hour.json")
	if c.path != wantPath {
		t.Fatalf("input path %s, want %s", c.path, wantPath)
	}
	b, _ := json.Marshal(c.input)
	want := `{"day":"2026-10-07","hour":10,` +
		`"meetings":[{"app":"zoom.us","end":"10:55","id":"2026-10-07-1050-zoom-us","start":"10:50"}],` +
		`"ocr":["Agenda"],` +
		`"segments":[{"app":"zoom.us","category":"","index":0,"minutes":20,"start":"10:10","title":"Zoom Meeting"},{"app":"Code","category":"Break/Idle","index":2,"minutes":5,"start":"10:40","title":"main.go"}],` +
		`"transcript":[{"speaker":"You","text":"hello"},{"speaker":"Speaker 1","text":"hi"}]}`
	if string(b) != want {
		t.Fatalf("summarize-hour input\n got %s\nwant %s", b, want)
	}

	if len(e.fk.calls) != 2 || e.fk.calls[1].skill != "categorize-time" {
		t.Fatalf("calls = %v", e.fk.skillsCalled())
	}
	b, _ = json.Marshal(e.fk.calls[1].input)
	want = `{"categories":["Deep work","Meetings","Communication","Admin","Browsing/Research","Break/Idle"],` +
		`"hour_summary":{"action_items":[{"owner":"You","text":"Send mockups"}],"main_focus":"Checkout design","narrative":"","notes":["Designed the checkout flow"],"timeline":[]},` +
		`"segments":[{"app":"zoom.us","index":0,"minutes":20,"title":"Zoom Meeting"}]}`
	if string(b) != want {
		t.Fatalf("categorize-time input\n got %s\nwant %s", b, want)
	}
	segs := e.segments(10)
	if segs[0].Category != "Meetings" || segs[0].Source != "skill" || segs[0].Confidence != 0.95 {
		t.Fatalf("skill category not stored: %+v", segs[0])
	}
	if e.hour(10).Status != store.StatusDone {
		t.Fatal("hour not done")
	}
}

func TestSummarizerMarksNeedsReview(t *testing.T) {
	e := newSumEnv(t)
	doc := &HourDoc{Day: day, Hour: 11, Segments: []activity.Segment{sg(activity.KindActive, "Code", "x.go", 11, 0, 30)}}
	e.transcribed(doc)
	e.fk.errs["summarize-hour"] = fmt.Errorf("%w: summarize-hour: no JSON", skills.ErrInvalidOutput)
	e.fk.out["categorize-time"] = `{"segments":[{"index":0,"category":"Deep work","confidence":0.9}]}`

	if err := e.s.SummarizeHour(context.Background(), doc); err != nil {
		t.Fatalf("invalid output must not fail the hour: %v", err)
	}
	rec := e.hour(11)
	if rec.Status != store.StatusNeedsReview {
		t.Fatalf("status = %q, want needs_review", rec.Status)
	}
	// Categorization still succeeded and is kept.
	if segs := e.segments(11); segs[0].Category != "Deep work" || segs[0].Source != "skill" {
		t.Fatalf("segments = %+v", segs)
	}
}

func TestSummarizerRejectsUnknownCategory(t *testing.T) {
	e := newSumEnv(t)
	doc := &HourDoc{Day: day, Hour: 12, Segments: []activity.Segment{sg(activity.KindActive, "Code", "x.go", 12, 0, 30)}}
	e.transcribed(doc)
	e.fk.out["summarize-hour"] = hourOut
	e.fk.out["categorize-time"] = `{"segments":[{"index":0,"category":"Coding","confidence":0.9}]}`

	if err := e.s.SummarizeHour(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	rec := e.hour(12)
	if rec.Status != store.StatusNeedsReview {
		t.Fatalf("status = %q, want needs_review", rec.Status)
	}
	if !strings.Contains(rec.SummaryJSON, "Checkout design") {
		t.Fatalf("hour summary that succeeded must be kept: %q", rec.SummaryJSON)
	}
	if segs := e.segments(12); segs[0].Category != "" {
		t.Fatalf("unknown category stored: %+v", segs[0])
	}
}

func TestSummarizerRejectsMissingSegmentIndex(t *testing.T) {
	e := newSumEnv(t)
	doc := &HourDoc{Day: day, Hour: 12, Segments: []activity.Segment{
		sg(activity.KindActive, "Code", "x.go", 12, 0, 30),
		sg(activity.KindActive, "Mail", "Inbox", 12, 30, 40),
	}}
	e.transcribed(doc)
	e.fk.out["summarize-hour"] = hourOut
	e.fk.out["categorize-time"] = `{"segments":[{"index":0,"category":"Deep work","confidence":0.9},{"index":7,"category":"Admin","confidence":0.9}]}`

	if err := e.s.SummarizeHour(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	if rec := e.hour(12); rec.Status != store.StatusNeedsReview {
		t.Fatalf("status = %q, want needs_review", rec.Status)
	}
}

func TestSummarizerPropagatesRunnerError(t *testing.T) {
	e := newSumEnv(t)
	doc := &HourDoc{Day: day, Hour: 13, Segments: []activity.Segment{sg(activity.KindActive, "Code", "x.go", 13, 0, 30)}}
	e.transcribed(doc)
	e.fk.errs["summarize-hour"] = fmt.Errorf("%w: summarize-hour: claude: exit status 1", skills.ErrUnavailable)

	if err := e.s.SummarizeHour(context.Background(), doc); err == nil {
		t.Fatal("want error")
	}
	if rec := e.hour(13); rec.Status != store.StatusTranscribed {
		t.Fatalf("status = %q, want transcribed (retry later)", rec.Status)
	}
}

func TestSummarizerSavesMeetingParticipants(t *testing.T) {
	e := newSumEnv(t)
	m := activity.Meeting{ID: "2026-10-07-1340-zoom-us", Start: at(13, 40, 0), End: at(14, 20, 0), App: "zoom.us", Title: "Billing sync"}
	ongoing := activity.Meeting{ID: "2026-10-07-1450-zoom-us", Start: at(14, 50, 0), End: at(15, 0, 0), App: "zoom.us"}
	earlier := &HourDoc{Day: day, Hour: 13, Status: store.StatusDone,
		Transcript: []media.Utterance{
			{Start: at(13, 10, 0), End: at(13, 10, 5), Track: "mic", Speaker: "me", Text: "before the meeting"},
			{Start: at(13, 45, 0), End: at(13, 45, 5), Track: "mic", Speaker: "me", Text: "thanks, Priya"},
		},
		Screens: []media.ScreenText{{Time: at(13, 41, 0), File: "134100.jpg", Text: "Priya N."}},
	}
	if err := earlier.Save(e.paths.DerivedHour(day, 13)); err != nil {
		t.Fatal(err)
	}
	doc := &HourDoc{Day: day, Hour: 14,
		Segments: []activity.Segment{sg(activity.KindActive, "zoom.us", "Zoom Meeting", 14, 0, 60)},
		Transcript: []media.Utterance{
			{Start: at(14, 5, 0), End: at(14, 5, 4), Track: "system", Speaker: "Speaker 1", Text: "ship it Friday"},
			{Start: at(14, 30, 0), End: at(14, 30, 4), Track: "mic", Speaker: "me", Text: "after the meeting"},
		},
		Meetings:      []activity.Meeting{m, ongoing},
		EndedMeetings: []string{m.ID},
	}
	e.transcribed(doc)
	if err := e.st.SetParticipant(store.ParticipantRecord{MeetingID: m.ID, Speaker: "Speaker 2", Name: "Bob", Confidence: 1, Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	e.s.Cfg.Rules = map[string]string{"zoom.us": "Meetings"}
	e.fk.out["summarize-hour"] = hourOut
	e.fk.out["summarize-meeting"] = `{"participants":[{"speaker":"You","name":"","confidence":1},{"speaker":"Speaker 1","name":"Priya","confidence":0.9},{"speaker":"Speaker 2","name":"Rob","confidence":0.4}],` +
		`"summary":"Agreed to ship the invoice export fix Friday.","decisions":["Ship Friday"],"action_items":[]}`

	if err := e.s.SummarizeHour(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	if got := e.fk.skillsCalled(); !reflect.DeepEqual(got, []string{"summarize-hour", "summarize-meeting"}) {
		t.Fatalf("skills called = %v (only the ended meeting is summarized)", got)
	}
	mc := e.fk.calls[1]
	if want := filepath.Join(e.paths.Root, "derived", day, ".inputs", "14-summarize-meeting-"+m.ID+".json"); mc.path != want {
		t.Fatalf("meeting input path %s, want %s", mc.path, want)
	}
	b, _ := json.Marshal(mc.input)
	want := `{"meeting":{"app":"zoom.us","end":"` + m.End.Format(time.RFC3339) + `","id":"` + m.ID + `","start":"` + m.Start.Format(time.RFC3339) + `","title":"Billing sync"},` +
		`"screens":["Priya N."],` +
		`"transcript":[{"speaker":"You","t":"00:05:00","text":"thanks, Priya"},{"speaker":"Speaker 1","t":"00:25:00","text":"ship it Friday"}]}`
	if string(b) != want {
		t.Fatalf("summarize-meeting input\n got %s\nwant %s", b, want)
	}

	ps, err := e.st.Participants(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]store.ParticipantRecord{}
	for _, p := range ps {
		got[p.Speaker] = p
	}
	if p := got["Speaker 1"]; p.Name != "Priya" || p.Confidence != 0.9 || p.Confirmed {
		t.Fatalf("Speaker 1 = %+v", p)
	}
	if p := got["You"]; p.Confirmed || p.Confidence != 1 {
		t.Fatalf("You = %+v", p)
	}
	if p := got["Speaker 2"]; p.Name != "Bob" || !p.Confirmed {
		t.Fatalf("a confirmed participant was overwritten: %+v", p)
	}
	rec, ok, err := e.st.Meeting(m.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	var ms skills.MeetingSummary
	if err := json.Unmarshal([]byte(rec.SummaryJSON), &ms); err != nil || ms.Decisions[0] != "Ship Friday" {
		t.Fatalf("meeting summary %q: %v", rec.SummaryJSON, err)
	}
	if rec.Title != "Billing sync" || !rec.End.Equal(m.End) {
		t.Fatalf("meeting record clobbered: %+v", rec)
	}
	hits := e.search("invoice export")
	if len(hits) != 1 || hits[0].Kind != "meeting" || hits[0].Ref != "meeting/"+m.ID {
		t.Fatalf("meeting hits = %+v", hits)
	}
	if e.hour(14).Status != store.StatusDone {
		t.Fatal("hour not done")
	}
}

func TestSummarizerMeetingInvalidMarksNeedsReview(t *testing.T) {
	e := newSumEnv(t)
	m := activity.Meeting{ID: "2026-10-07-1500-zoom-us", Start: at(15, 0, 0), End: at(15, 20, 0), App: "zoom.us"}
	doc := &HourDoc{Day: day, Hour: 15,
		Segments: []activity.Segment{sg(activity.KindActive, "zoom.us", "Zoom", 15, 0, 20)},
		Meetings: []activity.Meeting{m}, EndedMeetings: []string{m.ID},
	}
	e.transcribed(doc)
	e.s.Cfg.Rules = map[string]string{"zoom": "Meetings"}
	e.fk.out["summarize-hour"] = hourOut
	e.fk.errs["summarize-meeting"] = fmt.Errorf("%w: bad", skills.ErrInvalidOutput)

	if err := e.s.SummarizeHour(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	rec := e.hour(15)
	if rec.Status != store.StatusNeedsReview || !strings.Contains(rec.SummaryJSON, "Checkout design") {
		t.Fatalf("hour = %+v", rec)
	}
}

// Speech overlapping an excluded or paused span never reaches a skill, in
// this hour or in earlier hours a meeting spans.
func TestSummarizerMeetingInputDropsHiddenSpeech(t *testing.T) {
	e := newSumEnv(t)
	m := activity.Meeting{ID: "2026-10-07-1340-zoom-us", Start: at(13, 40, 0), End: at(14, 20, 0), App: "zoom.us"}
	earlier := &HourDoc{Day: day, Hour: 13, Status: store.StatusDone,
		Segments: []activity.Segment{
			sg(activity.KindActive, "zoom.us", "Zoom", 13, 40, 50),
			sg(activity.KindPaused, "", "", 13, 50, 55),
			sg(activity.KindActive, "zoom.us", "Zoom", 13, 55, 60),
		},
		Transcript: []media.Utterance{
			{Start: at(13, 45, 0), End: at(13, 45, 5), Track: "mic", Speaker: "me", Text: "visible early"},
			{Start: at(13, 52, 0), End: at(13, 52, 5), Track: "system", Speaker: "Speaker 1", Text: "during pause"},
		},
	}
	if err := earlier.Save(e.paths.DerivedHour(day, 13)); err != nil {
		t.Fatal(err)
	}
	doc := &HourDoc{Day: day, Hour: 14,
		Segments: []activity.Segment{
			sg(activity.KindActive, "zoom.us", "Zoom", 14, 0, 10),
			sg(activity.KindExcluded, "", "", 14, 10, 15),
			sg(activity.KindActive, "zoom.us", "Zoom", 14, 15, 60),
		},
		Transcript: []media.Utterance{
			{Start: at(14, 5, 0), End: at(14, 5, 4), Track: "system", Speaker: "Speaker 1", Text: "visible late"},
			{Start: at(14, 12, 0), End: at(14, 12, 4), Track: "mic", Speaker: "me", Text: "during exclusion"},
		},
		Meetings: []activity.Meeting{m}, EndedMeetings: []string{m.ID},
	}
	e.transcribed(doc)
	e.s.Cfg.Rules = map[string]string{"zoom": "Meetings"}
	e.fk.out["summarize-hour"] = hourOut
	e.fk.out["summarize-meeting"] = `{"participants":[],"summary":"s","decisions":[],"action_items":[]}`

	if err := e.s.SummarizeHour(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	if len(e.fk.calls) != 2 {
		t.Fatalf("calls = %v", e.fk.skillsCalled())
	}
	hourTr, _ := json.Marshal(e.fk.calls[0].input["transcript"])
	if want := `[{"speaker":"Speaker 1","text":"visible late"}]`; string(hourTr) != want {
		t.Fatalf("hour transcript %s, want %s", hourTr, want)
	}
	meetTr, _ := json.Marshal(e.fk.calls[1].input["transcript"])
	if want := `[{"speaker":"You","t":"00:05:00","text":"visible early"},{"speaker":"Speaker 1","t":"00:25:00","text":"visible late"}]`; string(meetTr) != want {
		t.Fatalf("meeting transcript %s, want %s", meetTr, want)
	}
}

const detailedHourOut = `{"narrative":"Spent the hour reworking the checkout flow in Figma, then reviewed PR 42.","timeline":[` +
	`{"start":"10:00","end":"10:40","app":"Figma","activity":"Reworked the Checkout flow frames"},` +
	`{"start":"10:40","end":"11:00","app":"Safari","activity":"Reviewed PR 42 on GitHub"}],` +
	`"notes":["Redrew the payment step"],"main_focus":"Checkout design","action_items":[]}`

func TestSummarizerStoresAndIndexesNarrativeAndTimeline(t *testing.T) {
	e := newSumEnv(t)
	e.s.Cfg.Rules = map[string]string{"Figma": "Design"}
	doc := &HourDoc{Day: day, Hour: 10, Segments: []activity.Segment{sg(activity.KindActive, "Figma", "Checkout flow", 10, 0, 40)}}
	e.transcribed(doc)
	e.fk.out["summarize-hour"] = detailedHourOut

	if err := e.s.SummarizeHour(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	rec := e.hour(10)
	if rec.Status != store.StatusDone {
		t.Fatalf("status = %q", rec.Status)
	}
	var hs skills.HourSummary
	if err := json.Unmarshal([]byte(rec.SummaryJSON), &hs); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hs.Narrative, "Spent the hour") || len(hs.Timeline) != 2 || hs.Timeline[1].App != "Safari" || hs.Timeline[1].End != "11:00" {
		t.Fatalf("stored summary = %+v", hs)
	}
	if hits := e.search("reworking"); len(hits) != 1 || hits[0].Kind != "note" || hits[0].Hour != 10 {
		t.Fatalf("narrative hits = %+v", hits)
	}
	if hits := e.search("Reviewed GitHub"); len(hits) != 1 || hits[0].Kind != "note" {
		t.Fatalf("timeline activity hits = %+v", hits)
	}
}

func TestSummarizerRejectsTimelineOutsideHour(t *testing.T) {
	for name, tl := range map[string]string{
		"before hour": `{"start":"09:50","end":"10:10","app":"Code","activity":"x"}`,
		"after hour":  `{"start":"10:50","end":"11:10","app":"Code","activity":"x"}`,
		"not HH:MM":   `{"start":"10:5","end":"10:20","app":"Code","activity":"x"}`,
		"bad minutes": `{"start":"10:60","end":"10:70","app":"Code","activity":"x"}`,
		"end before":  `{"start":"10:30","end":"10:20","app":"Code","activity":"x"}`,
		"words":       `{"start":"ten","end":"eleven","app":"Code","activity":"x"}`,
	} {
		t.Run(name, func(t *testing.T) {
			e := newSumEnv(t)
			doc := &HourDoc{Day: day, Hour: 10, Segments: []activity.Segment{sg(activity.KindActive, "Code", "x.go", 10, 0, 30)}}
			e.transcribed(doc)
			e.fk.out["summarize-hour"] = `{"narrative":"n","timeline":[` + tl + `],"notes":[],"main_focus":"f","action_items":[]}`
			e.fk.out["categorize-time"] = `{"segments":[{"index":0,"category":"Deep work","confidence":0.9}]}`
			if err := e.s.SummarizeHour(context.Background(), doc); err != nil {
				t.Fatal(err)
			}
			if rec := e.hour(10); rec.Status != store.StatusNeedsReview || rec.SummaryJSON != "" {
				t.Fatalf("status %q summary %q, want needs_review with no summary", rec.Status, rec.SummaryJSON)
			}
		})
	}
}

func TestSummarizerAcceptsTimelineEndingAtNextHour(t *testing.T) {
	e := newSumEnv(t)
	doc := &HourDoc{Day: day, Hour: 23, Segments: []activity.Segment{sg(activity.KindActive, "Code", "x.go", 23, 0, 30)}}
	e.transcribed(doc)
	e.fk.out["summarize-hour"] = `{"narrative":"n","timeline":[{"start":"23:30","end":"00:00","app":"Code","activity":"x"}],"notes":[],"main_focus":"f","action_items":[]}`
	e.fk.out["categorize-time"] = `{"segments":[{"index":0,"category":"Deep work","confidence":0.9}]}`
	if err := e.s.SummarizeHour(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	if rec := e.hour(23); rec.Status != store.StatusDone {
		t.Fatalf("status = %q", rec.Status)
	}
}

func TestSummarizerCategorizesMeetingTimeAsMeetings(t *testing.T) {
	e := newSumEnv(t)
	m := activity.Meeting{ID: "2026-10-07-1410-in-person", Start: at(14, 10, 0), End: at(14, 45, 0), App: activity.InPersonApp}
	doc := &HourDoc{Day: day, Hour: 14, Meetings: []activity.Meeting{m}, Segments: []activity.Segment{
		sg(activity.KindIdle, "loginwindow", "", 14, 0, 20),
		sg(activity.KindActive, "loginwindow", "", 14, 20, 40),
		sg(activity.KindActive, "Code", "a.go", 14, 40, 59),
	}}
	e.transcribed(doc)
	e.fk.out["summarize-hour"] = hourOut
	e.fk.out["categorize-time"] = `{"segments":[{"index":4,"category":"Deep work","confidence":0.9}]}`

	if err := e.s.SummarizeHour(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	type cs struct {
		from, to int
		cat, src string
	}
	var got []cs
	for _, s := range e.segments(14) {
		got = append(got, cs{s.Start.Minute(), s.End.Minute(), s.Category, s.Source})
	}
	want := []cs{
		{0, 10, "Break/Idle", "auto"},
		{10, 20, "Meetings", "rule"},
		{20, 40, "Meetings", "rule"},
		{40, 45, "Meetings", "rule"},
		{45, 59, "Deep work", "skill"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("segments = %v\nwant %v", got, want)
	}
	for _, c := range e.fk.calls {
		if c.skill != "categorize-time" {
			continue
		}
		segs := c.input["segments"].([]any)
		if len(segs) != 1 || segs[0].(map[string]any)["index"].(float64) != 4 {
			t.Fatalf("categorize-time asked about %v", segs)
		}
	}
}
