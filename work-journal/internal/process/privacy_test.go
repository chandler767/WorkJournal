package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"workjournal/internal/media"
	"workjournal/internal/skills"
	"workjournal/internal/store"
)

// privacyRunner fakes the privacy-filter skill: respond builds the output
// from the decoded input. Like the real runner, an output failing its
// Validator is ErrInvalidOutput.
type privacyRunner struct {
	inputs  []privacyInput
	paths   []string
	respond func(in privacyInput) string
	err     error
}

func (r *privacyRunner) Run(ctx context.Context, skill, path string, out any) error {
	if skill != "privacy-filter" {
		return fmt.Errorf("privacy runner: unexpected skill %s", skill)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("privacy runner: %w", err)
	}
	var in privacyInput
	if err := json.Unmarshal(b, &in); err != nil {
		return fmt.Errorf("privacy runner: %w", err)
	}
	r.inputs, r.paths = append(r.inputs, in), append(r.paths, path)
	if r.err != nil {
		return r.err
	}
	res := `{"drop":[],"redact":[]}`
	if r.respond != nil {
		res = r.respond(in)
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

// dropping answers with one drop range per input line whose text is in
// texts, and a redaction for each line whose text is a key of redact.
func dropping(reason string, conf float64, texts []string, redact map[string]string) func(privacyInput) string {
	return func(in privacyInput) string {
		out := skills.PrivacyResult{Drop: []skills.DropRange{}, Redact: []skills.Redaction{}}
		for _, l := range in.Utterances {
			if l.Context {
				continue
			}
			if slices.Contains(texts, l.Text) {
				out.Drop = append(out.Drop, skills.DropRange{From: l.Index, To: l.Index, Reason: reason, Confidence: conf})
			}
			if r, ok := redact[l.Text]; ok {
				out.Redact = append(out.Redact, skills.Redaction{Index: l.Index, Text: r})
			}
		}
		b, _ := json.Marshal(out)
		return string(b)
	}
}

// privacyFixture: hour 13 in Code, one mic chunk with work talk, a family
// call and an account number.
func privacyFixture(e *env) *privacyRunner {
	e.activity(old, ev(13, 0, "start"), ev(13, 0, "app", "Code", "main.go"))
	e.file("audio/1300-mic.m4a", old)
	e.tr.utts["1300-mic"] = []fu{
		{1, 3, "the parser needs a retry"},
		{60, 62, "love you mom"},
		{63, 65, "dinner at seven then"},
		{240, 245, "the account number is 4417 1234"},
	}
	e.p.Cfg.Diarization = "off"
	r := &privacyRunner{respond: dropping("personal_non_work", 0.9,
		[]string{"love you mom", "dinner at seven then"},
		map[string]string{"the account number is 4417 1234": "the account number is [redacted]"})}
	e.p.Privacy = r
	return r
}

func transcriptTexts(d *HourDoc) []string {
	var out []string
	for _, u := range d.Transcript {
		out = append(out, u.Text)
	}
	return out
}

func TestPrivacyDropsAndRedacts(t *testing.T) {
	e := newEnv(t)
	r := privacyFixture(e)
	if err := e.p.ProcessPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.inputs) != 1 {
		t.Fatalf("privacy-filter ran %d times", len(r.inputs))
	}
	in := r.inputs[0]
	if in.Day != day || in.Hour != 13 || len(in.Utterances) != 4 {
		t.Fatalf("input = %+v", in)
	}
	if l := in.Utterances[1]; l.Index != 1 || l.Time != "13:01:00" || l.Track != "mic" || l.Speaker != "You" || l.Context {
		t.Errorf("line 1 = %+v", l)
	}
	if _, err := os.Stat(r.paths[0]); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("unfiltered skill input %s left behind (%v)", r.paths[0], err)
	}
	doc, err := Load(e.paths.DerivedHour(day, 13))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"the parser needs a retry", "the account number is [redacted]"}
	if got := transcriptTexts(doc); !reflect.DeepEqual(got, want) {
		t.Fatalf("transcript = %q, want %q", got, want)
	}
	if len(doc.Removed) != 1 || !doc.Removed[0].Start.Equal(at(13, 1, 0)) || !doc.Removed[0].End.Equal(at(13, 1, 5)) ||
		doc.Removed[0].Reason != "personal_non_work" {
		t.Fatalf("removed = %+v", doc.Removed)
	}
	assertDerivedLacks(t, e, 13, "parser", "love you mom", "dinner", "4417")
	for _, q := range []string{"mom", "dinner", "4417"} {
		hits, err := e.st.Search(q, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 0 {
			t.Errorf("search %q found %+v", q, hits)
		}
	}
	if hits, _ := e.st.Search("parser", 10); len(hits) != 1 {
		t.Errorf("search parser: %+v", hits)
	}
	if len(e.sum.calls) != 1 {
		t.Errorf("summarizer calls = %v", e.sum.calls)
	}
}

