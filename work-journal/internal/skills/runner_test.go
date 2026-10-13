package skills

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeExec returns canned stdout values in order and records each call.
type fakeExec struct {
	outs  []string
	err   error
	calls [][]string
}

func (f *fakeExec) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if f.err != nil {
		return nil, f.err
	}
	i := min(len(f.calls)-1, len(f.outs)-1)
	return []byte(f.outs[i]), nil
}

// envelope wraps result the way `claude --output-format json` does.
func envelope(result string, isError bool) string {
	b, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": isError, "result": result})
	return string(b)
}

func newRunner(f *fakeExec) *ClaudeRunner {
	return &ClaudeRunner{Bin: "claude", WorkDir: "/repo", DataDir: "/data", Exec: f.run}
}

const goodHour = `{"narrative":"n","timeline":[{"start":"10:00","end":"10:30","app":"Code","activity":"a"}],"notes":["a"],"main_focus":"x","action_items":[]}`

func TestRunnerParsesEnvelope(t *testing.T) {
	f := &fakeExec{outs: []string{envelope(goodHour, false)}}
	var got HourSummary
	if err := newRunner(f).Run(context.Background(), "summarize-hour", "/data/in.json", &got); err != nil {
		t.Fatal(err)
	}
	want := HourSummary{Narrative: "n", Timeline: []TimelineEntry{{Start: "10:00", End: "10:30", App: "Code", Activity: "a"}}, Notes: []string{"a"}, MainFocus: "x", ActionItems: []ActionItem{}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %d", len(f.calls))
	}
}

func TestRunnerStripsFences(t *testing.T) {
	f := &fakeExec{outs: []string{envelope("Here you go:\n```json\n"+goodHour+"\n```\nAnything else?", false)}}
	var got HourSummary
	if err := newRunner(f).Run(context.Background(), "summarize-hour", "/data/in.json", &got); err != nil {
		t.Fatal(err)
	}
	if got.MainFocus != "x" || len(got.Notes) != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestExtractJSON(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"a":1}`, `{"a":1}`},
		{"prose {\"a\":\"}{\",\"b\":{\"c\":2}} trailing {\"z\":1}", `{"a":"}{","b":{"c":2}}`},
		{"```json\n{\"a\":1}\n```", `{"a":1}`},
		{"{\"x\":0} then ```json\n{\"a\":1}\n```", `{"a":1}`},
		{`{"a":"esc \" quote }"}`, `{"a":"esc \" quote }"}`},
	}
	for _, c := range cases {
		got, err := ExtractJSON(c.in)
		if err != nil || string(got) != c.want {
			t.Errorf("ExtractJSON(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "no json here", `{"a":1`} {
		if _, err := ExtractJSON(bad); err == nil {
			t.Errorf("ExtractJSON(%q): want error", bad)
		}
	}
}

func TestRunnerRetriesOnceThenInvalid(t *testing.T) {
	f := &fakeExec{outs: []string{envelope("sorry, no JSON", false), "not an envelope"}}
	var got HourSummary
	err := newRunner(f).Run(context.Background(), "summarize-hour", "/data/in.json", &got)
	if !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("err = %v, want ErrInvalidOutput", err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("Exec called %d times, want 2", len(f.calls))
	}
}

func TestRunnerRejectsSchemaViolation(t *testing.T) {
	bad := `{"segments":[{"index":0,"category":"Admin","confidence":1.4}]}`
	good := `{"segments":[{"index":0,"category":"Admin","confidence":0.9}]}`
	f := &fakeExec{outs: []string{envelope(bad, false), envelope(good, false)}}
	var got Categorization
	if err := newRunner(f).Run(context.Background(), "categorize-time", "/data/in.json", &got); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("Exec called %d times, want 2 (retry after schema violation)", len(f.calls))
	}
	if len(got.Segments) != 1 || got.Segments[0].Confidence != 0.9 {
		t.Fatalf("got %+v", got)
	}

	f = &fakeExec{outs: []string{envelope(bad, false)}}
	if err := newRunner(f).Run(context.Background(), "categorize-time", "/data/in.json", &got); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("err = %v, want ErrInvalidOutput", err)
	}
}

// checked rejects any category other than "Admin".
type checked struct {
	Categorization
}

func (c *checked) Validate() error {
	for _, s := range c.Segments {
		if s.Category != "Admin" {
			return errors.New("unknown category " + s.Category)
		}
	}
	return nil
}

func TestRunnerRetriesWhenValidatorFails(t *testing.T) {
	bad := `{"segments":[{"index":0,"category":"Design","confidence":0.9}]}`
	good := `{"segments":[{"index":0,"category":"Admin","confidence":0.8}]}`
	f := &fakeExec{outs: []string{envelope(bad, false), envelope(good, false)}}
	var got checked
	if err := newRunner(f).Run(context.Background(), "categorize-time", "/data/in.json", &got); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 || len(got.Segments) != 1 || got.Segments[0].Category != "Admin" {
		t.Fatalf("calls=%d got %+v", len(f.calls), got)
	}
}

func TestRunnerUnknownSkill(t *testing.T) {
	f := &fakeExec{outs: []string{envelope(goodHour, false)}}
	var got HourSummary
	if err := newRunner(f).Run(context.Background(), "nope", "/data/in.json", &got); err == nil {
		t.Fatal("want error for unknown skill")
	}
	if len(f.calls) != 0 {
		t.Fatalf("claude ran for an unknown skill")
	}
}

func TestRunnerArgs(t *testing.T) {
	f := &fakeExec{outs: []string{envelope(goodHour, false)}}
	var got HourSummary
	if err := newRunner(f).Run(context.Background(), "summarize-hour", "/data/derived/in.json", &got); err != nil {
		t.Fatal(err)
	}
	call := f.calls[0]
	if call[0] != "claude" {
		t.Fatalf("bin = %q", call[0])
	}
	args := call[1:]
	pair := func(flag, val string) bool {
		i := slices.Index(args, flag)
		return i >= 0 && i+1 < len(args) && args[i+1] == val
	}
	if !pair("--allowedTools", "Read") {
		t.Errorf("missing --allowedTools Read: %q", args)
	}
	if !pair("--add-dir", "/data") {
		t.Errorf("missing --add-dir /data: %q", args)
	}
	if !pair("--output-format", "json") {
		t.Errorf("missing --output-format json: %q", args)
	}
	// Only Skill (to load the skill) and Read exist; no MCP servers load.
	if !pair("--tools", "Skill,Read") {
		t.Errorf("missing --tools Skill,Read: %q", args)
	}
	if !slices.Contains(args, "--strict-mcp-config") {
		t.Errorf("missing --strict-mcp-config: %q", args)
	}
	if slices.Contains(args, "--mcp-config") {
		t.Errorf("--mcp-config must not be passed: %q", args)
	}
	want := "Use the tessl__summarize-hour skill. Input file: /data/derived/in.json (one record per line; if it is too large to read at once, read it in parts with offset and limit). Reply with only the JSON object."
	if !pair("-p", want) {
		t.Errorf("missing -p prompt %q: %q", want, args)
	}
	for _, a := range args {
		if strings.Contains(a, "dangerously") || strings.Contains(a, "bypassPermissions") {
			t.Errorf("forbidden arg %q", a)
		}
	}
	if n := strings.Count(strings.Join(args, " "), "--allowedTools"); n != 1 {
		t.Errorf("--allowedTools appears %d times", n)
	}
}

// TestRunnerRunsInWorkDir checks the default exec path: the real process
// runs in WorkDir so the installed skills under .claude/skills are found.
func TestRunnerRunsInWorkDir(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-claude")
	script := "#!/bin/sh\nprintf '{\"type\":\"result\",\"is_error\":false,\"result\":\"{\\\\\"markdown\\\\\":\\\\\"%s\\\\\"}\"}' \"$(pwd -P)\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	r := &ClaudeRunner{Bin: bin, WorkDir: work, DataDir: dir}
	var got Markdown
	if err := r.Run(context.Background(), "close-day", filepath.Join(dir, "in.json"), &got); err != nil {
		t.Fatal(err)
	}
	wantDir, _ := filepath.EvalSymlinks(work)
	if got.Markdown != wantDir {
		t.Fatalf("ran in %q, want %q", got.Markdown, wantDir)
	}
}