func TestPrivacyRedactionOff(t *testing.T) {
	e := newEnv(t)
	privacyFixture(e)
	e.p.Cfg.RedactIdentifiers = false
	doc, err := e.p.Transcribe(context.Background(), day, 13)
	if err != nil {
		t.Fatal(err)
	}
	if got := transcriptTexts(doc); !slices.Contains(got, "the account number is 4417 1234") || slices.Contains(got, "love you mom") {
		t.Fatalf("transcript = %q", got)
	}
}

func TestPrivacyConfidenceThreshold(t *testing.T) {
	e := newEnv(t)
	r := privacyFixture(e)
	r.respond = dropping("bystander", 0.6, []string{"love you mom", "dinner at seven then"}, nil)
	doc, err := e.p.Transcribe(context.Background(), day, 13)
	if err != nil {
		t.Fatal(err)
	}
	if got := transcriptTexts(doc); len(got) != 4 || len(doc.Removed) != 0 {
		t.Fatalf("0.6 < 0.7 dropped: %q, removed %+v", got, doc.Removed)
	}
	// The stored decisions keep their confidence: a lower threshold applies
	// without asking again.
	e.p.Cfg.PrivacyDropConfidence = 0.5
	doc, err = e.p.Transcribe(context.Background(), day, 13)
	if err != nil {
		t.Fatal(err)
	}
	if got := transcriptTexts(doc); len(got) != 2 || len(doc.Removed) != 1 || doc.Removed[0].Reason != "bystander" {
		t.Fatalf("at 0.5: %q, removed %+v", got, doc.Removed)
	}
	if len(r.inputs) != 1 {
		t.Fatalf("privacy-filter ran %d times, want 1 (decisions cached)", len(r.inputs))
	}
}

func TestPrivacyTogglesOffSkipSkill(t *testing.T) {
	e := newEnv(t)
	r := privacyFixture(e)
	e.p.Cfg.PrivacyFilter = false
	doc, err := e.p.Transcribe(context.Background(), day, 13)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.inputs) != 0 {
		t.Fatalf("skill called with privacy_filter off")
	}
	if len(doc.Transcript) != 4 || len(doc.Removed) != 0 {
		t.Fatalf("transcript = %q", transcriptTexts(doc))
	}
}

func TestPrivacySkippedWithoutSpeech(t *testing.T) {
	e := newEnv(t)
	r := privacyFixture(e)
	e.tr.utts["1300-mic"] = nil
	if _, err := e.p.Transcribe(context.Background(), day, 13); err != nil {
		t.Fatal(err)
	}
	if len(r.inputs) != 0 {
		t.Fatalf("skill called for an hour without speech")
	}
}

// A conversation between two other voices near the laptop is detected as
// an in-person meeting; once it is removed, detection runs again and the
// meeting is gone.
func TestPrivacyDropRemovesBystanderMeeting(t *testing.T) {
	e := newEnv(t)
	inPersonFixture(e)
	e.p.Cfg.Diarization = "off" // the in-person meeting is kept without diarization
	var bystander []string
	for _, c := range []string{"1400-mic", "1405-mic"} {
		for _, u := range e.tr.utts[c] {
			bystander = append(bystander, u.text)
		}
	}
	r := &privacyRunner{respond: dropping("bystander", 0.95, bystander, nil)}
	e.p.Privacy = r
	e.p.Cfg.PrivacyFilter = false
	before, err := e.p.Transcribe(context.Background(), day, 14)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Meetings) != 1 {
		t.Fatalf("fixture: meetings = %+v", before.Meetings)
	}
	e.p.Cfg.PrivacyFilter = true
	doc, err := e.p.Transcribe(context.Background(), day, 14)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Meetings) != 0 || len(doc.EndedMeetings) != 0 {
		t.Fatalf("bystander conversation still a meeting: %+v", doc.Meetings)
	}
	if ms, err := e.st.MeetingsOn(day); err != nil || len(ms) != 0 {
		t.Fatalf("stored meetings = %+v, %v", ms, err)
	}
	for _, u := range doc.Transcript {
		if slices.Contains(bystander, u.Text) {
			t.Errorf("bystander line %q kept", u.Text)
		}
	}
	if len(doc.Removed) == 0 {
		t.Fatal("no removed span recorded")
	}
	in := r.inputs[0]
	if len(in.Meetings) != 1 || in.Meetings[0].App != "In person" {
		t.Errorf("skill input meetings = %+v", in.Meetings)
	}
}

// Each pass sends only lines without a decision, plus up to two minutes of
// preceding context; the final pass reuses every decision.
func TestPrivacyIncremental(t *testing.T) {
	e := newEnv(t)
	r := privacyFixture(e)
	ctx := context.Background()
	if _, err := e.p.Transcribe(ctx, day, 13); err != nil {
		t.Fatal(err)
	}
	e.file("audio/1305-mic.m4a", old)
	e.tr.utts["1305-mic"] = []fu{{30, 33, "new line"}}
	doc, err := e.p.Transcribe(ctx, day, 13)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.inputs) != 2 {
		t.Fatalf("privacy-filter ran %d times", len(r.inputs))
	}
	var sent, context []string
	for _, l := range r.inputs[1].Utterances {
		if l.Context {
			context = append(context, l.Text)
		} else {
			sent = append(sent, l.Text)
		}
	}
	// 13:05:30 is new; context starts at 13:03:30: the 13:04:00 line only.
	if !reflect.DeepEqual(sent, []string{"new line"}) || !reflect.DeepEqual(context, []string{"the account number is 4417 1234"}) {
		t.Fatalf("second pass sent %q with context %q", sent, context)
	}
	want := []string{"the parser needs a retry", "the account number is [redacted]", "new line"}
	if got := transcriptTexts(doc); !reflect.DeepEqual(got, want) {
		t.Fatalf("transcript = %q, want %q", got, want)
	}
	if _, err := e.p.Transcribe(ctx, day, 13); err != nil {
		t.Fatal(err)
	}
	if len(r.inputs) != 2 {
		t.Fatalf("a pass with every line decided ran the skill")
	}
	if fi, err := os.Stat(filepath.Join(e.paths.CacheDay(day), "privacy.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("privacy cache: %v %v", fi, err)
	}
}

// A drop range over context lines turns their earlier keep into a drop
// (never a drop into a keep); the pass's record purges them from the
// derived doc and the index. Redactions of context lines are ignored.
func TestPrivacyContextUpgradedToDrop(t *testing.T) {
	e := newEnv(t)
	r := privacyFixture(e)
	e.tr.utts["1300-mic"] = []fu{
		{1, 3, "the parser needs a retry"},
		{240, 243, "so how was it"},  // 13:04:00, ambiguous in pass 1
		{250, 253, "it was alright"}, // 13:04:10
	}
	r.respond = dropping("bystander", 0.9, nil, nil) // pass 1 keeps everything
	ctx := context.Background()
	if _, err := e.p.Transcribe(ctx, day, 13); err != nil {
		t.Fatal(err)
	}
	if hits, _ := e.st.Search("alright", 10); len(hits) != 1 {
		t.Fatalf("pass 1 should keep the ambiguous lines: %+v", hits)
	}
	e.file("audio/1305-mic.m4a", old)
	e.tr.utts["1305-mic"] = []fu{{0, 3, "the kids loved the lake house"}, {5, 8, "we should all go back in june"}}
	r.respond = func(in privacyInput) string {
		ctxIdx := -1
		for _, l := range in.Utterances {
			if l.Context {
				ctxIdx = l.Index
			}
		}
		return fmt.Sprintf(`{"drop":[{"from":0,"to":%d,"reason":"bystander","confidence":0.95}],"redact":[{"index":%d,"text":"[redacted]"}]}`,
			len(in.Utterances)-1, ctxIdx)
	}
	doc, err := e.p.Transcribe(ctx, day, 13)
	if err != nil {
		t.Fatal(err)
	}
	if ctxLines := len(r.inputs[1].Utterances) - 2; ctxLines != 2 {
		t.Fatalf("pass 2 sent %d context lines, want 2: %+v", ctxLines, r.inputs[1].Utterances)
	}
	if got := transcriptTexts(doc); !reflect.DeepEqual(got, []string{"the parser needs a retry"}) {
		t.Fatalf("transcript = %q", got)
	}
	assertDerivedLacks(t, e, 13, "parser", "so how was it", "alright", "lake house", "june")
	for _, q := range []string{"alright", "lake"} {
		if hits, _ := e.st.Search(q, 10); len(hits) != 0 {
			t.Errorf("search %q still finds %+v", q, hits)
		}
	}
	if len(doc.Removed) != 1 || !doc.Removed[0].Start.Equal(at(13, 4, 0)) {
		t.Errorf("removed = %+v", doc.Removed)
	}
}