// TestSchemasMatchSkillDocs guards against drift between the schema each
// SKILL.md shows Claude and the schema the runner validates against.
func TestSchemasMatchSkillDocs(t *testing.T) {
	block := regexp.MustCompile("(?s)## Output schema\\s*```json\\n(.*?)\\n```")
	for _, name := range Names {
		md, err := os.ReadFile(filepath.Join("..", "..", "skills", "work-journal", "skills", name, "SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		m := block.FindSubmatch(md)
		if m == nil {
			t.Errorf("%s: no json block under ## Output schema", name)
			continue
		}
		var inDoc, embedded any
		if err := json.Unmarshal(m[1], &inDoc); err != nil {
			t.Errorf("%s: SKILL.md schema: %v", name, err)
			continue
		}
		raw, err := schemaFS.ReadFile("schemas/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &embedded); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(inDoc, embedded) {
			t.Errorf("%s: SKILL.md schema differs from internal/skills/schemas/%s.json", name, name)
		}
	}
}

func TestRunnerIsErrorEnvelope(t *testing.T) {
	f := &fakeExec{outs: []string{envelope("API Error: overloaded", true)}}
	var got HourSummary
	err := newRunner(f).Run(context.Background(), "summarize-hour", "/data/in.json", &got)
	if err == nil || !strings.Contains(err.Error(), "overloaded") {
		t.Fatalf("err = %v, want the envelope's message", err)
	}
}

func TestRunnerIsErrorCountsAsAttempt(t *testing.T) {
	f := &fakeExec{outs: []string{envelope("API Error: overloaded", true), envelope("API Error: overloaded", true)}}
	var got HourSummary
	err := newRunner(f).Run(context.Background(), "summarize-hour", "/data/in.json", &got)
	if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("err = %v, want transient ErrUnavailable (Ruling 34)", err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("Exec called %d times, want 2", len(f.calls))
	}

	f = &fakeExec{outs: []string{envelope("API Error: overloaded", true), envelope(goodHour, false)}}
	if err := newRunner(f).Run(context.Background(), "summarize-hour", "/data/in.json", &got); err != nil || len(f.calls) != 2 {
		t.Fatalf("is_error then good: err=%v calls=%d", err, len(f.calls))
	}
}

func TestRunnerExitErrorCountsAsAttempt(t *testing.T) {
	f := &fakeExec{err: errors.New("claude: exit status 1: boom")}
	var got HourSummary
	err := newRunner(f).Run(context.Background(), "summarize-hour", "/data/in.json", &got)
	if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrInvalidOutput) || len(f.calls) != 2 {
		t.Fatalf("err = %v calls = %d, want transient ErrUnavailable after 2 calls", err, len(f.calls))
	}
}

func TestRunnerExecNotFoundIsTransient(t *testing.T) {
	f := &fakeExec{err: &exec.Error{Name: "claude", Err: exec.ErrNotFound}}
	var got HourSummary
	err := newRunner(f).Run(context.Background(), "summarize-hour", "/data/in.json", &got)
	if err == nil || errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("err = %v, want a plain (retryable) error", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %d", len(f.calls))
	}
	// The real exec path with a missing binary is transient too.
	r := &ClaudeRunner{Bin: filepath.Join(t.TempDir(), "no-such-claude"), WorkDir: t.TempDir(), DataDir: t.TempDir()}
	if err := r.Run(context.Background(), "close-day", "/data/in.json", &Markdown{}); err == nil || errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("missing binary: err = %v, want a plain error", err)
	}
}

func TestRunnerTimeoutCountsAsAttempt(t *testing.T) {
	calls := 0
	r := &ClaudeRunner{Bin: "claude", WorkDir: "/repo", DataDir: "/data", Timeout: 50 * time.Millisecond,
		Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			calls++
			<-ctx.Done()
			return nil, ctx.Err()
		}}
	var got HourSummary
	err := r.Run(context.Background(), "summarize-hour", "/data/in.json", &got)
	if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrInvalidOutput) || calls != 2 {
		t.Fatalf("err = %v calls = %d, want transient ErrUnavailable after 2 timed-out attempts", err, calls)
	}
}

// TestRunnerParentCancelIsTransient runs a real slow process and cancels the
// caller's context: the process is killed promptly and the error is not
// ErrInvalidOutput (the hour is retried later, not sent to review).
func TestRunnerParentCancelIsTransient(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "slow-claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	r := &ClaudeRunner{Bin: bin, WorkDir: dir, DataDir: dir}
	began := time.Now()
	err := r.Run(ctx, "close-day", filepath.Join(dir, "in.json"), &Markdown{})
	if took := time.Since(began); took > 5*time.Second {
		t.Fatalf("Run took %s after cancel", took)
	}
	if err == nil || errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("err = %v, want a plain cancellation error", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the context error", err)
	}
}