// A drop never turns back into a keep through a later context range.
func TestPrivacyContextNeverDowngradesDrop(t *testing.T) {
	e := newEnv(t)
	r := privacyFixture(e) // drops the 13:01 call
	ctx := context.Background()
	if _, err := e.p.Transcribe(ctx, day, 13); err != nil {
		t.Fatal(err)
	}
	e.file("audio/1305-mic.m4a", old)
	e.tr.utts["1305-mic"] = []fu{{30, 33, "new line"}}
	r.respond = dropping("bystander", 0.9, nil, nil)
	doc, err := e.p.Transcribe(ctx, day, 13)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(transcriptTexts(doc), "love you mom") {
		t.Fatal("dropped line came back")
	}
}

// Forgetting an hour's decisions (Reprocess) asks again, so a wrong drop
// can be undone.
func TestPrivacyForgetHour(t *testing.T) {
	e := newEnv(t)
	r := privacyFixture(e)
	ctx := context.Background()
	if _, err := e.p.Transcribe(ctx, day, 13); err != nil {
		t.Fatal(err)
	}
	// A decision of another hour stays.
	other := privacyKey(media.Utterance{Track: "mic", Start: at(14, 0, 1), Text: "x"})
	c := e.p.loadPrivacyCache(day)
	c.Lines[other] = privacyDecision{Drop: true, Reason: "bystander", Confidence: 1}
	if err := writePrivateJSON(e.p.privacyCachePath(day), c); err != nil {
		t.Fatal(err)
	}
	if err := e.p.ForgetPrivacy(day, 13); err != nil {
		t.Fatal(err)
	}
	c = e.p.loadPrivacyCache(day)
	if len(c.Lines) != 1 || !c.Lines[other].Drop {
		t.Fatalf("cache after forget = %+v", c.Lines)
	}
	r.respond = dropping("bystander", 0.9, nil, nil)
	doc, err := e.p.Transcribe(ctx, day, 13)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.inputs) != 2 || len(doc.Transcript) != 4 || len(doc.Removed) != 0 {
		t.Fatalf("after forget: %d runs, transcript %q", len(r.inputs), transcriptTexts(doc))
	}
	if err := e.p.ForgetPrivacy("2026-10-01", 9); err != nil {
		t.Fatalf("forget without a cache: %v", err)
	}
}

// Invalid filter output is not retried forever: the hour goes to
// needs_review with its speech withheld (segments and screens kept, no
// summary), so retention proceeds; Reprocess tries again.
func TestPrivacyInvalidOutputWithholdsSpeech(t *testing.T) {
	e := newEnv(t)
	r := privacyFixture(e)
	e.file("screens/131000.jpg", old)
	e.ocr.texts["131000.jpg"] = "screen text"
	r.err = fmt.Errorf("%w: privacy-filter: bad", skills.ErrInvalidOutput)
	if err := e.p.ProcessPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec, ok := e.hourStatus(13)
	if !ok || rec.Status != store.StatusNeedsReview || rec.SummaryJSON != "" {
		t.Fatalf("hour = %+v %v", rec, ok)
	}
	if len(e.sum.calls) != 0 {
		t.Fatal("summarized withheld speech")
	}
	doc, err := Load(e.paths.DerivedHour(day, 13))
	if err != nil {
		t.Fatal(err)
	}
	if !doc.SpeechWithheld || len(doc.Transcript) != 0 || len(doc.Screens) != 1 || len(doc.Segments) == 0 {
		t.Fatalf("doc = %+v", doc)
	}
	assertDerivedLacks(t, e, 13, "screen text", "parser", "mom", "4417")
	if hits, _ := e.st.Search("parser", 10); len(hits) != 0 {
		t.Fatalf("indexed withheld speech: %+v", hits)
	}
	if hours, err := e.p.PendingHours(); err != nil || len(hours) != 0 {
		t.Fatalf("pending = %v %v", hours, err)
	}
	// Reprocess (status pending, Transcribe again) succeeds once the filter does.
	r.err = nil
	if err := e.st.UpsertHour(store.HourRecord{Day: day, Hour: 13, Status: store.StatusPending}); err != nil {
		t.Fatal(err)
	}
	doc, err = e.p.Transcribe(context.Background(), day, 13)
	if err != nil {
		t.Fatal(err)
	}
	if doc.SpeechWithheld || doc.Status != store.StatusTranscribed || len(doc.Transcript) != 2 {
		t.Fatalf("after reprocess: %+v", doc)
	}
}

// Without a usable filter result nothing unfiltered is written: no derived
// doc, no indexed text, no summary; the hour is retried later.
func TestPrivacyFailureWritesNothing(t *testing.T) {
	for _, fail := range []error{
		fmt.Errorf("%w: privacy-filter: exit 1", skills.ErrUnavailable),
	} {
		t.Run(fail.Error(), func(t *testing.T) {
			e := newEnv(t)
			r := privacyFixture(e)
			r.err = fail
			err := e.p.ProcessPending(context.Background())
			if !errors.Is(err, skills.ErrUnavailable) {
				t.Fatalf("err = %v, want transient", err)
			}
			if rec, ok := e.hourStatus(13); ok && rec.Status != store.StatusPending && rec.Status != store.StatusTranscribed {
				t.Fatalf("status = %q", rec.Status)
			} else if ok && rec.SummaryJSON != "" {
				t.Fatalf("summary = %q", rec.SummaryJSON)
			}
			if len(e.sum.calls) != 0 {
				t.Fatalf("summarized after a failed filter")
			}
			if _, err := os.Stat(e.paths.DerivedHour(day, 13)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("derived doc written: %v", err)
			}
			if hits, _ := e.st.Search("mom", 10); len(hits) != 0 {
				t.Fatalf("indexed: %+v", hits)
			}
			if _, err := os.Stat(r.paths[0]); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("skill input left behind")
			}
			// Later the filter works and the hour completes.
			r.err = nil
			if err := e.p.ProcessPending(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(e.sum.calls) != 1 {
				t.Fatalf("not retried: %v", e.sum.calls)
			}
		})
	}
}

// The in-progress hour does not summarize when the filter fails.
func TestPrivacyFailureSkipsPartialSummary(t *testing.T) {
	e := newEnv(t)
	r := privacyFixture(e)
	r.err = fmt.Errorf("%w: privacy-filter: exit 1", skills.ErrUnavailable)
	e.p.Now = func() time.Time { return at(13, 20, 0) }
	_, err := e.p.ProcessCurrent(context.Background(), at(13, 20, 0), at(13, 20, 0))
	if !errors.Is(err, skills.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if len(e.sum.calls) != 0 {
		t.Fatalf("partial pass summarized")
	}
	if _, ok := e.hourStatus(13); ok {
		t.Fatalf("hour recorded")
	}
}

func TestPrivacyValidate(t *testing.T) {
	ok := &checkedPrivacy{lines: 3}
	ok.Drop = []skills.DropRange{{From: 0, To: 2, Reason: "bystander", Confidence: 0.8}}
	ok.Redact = []skills.Redaction{{Index: 1, Text: "[redacted]"}, {Index: 2, Text: "PIN [REDACTED]"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []skills.PrivacyResult{
		{Drop: []skills.DropRange{{From: 2, To: 1, Reason: "bystander"}}},
		{Drop: []skills.DropRange{{From: 0, To: 3, Reason: "bystander"}}},
		{Drop: []skills.DropRange{{From: -1, To: 0, Reason: "bystander"}}},
		{Redact: []skills.Redaction{{Index: 3, Text: "x"}}},
		{Redact: []skills.Redaction{{Index: 0, Text: "account 4417"}}},
	} {
		v := &checkedPrivacy{PrivacyResult: c, lines: 3}
		if v.Validate() == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}

func TestRemovedSpansGroupRuns(t *testing.T) {
	u := func(m, s1, s2 int) media.Utterance { return media.Utterance{Start: at(13, m, s1), End: at(13, m, s2)} }
	us := []media.Utterance{u(0, 0, 5), u(1, 0, 5), u(1, 10, 20), u(2, 0, 5), u(3, 0, 5), u(4, 0, 9)}
	got := removedSpans(us, map[int]string{1: "bystander", 2: "bystander", 4: "bystander", 5: "personal_non_work"})
	want := []RemovedSpan{
		{Start: at(13, 1, 0), End: at(13, 1, 20), Reason: "bystander"},
		{Start: at(13, 3, 0), End: at(13, 3, 5), Reason: "bystander"},
		{Start: at(13, 4, 0), End: at(13, 4, 9), Reason: "personal_non_work"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spans = %+v\nwant %+v", got, want)
	}
}