// Ruling 34: output received but invalid on either attempt is
// ErrInvalidOutput; failing to get any output twice is transient.
func TestRunnerErrorClass(t *testing.T) {
	var got HourSummary
	f := &fakeExec{outs: []string{envelope("API Error: overloaded", true), envelope("no json here", false)}}
	if err := newRunner(f).Run(context.Background(), "summarize-hour", "/data/in.json", &got); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("is_error then invalid: err = %v, want ErrInvalidOutput", err)
	}
	f = &fakeExec{outs: []string{envelope("no json here", false), envelope("API Error: overloaded", true)}}
	if err := newRunner(f).Run(context.Background(), "summarize-hour", "/data/in.json", &got); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("invalid then is_error: err = %v, want ErrInvalidOutput", err)
	}
	f = &fakeExec{outs: []string{"not an envelope", "not an envelope"}}
	if err := newRunner(f).Run(context.Background(), "summarize-hour", "/data/in.json", &got); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unparseable envelope: err = %v, want ErrUnavailable", err)
	}
}

func TestHourSchemaRequiresNarrativeAndTimelineAndCapsNotes(t *testing.T) {
	sch, err := schema("summarize-hour")
	if err != nil {
		t.Fatal(err)
	}
	notes := func(n int) string {
		return `[` + strings.TrimSuffix(strings.Repeat(`"n",`, n), ",") + `]`
	}
	cases := []struct {
		name, in string
		ok       bool
	}{
		{"12 notes", `{"narrative":"","timeline":[],"notes":` + notes(12) + `,"main_focus":"","action_items":[]}`, true},
		{"13 notes", `{"narrative":"","timeline":[],"notes":` + notes(13) + `,"main_focus":"","action_items":[]}`, false},
		{"no narrative", `{"timeline":[],"notes":[],"main_focus":"","action_items":[]}`, false},
		{"no timeline", `{"narrative":"","notes":[],"main_focus":"","action_items":[]}`, false},
		{"entry missing app", `{"narrative":"","timeline":[{"start":"10:00","end":"10:10","activity":"a"}],"notes":[],"main_focus":"","action_items":[]}`, false},
		{"entry extra field", `{"narrative":"","timeline":[{"start":"10:00","end":"10:10","app":"a","activity":"a","x":1}],"notes":[],"main_focus":"","action_items":[]}`, false},
	}
	for _, c := range cases {
		var out HourSummary
		err := decode(sch, c.in, &out)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestRunnerNoJSONErrorCarriesResultSnippet(t *testing.T) {
	prose := "I couldn't summarize this hour because I couldn't read the input file.\n\nIt is 582 KB of minified JSON on a single line. " + strings.Repeat("More words. ", 60)
	f := &fakeExec{outs: []string{envelope(prose, false)}}
	var got HourSummary
	err := newRunner(f).Run(context.Background(), "summarize-hour", "/data/in.json", &got)
	if !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("err = %v, want ErrInvalidOutput", err)
	}
	msg := err.Error()
	if strings.Contains(msg, "\n") {
		t.Fatalf("error is not one line: %q", msg)
	}
	if !strings.Contains(msg, "summarize-hour") || !strings.Contains(msg, "I couldn't summarize this hour because I couldn't read the input file. It is 582 KB") {
		t.Fatalf("error lacks skill or result snippet: %q", msg)
	}
	if strings.Contains(msg, strings.Repeat("More words. ", 30)) {
		t.Fatalf("snippet longer than 300 chars: %q", msg)
	}
}

func TestMarshalInputOneRecordPerLineNoHTMLEscapes(t *testing.T) {
	type rec struct {
		Speaker string `json:"speaker"`
		Text    string `json:"text"`
	}
	in := struct {
		Day   string   `json:"day"`
		Lines []rec    `json:"lines"`
		OCR   []string `json:"ocr"`
	}{Day: "2026-10-07", OCR: []string{"<a> & <b>", "two"}}
	for i := 0; i < 200; i++ {
		in.Lines = append(in.Lines, rec{"You", strings.Repeat("w", 30)})
	}
	b, err := MarshalInput(in)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if lines[0] != "{" || lines[1] != ` "day": "2026-10-07",` || lines[2] != ` "lines": [` {
		t.Fatalf("top level not one member per line, in field order: %q", lines[:3])
	}
	if want := `  {"speaker":"You","text":"` + strings.Repeat("w", 30) + `"},`; lines[3] != want {
		t.Fatalf("record line %q, want %q", lines[3], want)
	}
	if !strings.Contains(string(b), ` "ocr": ["<a> & <b>","two"]`) {
		t.Fatalf("short value not kept on one line, or HTML escaped: %s", b[len(b)-80:])
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
}
