# Work Journal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A macOS app that records screen, system audio, mic, and app activity, and uses Claude Code skills to turn it into hourly notes, meeting notes with named speakers, a categorized time breakdown, daily journals, and weekly rollups, browsable in a local dashboard.

**Architecture:** A small Swift helper (`WorkJournalRecorder.app`) captures raw media and does OCR. A single Go binary (`journal`) supervises it, processes each finished hour locally (whisper.cpp, sherpa-onnx, Vision OCR via the helper), calls skills through `claude -p` for all interpretation, stores results in SQLite + Markdown, and serves the dashboard.

**Tech Stack:** Go 1.23+, `modernc.org/sqlite` (pure Go, FTS5), `github.com/BurntSushi/toml`, `github.com/santhosh-tekuri/jsonschema/v6`, `github.com/k2-fsa/sherpa-onnx-go-macos`; Swift 6.3 compiled with `-swift-version 5` (ScreenCaptureKit, AVFoundation, Vision, AppKit); `whisper-cli` (Homebrew `whisper-cpp`); `afconvert` (built into macOS); `claude` CLI; `tessl` CLI.

**Spec:** `work-journal/docs/superpowers/specs/2026-10-07-work-journal-design.md`

## Global Constraints

- Project root: `/Users/chandler/Desktop/testing/work-journal`. Go module path: `workjournal`. All paths below are relative to that root.
- macOS 14+ only. No Python anywhere.
- Default data dir `~/WorkJournal/`; nothing under the data dir is ever written inside the repo. Tests use `t.TempDir()`.
- Defaults (spec §2–§9): `retention_days=7`, `close_time="18:00"`, `rollup_weekday="Friday"`, `idle_minutes=5`, `pause_auto_resume_minutes=60`, `screenshot_interval_sec=10`, `meeting_screenshot_interval_sec=5`, `audio_chunk_minutes=5`, `screenshot_width=1440`, `whisper_model="base.en"`, `voice_match_threshold=0.75`, `name_confidence_threshold=0.7`, `min_free_gb=5`, `listen_addr="127.0.0.1:4747"`, `meeting_min_two_way_minutes=2`.
- Default categories: `Deep work`, `Meetings`, `Communication`, `Admin`, `Browsing/Research`, `Break/Idle`.
- Default exclusions: apps `1Password`, `Keychain Access`; window-title keywords `Private`, `Incognito`.
- Default meeting apps: `zoom.us`, `Microsoft Teams`, `FaceTime`, `Slack` (title contains `Huddle`); browser titles containing `Google Meet`, `Meet -`, `Microsoft Teams`, `Zoom Meeting`.
- Dashboard binds `127.0.0.1` only; no CDN or remote assets.
- Voice embeddings never leave the machine and are never passed to `claude`.
- Day keys are local-time `YYYY-MM-DD`; hour keys are local-time `0–23`; week keys are ISO `YYYY-Www`.
- Every external dependency sits behind a Go interface defined in the task that introduces it; tests use fakes, never real `claude`/`whisper-cli`/recorder (except `-tags=skills` evals and the spike).
- Commits: Conventional Commits, ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Git has no global identity on this machine; commit with `GIT_AUTHOR_NAME=chandler GIT_AUTHOR_EMAIL=chandler@tessl.io GIT_COMMITTER_NAME=chandler GIT_COMMITTER_EMAIL=chandler@tessl.io`. Never modify git config.

## Review Focus

1. **The hour in progress is never processed.** Only hours strictly before the current local hour, and only files whose mtime is ≥ 30 s old, are processed; partly written `.m4a` must not be read. → Task 9 test `TestPendingHoursSkipsCurrentHourAndFreshFiles`.
2. **Empty or idle hours produce no made-up notes.** An hour with no transcript, no OCR and only idle/paused/gap segments is marked `done` with an empty summary and **no** skill call. → Task 9 test `TestEmptyHourSkipsSkills`.
3. **Excluded time leaks nothing.** No screenshot, OCR text, transcript or window title from an excluded span may reach derived JSON, FTS, or a skill input. → Task 5 test `TestExcludedSpansDropTitles`, Task 9 test `TestExcludedSpanTextNotIndexed`.
4. **Claude output wrappers.** `claude --output-format json` returns an envelope whose `result` string may wrap the JSON in ```` ```json ```` fences or prose; it must still parse, and anything else becomes `needs_review`, never a crash. → Task 11 tests.
5. **Bad settings input.** `retention_days` ≤ 0, an unparseable `close_time`, or an empty category list is rejected with a visible message, and `config.toml` is left unchanged. → Task 2 `TestValidate`, Task 15 `TestSettingsRejectsInvalid`.

---

## File Structure

```
work-journal/
├── go.mod / go.sum / Makefile / README.md / .gitignore / config.example.toml
├── cmd/journal/main.go                  CLI dispatch
├── cmd/journal/app.go                   wiring: build all deps from config
├── internal/config/{config,paths}.go    Config, defaults, validate, Paths
├── internal/store/{store,schema.sql,hours,segments,meetings,search,kv}.go
├── internal/activity/{events,segments,meetings}.go   parse activity.jsonl → Segments, Meetings
├── internal/capture/{supervisor,control}.go          run recorder, socket client
├── internal/media/{convert,whisper,ocr}.go           afconvert, whisper-cli, recorder ocr
├── internal/diarize/{diarize,voices}.go              sherpa-onnx + voice profiles
├── internal/process/{hourdoc,pipeline}.go            per-hour pipeline
├── internal/skills/{runner,types,schemas/*.json}     claude -p runner
├── internal/categorize/rules.go
├── internal/scheduler/{clock,scheduler}.go
├── internal/retention/{retention,disk}.go
├── internal/journal/{closeday,rollup}.go             close-day / weekly-rollup jobs
├── internal/web/{server,day,hour,meeting,search,settings,week}.go
├── internal/web/templates/*.html, internal/web/static/{app.css,app.js,chart.js}
├── recorder/{build.sh,Info.plist}
├── recorder/Sources/{main,Policy,Capture,AudioChunkWriter,MicRecorder,
│                     ActivityMonitor,ControlServer,MenuBar,OCR,SelfTest}.swift
├── skills/work-journal/{tile.json,README.md}
├── skills/work-journal/skills/<name>/{SKILL.md,fixtures/*.json}   ×5
└── docs/{spike-diarization.md,manual-checklist.md}
```

### Cross-component contracts (fixed here; referenced by many tasks)

**Activity events** (`raw/<day>/activity.jsonl`, one JSON object per line, written by the recorder):
`{"ts":"<RFC3339 local>","type":"<t>","app":"<name>","bundle":"<id>","title":"<window title>"}`
`type` ∈ `start, stop, sleep, wake, app, idle, active, pause, resume, excluded, included, screens_off, audio_off, normal`. Time between `sleep` and `wake` is a gap. `app`/`bundle`/`title` are present only on `app`; for `excluded`, title is always omitted.

**Raw media names:** `raw/<day>/audio/HHMM-system.m4a`, `raw/<day>/audio/HHMM-mic.m4a` (HHMM = chunk start; if that name already exists for the day, the chunk is named `HHMMSS-<track>.m4a` with the exact start second — Go parsers accept both 4- and 6-digit prefixes), `raw/<day>/screens/HHMMSS.jpg`.

**Recorder CLI:**
`WorkJournalRecorder.app/Contents/MacOS/recorder run --data-dir D --socket S --interval 10 --meeting-interval 5 --chunk-minutes 5 --width 1440 --idle-minutes 5 --pause-auto-resume 60 --dashboard-url U [--all-displays] [--exclude-app A]… [--exclude-title K]… [--meeting-app A]… [--meeting-title K]… [--degraded]`  (`--degraded` = start with the error icon; the supervisor passes it after 3 failures within 5 min)
`recorder ocr <file>…` → stdout `[{"file":"…","text":"…"}]`
`recorder selftest --data-dir D` → exit 0 and `selftest ok`, or exit 1 and the failed check
`recorder run-tests` → exit 0 when Policy assertions pass

**Control socket** (`<data-dir>/recorder.sock`, newline-terminated text): `pause`, `resume`, `screens_off`, `audio_off`, `normal`, `rotate`, `quit` → `ok` (`rotate` finishes the open chunk of both audio tracks and replies once the `.m4a` files are fully written; the next samples open new chunks, `HHMMSS-…` when `HHMM-…` is taken; with audio off there is nothing to finish and it still replies `ok`. `quit` finishes writers, emits `stop`, exits 0); `status` → `{"state":"recording|paused|excluded","since":"<RFC3339>","meeting":bool,"screens":bool,"audio":bool}`.

**Hour status:** `pending → transcribed → done | needs_review`.

---

### Task 0 (human): Install prerequisites

Go, Homebrew and whisper.cpp are not installed. Homebrew needs your password, so you install it yourself.

- [ ] **Step 1:** Install Homebrew from https://brew.sh and follow its "Next steps" to add it to `PATH`.
- [ ] **Step 2:** `brew install go whisper-cpp`
- [ ] **Step 3: Verify:** `go version` prints `go1.23` or later; `which whisper-cli` prints a path; `swiftc --version` prints Swift 6.x.

---

### Task 1: Go module, config, and paths

**Files:**
- Create: `go.mod`, `.gitignore`, `Makefile`, `config.example.toml`, `internal/config/config.go`, `internal/config/paths.go`
- Test: `internal/config/config_test.go`, `internal/config/paths_test.go`

**Interfaces:**
- Produces:
  - `type Config struct` with TOML-tagged fields for every default in Global Constraints, plus `DataDir string`, `RollupWeekday string`, `AllDisplays bool`, `ExcludedApps, ExcludedTitleKeywords, MeetingApps, MeetingTitleKeywords, Categories []string`, `Rules map[string]string` (app or title substring → category), `Diarization string` (`"sherpa"` default, or `"off"`; set by the Task 8 decision).
  - `func Default() Config` (`DataDir` = `$HOME/WorkJournal`)
  - `func Load(path string) (Config, error)`: a missing file returns `Default()`; fields absent from the file keep their defaults.
  - `func Save(path string, c Config) error`: validates first and writes atomically (temp file + rename).
  - `func (c Config) Validate() error`
  - `func (c Config) CloseClock() (hour, minute int)`
  - `type Paths struct{ Root string }` with methods `Config() string`, `DB() string`, `Socket() string`, `RawDay(day string) string`, `Audio(day string) string`, `Screens(day string) string`, `Activity(day string) string`, `DerivedHour(day string, hour int) string` (`derived/<day>/HH.json`, two-digit hour), `DailyJournal(day string) string`, `WeeklyJournal(week string) string`, `Voices() string`, `Models() string`, `Ensure() error`
  - `func DayKey(t time.Time) string`, `func WeekKey(t time.Time) string` (ISO, e.g. `2026-W41`)

- [ ] **Step 1: Write failing tests**
  - `TestDefaults`: `Default().RetentionDays == 7`, `CloseTime == "18:00"`, `ListenAddr == "127.0.0.1:4747"`, `len(Categories) == 6`, and `ExcludedApps` contains `"1Password"`.
  - `TestLoadMissingFileReturnsDefaults`
  - `TestLoadPartialFileKeepsDefaults`: a file containing only `retention_days = 14` gives 14, with `CloseTime` still `"18:00"`.
  - `TestSaveLoadRoundTrip`
  - `TestValidate` (table): `RetentionDays: 0` → error containing `retention_days`; `CloseTime: "25:00"` → error containing `close_time`; `Categories: nil` → error containing `categories`; `VoiceMatchThreshold: 1.5` → error.
  - `TestSaveInvalidLeavesFileUnchanged`
  - `TestPaths`: `DerivedHour("2026-10-07", 9)` ends with `derived/2026-10-07/09.json`; `WeekKey(2026-10-07)` → `"2026-W41"`; `WeekKey(2027-01-01)` → `"2026-W53"`.
- [ ] **Step 2:** `cd work-journal && go test ./internal/config/...` → FAIL (undefined).
- [ ] **Step 3:** Implement. `.gitignore`: `bin/`, `recorder/build/`, `*.db`. `Makefile` targets: `test` (`go test ./...`), `build` (`go build -o bin/journal ./cmd/journal`), `recorder` (`./recorder/build.sh`), `skills-eval` (`go test -tags=skills ./internal/skills/...`). `config.example.toml` lists every key with its default.
- [ ] **Step 4:** `go test ./internal/config/...` → PASS.
- [ ] **Step 5:** Commit: `feat: add go module, config and data paths`.

---

### Task 2: SQLite store

**Files:**
- Create: `internal/store/{store.go,schema.sql,hours.go,segments.go,meetings.go,search.go,kv.go}`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Produces:
  - `func Open(path string) (*Store, error)` (applies the embedded `schema.sql`, WAL mode); `(*Store).Close() error`
  - `type HourRecord struct{ Day string; Hour int; Status string; SummaryJSON string; UpdatedAt time.Time }`
  - `UpsertHour(HourRecord) error`, `Hour(day string, hour int) (HourRecord, bool, error)`, `HoursByStatus(statuses ...string) ([]HourRecord, error)`, `HoursOn(day string) ([]HourRecord, error)`
  - `type SegmentRecord struct{ Day string; Hour int; Start, End time.Time; App, Title, Kind, Category string; Confidence float64; Source string /* "rule"|"skill"|"user"|"auto" */ }`
  - `ReplaceSegments(day string, hour int, segs []SegmentRecord) error`, `SegmentsOn(day string) ([]SegmentRecord, error)`, `SetSegmentCategory(day string, hour int, start time.Time, category string) error` (sets `Source="user"`)
  - `CategoryTotals(fromDay, toDay string) (map[string]time.Duration, error)` (inclusive range)
  - `type MeetingRecord struct{ ID, Day string; Start, End time.Time; App, Title, SummaryJSON string }`, `UpsertMeeting`, `Meeting(id) (MeetingRecord, bool, error)`, `MeetingsOn(day)`
  - `type ParticipantRecord struct{ MeetingID, Speaker, Name string; Confidence float64; Confirmed bool }`, `SetParticipant`, `Participants(meetingID)`
  - `type TextDoc struct{ Kind /* transcript|ocr|note|meeting */, Day string; Hour int; TS time.Time; Text, Ref string }`, `ReplaceText(day string, hour int, docs []TextDoc) error`
  - `type SearchHit struct{ TextDoc; Snippet string }`, `Search(q string, limit int) ([]SearchHit, error)` (FTS5 `snippet()`, ranked by `bm25`; user input is quoted so syntax like `foo"` or `AND` can't break the query)
  - `GetKV(key string) (string, bool, error)`, `SetKV(key, val string) error`
  - `const StatusPending, StatusTranscribed, StatusDone, StatusNeedsReview`

- [ ] **Step 1: Write failing tests**, all against `Open(filepath.Join(t.TempDir(),"j.db"))`: `TestHourUpsertAndStatusQuery`, `TestReplaceSegmentsIsIdempotent`, `TestCategoryTotalsAcrossDays` (two segments of 30 min `Meetings` + 15 min `Admin` on different days → exact durations), `TestSetSegmentCategoryMarksUser`, `TestSearchFindsTranscriptAndSnippet` (searching `budget` finds a transcript doc and the snippet contains `budget`), `TestSearchToleratesSyntaxChars` (`q = "foo\" AND"` → no error), `TestReplaceTextRemovesOldDocs`, `TestKV`, `TestParticipantsUpsert`.
- [ ] **Step 2:** `go test ./internal/store/...` → FAIL.
- [ ] **Step 3:** Implement with `modernc.org/sqlite` (driver name `sqlite`). Use an FTS5 virtual table `docs_fts(text, kind UNINDEXED, day UNINDEXED, hour UNINDEXED, ts UNINDEXED, ref UNINDEXED)`.
- [ ] **Step 4:** `go test ./internal/store/...` → PASS.
- [ ] **Step 5:** Commit: `feat: add sqlite store with full-text search`.

---

### Task 3: Swift recorder — Policy, OCR, build, selftest

**Files:**
- Create: `recorder/build.sh`, `recorder/Info.plist`, `recorder/Sources/{main.swift,Policy.swift,OCR.swift,SelfTest.swift}`

**Interfaces:**
- Produces: `recorder run-tests`, `recorder ocr`, and `recorder selftest` per the contracts; the `.app` at `recorder/build/WorkJournalRecorder.app`.
- `Policy.swift` (pure, no frameworks except Foundation):
  - `struct PolicyConfig { excludeApps, excludeTitles, meetingApps, meetingTitles: [String]; interval, meetingInterval: Int }`
  - `func isExcluded(app: String, title: String, _ c: PolicyConfig) -> Bool` (case-insensitive; app equality or title substring)
  - `func isMeeting(app: String, title: String, _ c: PolicyConfig) -> Bool`
  - `func screenshotInterval(meeting: Bool, _ c: PolicyConfig) -> Int`
  - `func chunkName(start: Date, track: String) -> String` (`"HHMM-system.m4a"`)
  - `func shouldSkipScreenshot(prev: [UInt8]?, next: [UInt8], threshold: Double) -> Bool` (compare 16×16 grayscale thumbnails by mean absolute difference; skip when < `threshold`, default 2.0)

- [ ] **Step 1: Write failing assertions** in `main.swift`'s `run-tests` path: `isExcluded("1Password","",c)`, `isExcluded("Safari","Private Browsing — x",c)`, `!isExcluded("Safari","Docs",c)`, `isMeeting("zoom.us","Zoom Meeting",c)`, `isMeeting("Google Chrome","Meet - abc-defg",c)`, `isMeeting("Slack","Huddle with Priya",c)`, `screenshotInterval(meeting:true,c) == 5`, `chunkName(start: 14:05, track:"mic") == "1405-mic.m4a"`, identical thumbnails are skipped, a fully inverted one is not. Each failure prints its name; exit 1 if any fail.
- [ ] **Step 2:** `./recorder/build.sh && recorder/build/WorkJournalRecorder.app/Contents/MacOS/recorder run-tests` → fails (stubs return wrong values).
- [ ] **Step 3: Implement** `Policy.swift`. `build.sh`: `swiftc -O -swift-version 5 -parse-as-library -o …/MacOS/recorder Sources/*.swift`, copy `Info.plist` (bundle id `local.workjournal.recorder`, `LSUIElement` true, `NSMicrophoneUsageDescription`, `NSScreenCaptureUsageDescription`), then `codesign --force --sign - <app>`. `OCR.swift`: `VNRecognizeTextRequest`, `.accurate`, language correction on, output JSON per the contract. `SelfTest.swift`: `run` and `ocr` are wired in Task 4, so selftest for now writes one `activity.jsonl` line and runs OCR on a generated image containing the text `selftest`, checking that it reads back.
- [ ] **Step 4:** `run-tests` exits 0; `recorder selftest --data-dir $(mktemp -d)` prints `selftest ok`.
- [ ] **Step 5:** Commit: `feat(recorder): add policy, OCR and build script`.

---

### Task 4: Swift recorder — capture, activity, control socket, menu bar

**Files:**
- Create: `recorder/Sources/{Capture.swift,AudioChunkWriter.swift,MicRecorder.swift,ActivityMonitor.swift,ControlServer.swift,MenuBar.swift}`; Modify: `main.swift`, `SelfTest.swift`
- Create: `docs/manual-checklist.md`

**Interfaces:**
- Consumes: `Policy.swift` (Task 3).
- Produces: `recorder run …` per the contract; the activity events, file names and socket protocol listed under Cross-component contracts.

Behavior requirements (spec §4):
- `Capture`: one `SCStream` on the main display (or all displays when `--all-displays`) with `capturesAudio = true`. System audio goes to `AudioChunkWriter(track:"system")`. Screens are pulled with `SCScreenshotManager` on a timer at `screenshotInterval(meeting:)`, scaled to `--width` (per display: with `--all-displays` each display is scaled to `--width` and placed side by side left to right, so the image is `--width` × displays wide), JPEG quality 0.6, and skipped per `shouldSkipScreenshot`.
- `AudioChunkWriter`: `AVAssetWriter` AAC mono 32 kbps, rotated on wall-clock boundaries that are multiples of `--chunk-minutes`; finishes the file on rotate, pause and stop.
- `MicRecorder`: `AVAudioEngine` input tap → `AudioChunkWriter(track:"mic")`.
- `ActivityMonitor`: `NSWorkspace.willSleepNotification`/`didWakeNotification` → `sleep`/`wake` events (writers finish on sleep); `NSWorkspace.didActivateApplicationNotification`, plus the window title via `AXUIElementCopyAttributeValue(kAXFocusedWindowAttribute → kAXTitleAttribute)`, polled every 2 s. Idle uses `CGEventSource.secondsSinceLastEventType(.combinedSessionState, .null)` ≥ `--idle-minutes`. Emits the events from the contract. While `isExcluded` is true it stops screenshots and **both** audio writers and emits `excluded` with no title; when it ends, `included`.
- `ControlServer`: Unix-socket listener implementing the protocol. `pause` auto-resumes after `--pause-auto-resume` minutes.
- `MenuBar`: `NSStatusItem` with SF Symbols `record.circle` / `pause.circle` / `exclamationmark.circle`, and menu items Pause/Resume, Open Dashboard (`NSWorkspace.open(--dashboard-url)`), Quit (emits `stop` and finishes writers).
- `SelfTest` is extended: 5 s capture into a temp dir, then assert ≥ 1 `screens/*.jpg`, both `*-system.m4a` and `*-mic.m4a` exist with size > 0, and `activity.jsonl` has `start` and `app` events.

- [ ] **Step 1:** Extend `selftest` with the capture assertions above (failing: no capture code yet).
- [ ] **Step 2:** Build and run `recorder selftest` → fails on the screens check.
- [ ] **Step 3:** Implement the files above.
- [ ] **Step 4:** Build; `run-tests` passes; `selftest` prints `selftest ok`. The first run triggers the macOS permission prompts: grant them (the human does this) and re-run. Write `docs/manual-checklist.md` covering: permissions granted; both audio tracks audible in a test call; switching to 1Password logs `excluded` and no screenshot or audio is written for that span; pause/resume from the menu bar; auto-resume after the configured minutes; `echo status | nc -U ~/WorkJournal/recorder.sock` returns JSON.
- [ ] **Step 5:** Commit: `feat(recorder): capture screen, audio and activity with control socket`.

---

### Task 5: Activity parsing, segments, meeting detection (Go)

**Files:**
- Create: `internal/activity/{events.go,segments.go,meetings.go}`
- Test: `internal/activity/activity_test.go`, `internal/activity/testdata/*.jsonl`

**Interfaces:**
- Consumes: `config.Config` (Task 1).
- Produces:
  - `type Event struct{ TS time.Time; Type, App, Bundle, Title string }`, `func ReadEvents(path string) ([]Event, error)` (skips malformed lines and counts them)
  - `type Segment struct{ Start, End time.Time; App, Title, Kind string }` where Kind ∈ `active, idle, excluded, paused, gap`
  - `func Segments(events []Event, from, to time.Time) []Segment`: clips to `[from,to)`; the time between `stop`/missing events and the next `start` is `gap`; excluded segments have empty `App` and `Title`.
  - `type Meeting struct{ ID string; Start, End time.Time; App, Title string }` (ID = `<day>-<HHMM>-<slug(app)>`)
  - `type SpeechSpan struct{ Start, End time.Time; Track string }`
  - `func DetectMeetings(segs []Segment, speech []SpeechSpan, c config.Config) []Meeting`: a meeting is the union of meeting-app/title active segments **or** spans where both tracks have overlapping speech totaling ≥ `meeting_min_two_way_minutes`. Adjacent spans < 2 min apart merge.

- [ ] **Step 1: Write failing tests:** `TestSegmentsBasic` (app A 14:00–14:20, B 14:20–15:00 → 2 active segments), `TestSegmentsClipToHour`, `TestSegmentsIdleAndPause`, `TestSegmentsGapAfterStop`, `TestSegmentsSleepIsGap`, `TestExcludedSpansDropTitles` (an excluded segment has empty App and Title even if the preceding `app` event had a title), `TestReadEventsSkipsMalformed`, `TestDetectMeetingByApp` (Zoom 10:00–10:45 → one meeting with those bounds), `TestDetectMeetingByTwoWaySpeech` (3 min of overlapping mic+system speech in Arc → meeting), `TestNoMeetingForOneWayAudio` (a 20-min YouTube video on system audio only → none), `TestMeetingsMergeShortGaps`.
- [ ] **Step 2:** `go test ./internal/activity/...` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** PASS.
- [ ] **Step 5:** Commit: `feat: parse activity into segments and detect meetings`.

---

### Task 6: Capture supervisor and control client (Go)

**Files:**
- Create: `internal/capture/{supervisor.go,control.go}`
- Test: `internal/capture/capture_test.go`

**Interfaces:**
- Consumes: `config.Config`, `config.Paths`.
- Produces:
  - `type Status struct{ State string; Since time.Time; Meeting, Screens, Audio bool }` (JSON tags match the socket contract)
  - `type Control interface{ Pause() error; Resume() error; Status() (Status, error); Send(cmd string) error }`, `func NewSocketControl(sock string) Control`
  - `func RecorderArgs(c config.Config, p config.Paths) []string` (builds the `run …` argument list from the contract)
  - `type Supervisor struct{ Bin string; Args []string; Logf func(string, ...any); Start func(ctx, bin string, args []string) (wait func() error, err error) /* injectable */; OnGap func(from, to time.Time) }`
  - `func (s *Supervisor) Run(ctx context.Context) error`: restarts on exit with back-off 1s, 2s, 4s … capped at 60 s, reset after 5 min of uptime; calls `OnGap` for each downtime; after 3 consecutive failures within 5 min, logs `recorder failing` and appends `--degraded` to the args for the next start.

- [ ] **Step 1: Write failing tests:** `TestRecorderArgsIncludesExclusions` (contains `--exclude-app 1Password` and `--meeting-interval 5`), `TestSupervisorRestartsWithBackoff` (fake Start exits immediately 3×; the recorded sleeps are 1s, 2s, 4s via an injected `sleep func(time.Duration)`), `TestSupervisorReportsGap`, `TestSupervisorAddsDegradedAfterThreeFailures`, `TestSupervisorStopsOnContextCancel`, `TestSocketControlRoundTrip` (a fake Unix-socket server in a temp dir answers `status` → parsed `Status{State:"paused"}`).
- [ ] **Step 2:** FAIL. **Step 3:** Implement (add `Sleep func(time.Duration)` to `Supervisor`). **Step 4:** PASS.
- [ ] **Step 5:** Commit: `feat: supervise recorder and add control client`.

---

### Task 7: Media — convert, transcribe, OCR wrappers

**Files:**
- Create: `internal/media/{convert.go,whisper.go,ocr.go,exec.go}`
- Test: `internal/media/media_test.go`, `internal/media/testdata/{whisper.json,ocr.json}`

**Interfaces:**
- Produces:
  - `type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)` (default `ExecRunner` wraps `exec.CommandContext`, putting stderr in the error)
  - `func ToWav16k(ctx, run Runner, in, out string) error` → `afconvert -f WAVE -d LEI16@16000 -c 1 in out`
  - `type Utterance struct{ Start, End time.Time; Track, Speaker, Text string }`
  - `type Transcriber interface{ Transcribe(ctx, wav string, chunkStart time.Time, track string) ([]Utterance, error) }`
  - `type Whisper struct{ Run Runner; Bin, Model string }`: `whisper-cli -m <Model> -f <wav> -oj -of <wav without ext> -np`, then reads `<prefix>.json` `transcription[].offsets.{from,to}` (ms) and `.text`. Offsets are added to `chunkStart`, text is trimmed, and blank or `[BLANK_AUDIO]` entries are dropped. Mic utterances get `Speaker="me"`.
  - `type ScreenText struct{ Time time.Time; File, Text string }`
  - `type OCR interface{ OCR(ctx, files []string) ([]ScreenText, error) }`
  - `type RecorderOCR struct{ Run Runner; Bin string; Batch int /*50*/ }`: parses the contract's JSON; `Time` comes from the `HHMMSS.jpg` filename plus the day. Consecutive identical texts are deduplicated.

- [ ] **Step 1: Write failing tests** using a fake `Runner` that records args and writes `testdata/whisper.json` to the `-of` path: `TestToWav16kArgs`, `TestWhisperParsesOffsetsAndDropsBlank`, `TestWhisperMicIsMe`, `TestOCRBatchesAndDedups` (120 files → 3 calls; duplicate texts collapsed), `TestOCRTimeFromFilename`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** PASS.
- [ ] **Step 5:** Commit: `feat: add audio conversion, whisper and OCR wrappers`.

---

### Task 8: SPIKE — sherpa-onnx diarization from Go (throwaway)

**Files:**
- Create: `spike/diarize/main.go` (throwaway, deleted at the end of this task), `docs/spike-diarization.md`

Question: on this Mac, does sherpa-onnx offline speaker diarization plus speaker-embedding matching, called from Go via `github.com/k2-fsa/sherpa-onnx-go-macos`, correctly separate 2–4 speakers in a 5-minute system-audio chunk, fast enough (< 0.5× real time)?

- [ ] **Step 1:** Download `sherpa-onnx-pyannote-segmentation-3-0` from the sherpa-onnx `speaker-segmentation-models` release and an English speaker-embedding model from the `speaker-recongition-models` release (e.g. `nemo_en_titanet_small.onnx`; compare against `3dspeaker_speech_eres2net_base_sv_zh-cn_3dspeaker_16k.onnx`) into `~/WorkJournal/models/`. Record the exact URLs.
- [ ] **Step 2:** Human records a ≥ 5-minute test call or plays a multi-speaker podcast while `recorder run` is active; convert one `*-system.m4a` with `afconvert`.
- [ ] **Step 3:** In `spike/diarize/main.go`, run `OfflineSpeakerDiarization` (clustering threshold 0.5 and `NumClusters=-1`), print the turns, and time it. Then extract one embedding per speaker with `SpeakerEmbeddingExtractor` and print the cosine similarity of the same speaker across two chunks versus different speakers.
- [ ] **Step 4:** Write `docs/spike-diarization.md`: model URLs chosen, speed, observed accuracy, same-speaker vs different-speaker cosine ranges (this confirms or adjusts `voice_match_threshold=0.75`), and a **decision**: `GO` (Task 9 uses sherpa; `Diarization` default stays `"sherpa"`) or `FALLBACK` (set the `Diarization` default to `"off"` in `internal/config`; Task 9 picks `NoopDiarizer` when it is `"off"`: every system utterance gets `Speaker="others"`, voice profiles are hidden in the UI, and names come only from `summarize-meeting`).
- [ ] **Step 5:** `rm -rf spike/`; commit only the doc: `docs: record diarization spike results`.

---

### Task 9: Diarizer, voice profiles, and the per-hour pipeline

**Files:**
- Create: `internal/diarize/{diarize.go,voices.go}`, `internal/process/{hourdoc.go,pipeline.go}`
- Test: `internal/diarize/voices_test.go`, `internal/process/pipeline_test.go`

**Interfaces:**
- Consumes: `store` (Task 2), `activity` (Task 5), `media` (Task 7), `config` (Task 1), spike decision (Task 8).
- Produces:
  - `type Turn struct{ Start, End float64; Speaker int }`, `type Diarizer interface{ Diarize(ctx, wav string) (turns []Turn, embeddings map[int][]float32, err error) }`, `type SherpaDiarizer struct{…}` (if GO) and `type NoopDiarizer struct{}`
  - `type Voices struct{ Dir string; Threshold float64 }`, `Match(emb []float32) (name string, score float64, ok bool)`, `Enroll(name string, emb []float32) error` (appends; files `<slug>.json` = `{"name":…, "embeddings":[[…]]}`), `List() ([]string, error)`, `Delete(name string) error`
  - `func Cosine(a, b []float32) float64`
  - `type HourDoc struct{ Day string; Hour int; Status string; Segments []activity.Segment; Transcript []media.Utterance; Screens []media.ScreenText; Meetings []activity.Meeting; Embeddings map[string][]float32 \`json:"-"\` }` with `Load(path)`/`Save(path)`. Embeddings are kept only in memory and in `voices/`, never in derived JSON.
  - `type Pipeline struct{ Cfg config.Config; Paths config.Paths; Store *store.Store; Transcriber media.Transcriber; OCR media.OCR; Diarizer diarize.Diarizer; Voices *diarize.Voices; Run media.Runner; Now func() time.Time; Summarize HourSummarizer /* Task 11 */ }`
  - `type HourSummarizer interface{ SummarizeHour(ctx, doc *HourDoc) error }` (set in Task 11; nil means skip, leaving the hour `transcribed`)
  - `func (p *Pipeline) PendingHours() ([]HourKey, error)`: hours that have raw data, are not `done`/`needs_review`, are strictly before the current hour, and have all raw files with mtime ≥ 30 s old.
  - `func (p *Pipeline) Transcribe(ctx, day string, hour int) (*HourDoc, error)`: builds the HourDoc, writes the derived JSON, indexes transcript and OCR text, upserts meetings and segments, and sets status `transcribed`.
  - `func (p *Pipeline) ProcessPending(ctx) error`: for each pending hour, `Transcribe` if needed and then `Summarize`. An error on one hour is logged and the loop continues.
  - `type HourKey struct{ Day string; Hour int }`

Speaker assignment: each system utterance takes the `Turn` with the largest time overlap → `Speaker N` (1-based), or the matched voice name when `Voices.Match` is ok.

- [ ] **Step 1: Write failing tests** with fake Transcriber, OCR, Diarizer and Runner and a temp data dir containing fixture `activity.jsonl`, audio and jpg files (empty files with set mtimes):
  - `TestCosine`, `TestVoicesEnrollMatchDelete` (enrolled `[1,0,0]` matches `[0.9,0.1,0]` ≥ 0.75; `[0,1,0]` does not)
  - `TestPendingHoursSkipsCurrentHourAndFreshFiles` (Now = 15:10; data at 13, 14 and 15; the 14:55 chunk has mtime 10 s ago → only hour 13 is pending)
  - `TestTranscribeBuildsHourDoc` (status `transcribed`; derived JSON exists; mic utterances `me`; system utterances `Speaker 1`/`Speaker 2` from fake turns)
  - `TestKnownVoiceNamed` (an enrolled voice → utterance Speaker `Priya`)
  - `TestExcludedSpanTextNotIndexed` (a screenshot whose timestamp falls inside an excluded segment is dropped before OCR; `Search` for its fixture text returns nothing)
  - `TestEmptyHourSkipsSkills` (an idle-only hour → status `done`, `SummaryJSON == "{}"`, and the fake summarizer is never called)
  - `TestProcessPendingContinuesAfterError`
  - `TestDerivedJSONHasNoEmbeddings`
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** PASS (`go test ./internal/diarize/... ./internal/process/...`).
- [ ] **Step 5:** Commit: `feat: add per-hour pipeline with diarization and voice profiles`.

---

### Task 10: Skills plugin

**Files:**
- Create: `skills/work-journal/tile.json`, `skills/work-journal/README.md`
- Create for each of `summarize-hour`, `categorize-time`, `summarize-meeting`, `close-day`, `weekly-rollup`: `skills/work-journal/skills/<name>/SKILL.md` and `fixtures/{01..03}-input.json`
- Create: `internal/skills/schemas/<name>.json` (JSON Schema draft 2020-12, one per skill)

**Interfaces:**
- Produces the output contracts used by Tasks 11 and 16. Each schema sets `additionalProperties:false`:
  - `summarize-hour` → `{"notes":[string, 0–6], "main_focus":string, "action_items":[{"text":string,"owner":string}]}`
  - `categorize-time` → `{"segments":[{"index":int,"category":string,"confidence":number 0–1}]}`
  - `summarize-meeting` → `{"participants":[{"speaker":string,"name":string,"confidence":number}], "summary":string, "decisions":[string], "action_items":[{"text":string,"owner":string}]}`
  - `close-day` / `weekly-rollup` → `{"markdown":string}`
- Input files are the JSON Go writes: summarize-hour reads `HourDoc` minus embeddings; categorize-time reads `{"categories":[…],"hour_summary":{…},"segments":[{"index","app","title","minutes"}]}`; summarize-meeting reads `{"meeting":{…},"transcript":[…],"screens":[…]}`; close-day reads `{"day","hours":[summaries],"meetings":[…],"totals":{cat:minutes}}`; weekly-rollup reads `{"week","days":[{"day","markdown"}],"totals":{…},"prior_totals":{…}}`.

Each `SKILL.md` has frontmatter `name` and a `description` stating when to use it, and states: read only the given input file; output **only** JSON matching the schema (include the schema inline); never invent meetings, people, or action items not supported by the input; empty input → empty arrays. Skill-specific rules:
- `categorize-time`: use only the given categories; idle → `Break/Idle`; confidence < 0.6 when unsure.
- `summarize-meeting`: name a speaker only from transcript cues ("thanks, Priya") or on-screen participant labels; otherwise `name:""`; the mic track is always the user.
- `close-day`: sections `## Summary`, `## Timeline`, `## Time by category`, `## Meetings`, `## Action items`, `## Open threads`.
- `weekly-rollup`: sections `## Themes`, `## Time trends`, `## Key outcomes`, `## Draft status update`.

- [ ] **Step 1:** Write the schemas and fixtures. Fixtures must include one empty-hour input, one meeting with a "thanks, Priya" cue, and one meeting with no name cues.
- [ ] **Step 2:** Validate: `tessl skill lint skills/work-journal` (check the exact command with `AGENT=1 tessl` first) → no errors.
- [ ] **Step 3:** From the repo root (`/Users/chandler/Desktop/testing`), run `tessl install file:./work-journal/skills/work-journal --yes` and confirm the five skills appear under `.claude/skills/`.
- [ ] **Step 4:** Commit: `feat(skills): add work-journal skills plugin`.

---

### Task 11: Skill runner, rule categorization, hour summarizer, skill evals

**Files:**
- Create: `internal/skills/{runner.go,types.go,embed.go}`, `internal/categorize/rules.go`, `internal/process/summarize.go`
- Test: `internal/skills/runner_test.go`, `internal/skills/eval_test.go` (`//go:build skills`), `internal/categorize/rules_test.go`, `internal/process/summarize_test.go`

**Interfaces:**
- Consumes: schemas and fixtures (Task 10), `process.HourDoc`, `HourSummarizer` (Task 9), store.
- Produces:
  - `type Runner interface{ Run(ctx context.Context, skill, inputPath string, out any) error }`
  - `type ClaudeRunner struct{ Bin, WorkDir, DataDir string; Exec media.Runner; Timeout time.Duration /*5m*/ }`, with args: `-p "Use the <skill> skill. Input file: <inputPath>. Reply with only the JSON object." --output-format json --allowedTools Read --add-dir <DataDir>`, run with `cmd.Dir = WorkDir` (the repo root, so the installed skills are found)
  - `var ErrInvalidOutput = errors.New("invalid skill output")`
  - `func ExtractJSON(result string) ([]byte, error)`: takes the first ```` ```json ```` fenced block, or else the first balanced `{…}`
  - `types.go`: `HourSummary`, `Categorization`, `MeetingSummary`, `Markdown`, `ActionItem`, `Participant`, matching the Task 10 schemas
  - `func Apply(segs []activity.Segment, rules map[string]string) (cats []string, unmatched []int)`: idle/paused/gap → `Break/Idle`, excluded → `""` (not counted), rule match on app or title substring (case-insensitive; longest key wins)
  - `type SkillSummarizer struct{ Runner skills.Runner; Store *store.Store; Paths config.Paths; Cfg config.Config }` implements `HourSummarizer`: writes the skill input files under `derived/<day>/.inputs/`, runs `categorize-time` only when `unmatched` is non-empty, runs `summarize-hour`, runs `summarize-meeting` for each meeting whose `End` is in this hour (with participants saved; `Confirmed=false`), stores everything, indexes notes, and sets status `done`. Any `ErrInvalidOutput` → `needs_review`.

- [ ] **Step 1: Write failing tests:**
  - `TestRunnerParsesEnvelope` (fake Exec returns `{"type":"result","is_error":false,"result":"{\"notes\":[\"a\"],\"main_focus\":\"x\",\"action_items\":[]}"}`)
  - `TestRunnerStripsFences` (result = "Here you go:\n```json\n{…}\n```")
  - `TestRunnerRetriesOnceThenInvalid` (2 bad outputs → `ErrInvalidOutput`; Exec called exactly 2×)
  - `TestRunnerRejectsSchemaViolation` (category confidence 1.4 → retry)
  - `TestRunnerIsErrorEnvelope` (`is_error:true` → error, not panic)
  - `TestRunnerArgs` (contains `--allowedTools Read` and `--add-dir <DataDir>`; never `--dangerously-skip-permissions`)
  - `TestApplyRules` (`Figma`→`Design` via rule; idle → `Break/Idle`; an unmatched index is returned)
  - `TestSummarizerSkipsCategorizeWhenAllMatched`
  - `TestSummarizerMarksNeedsReview`
  - `TestSummarizerSavesMeetingParticipants`
  - Eval (`-tags=skills`, real `claude`): for every fixture, output passes the schema; the empty-hour fixture → `notes` empty; the "thanks, Priya" fixture → one participant named `Priya`; the no-cue fixture → all names `""`.
- [ ] **Step 2:** `go test ./internal/skills/... ./internal/categorize/... ./internal/process/...` → FAIL.
- [ ] **Step 3:** Implement. Validate with `santhosh-tekuri/jsonschema/v6` against the embedded schemas.
- [ ] **Step 4:** Unit tests PASS; then `make skills-eval` → PASS. If an eval fails, fix the `SKILL.md` (use superpowers:writing-skills), not the test.
- [ ] **Step 5:** Commit: `feat: run skills via claude and summarize hours`.

---

### Task 12: Scheduler, retention, disk guard

**Files:**
- Create: `internal/scheduler/{clock.go,scheduler.go}`, `internal/retention/{retention.go,disk.go}`
- Test: `internal/scheduler/scheduler_test.go`, `internal/retention/retention_test.go`

**Interfaces:**
- Consumes: store KV (Task 2), `capture.Control` (Task 6), config.
- Produces:
  - `type Jobs interface{ ProcessPending(ctx) error; ReprocessHour(ctx, day string, hour int) error; CloseDay(ctx, day string) error; Rollup(ctx, week string) error; Retain(ctx) error; DiskCheck(ctx) error; EnrollSpeaker(ctx, meetingID, speaker, name string) error }` and `var ErrMediaExpired = errors.New("raw media expired")` (implemented in Task 17)
  - `type Scheduler struct{ Cfg func() config.Config; Jobs Jobs; Store *store.Store; Logf func(string, ...any) }`
  - `func (s *Scheduler) Tick(ctx, now time.Time)`: on every tick → `DiskCheck`; on a new hour since KV `last_process_hour` → `ProcessPending` then `Retain`; at or after the close time when KV `last_close_day` ≠ today → `CloseDay(today)`; when today is `RollupWeekday`, after the close, and KV `last_rollup_week` ≠ this week → `Rollup(week)`. **Catch-up:** if `last_close_day` is earlier than yesterday, close each missed day that has data, oldest first.
  - `func (s *Scheduler) Run(ctx, every time.Duration /*1m*/)`, which calls `Tick` immediately on start (covers wake from sleep)
  - `func Sweep(p config.Paths, st *store.Store, now time.Time, days int) (removed []string, err error)`: deletes `raw/<day>/` for days older than `days`, **skipping any day with an hour in `pending`/`transcribed`**
  - `func FreeGB(path string) (float64, error)` (`syscall.Statfs`)
  - `func Guard(free, min float64) string`: returns `"normal"` if free ≥ min; `"screens_off"` if free ≥ min/2; else `"audio_off"`. `DiskCheck` sends the result to `Control.Send` only when it changes, and stores it in KV `disk_state` for the dashboard warning.

- [ ] **Step 1: Write failing tests:** `TestTickProcessesOncePerHour`, `TestTickClosesDayAtCloseTime` (17:59 no, 18:00 yes, 18:30 not again), `TestTickCatchesUpMissedDays` (last close 3 days ago with data on 2 of the days → closes both, oldest first), `TestRollupOnlyOnRollupWeekday`, `TestSweepDeletesOldDays` (now = Oct 15, days = 7 → Oct 7 removed, Oct 8 kept), `TestSweepSkipsUnprocessedDay`, `TestGuardThresholds` (min 5: 6 → normal, 3 → screens_off, 2 → audio_off).
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** PASS.
- [ ] **Step 5:** Commit: `feat: add scheduler, retention sweep and disk guard`.

---

### Task 13: Dashboard — server, day view, hour view

**Files:**
- Create: `internal/web/{server.go,day.go,hour.go}`, `internal/web/templates/{layout,day,hour}.html`, `internal/web/static/{app.css,app.js,chart.js}`
- Test: `internal/web/web_test.go`, `internal/web/seed_test.go`

**Interfaces:**
- Consumes: store, `capture.Control`, config.Paths, `skills` types, `Jobs` (Task 12).
- Produces:
  - `type Server struct{ Store *store.Store; Paths config.Paths; Cfg func() config.Config; SaveCfg func(config.Config) error; Control capture.Control; Jobs scheduler.Jobs; Voices *diarize.Voices }`
  - `func (s *Server) Handler() http.Handler` with routes `GET /` (redirects to today), `GET /day/{date}`, `GET /hour/{date}/{hh}`, `POST /pause`, `POST /resume`, `POST /close-day/{date}`, `POST /hour/{date}/{hh}/reprocess`, `POST /segment/category`, `GET /media/{date}/{kind}/{file}` (serves only from `raw/<date>/{audio,screens}`; rejects `..`; returns 410 with `raw media expired` when the file is gone), `GET /static/…` (embedded)
  - `func Seed(t *testing.T, st *store.Store, p config.Paths)` in `seed_test.go` (shared by the web tests in Tasks 13–16)

Pages (spec §8): day = status banner + pause/resume button, day summary (from `journal/<day>.md` if present), timeline bar (segments colored per category; `chart.js` is a hand-written ~100-line SVG helper, vendored), time-by-category bars, hourly notes with a `needs_review` badge and reprocess button, and "Close the day"; a disk warning when KV `disk_state` ≠ `normal`. Hour = notes, speaker-labeled transcript (each line links `#t=<offset>`), a screenshot scrubber (`<input type=range>`), an `<audio>` per chunk, and a per-segment category `<select>`. Saving a category calls `SetSegmentCategory` and adds a suggestion `app → category` to KV `rule_suggestions` (shown in Settings in Task 15).

- [ ] **Step 1: Write failing tests** (httptest + Seed): `TestDayPageShowsNotesAndTotals`, `TestDayPageNeedsReviewBadge`, `TestHourPageShowsSpeakers`, `TestMediaRejectsTraversal` (`/media/2026-10-07/screens/..%2f..%2fconfig.toml` → 400), `TestMediaExpiredReturns410`, `TestPausePostsToControl` (fake Control records `Pause`), `TestSegmentCategoryOverride`, `TestServerBindsLocalhostOnly` (`config.Default().ListenAddr` host is `127.0.0.1`).
- [ ] **Step 2:** FAIL. **Step 3:** Implement with `html/template` and `embed`. **Step 4:** PASS.
- [ ] **Step 5:** Commit: `feat(web): add dashboard day and hour views`.

---

### Task 14: Dashboard — meetings, speaker naming, search

**Files:**
- Create: `internal/web/{meeting.go,search.go}`, `templates/{meeting,search}.html`
- Test: `internal/web/meeting_test.go`, `internal/web/search_test.go`

**Interfaces:**
- Consumes: Server (Task 13), `diarize.Voices`, store participants. Uses `Jobs.EnrollSpeaker` and `scheduler.ErrMediaExpired` from Task 12 (implemented in Task 17 wiring: it re-diarizes the meeting's audio chunks if raw media still exists, averages that speaker's embedding, calls `Voices.Enroll`, and re-labels stored utterances).
- Produces routes: `GET /meeting/{id}`, `POST /meeting/{id}/speaker` (form `speaker`, `name`) → `SetParticipant(Confirmed=true)` + `EnrollSpeaker`; `GET /search?q=`.

Rendering rule: a participant with `Confirmed=false` and `Confidence < name_confidence_threshold` displays as `Speaker 2 (Priya?)`; with an empty name, `Speaker 2`. When `Cfg().Diarization == "off"`, the naming form and voice-profile UI are hidden.

- [ ] **Step 1: Write failing tests:** `TestMeetingPageLowConfidenceName` (0.5 → `Speaker 2 (Priya?)`), `TestMeetingPageConfirmedName`, `TestConfirmSpeakerEnrolls` (fake Jobs records the `EnrollSpeaker` args), `TestEnrollWithExpiredMediaShowsMessage` (Jobs returns `ErrMediaExpired` → page shows "raw media expired — name saved, voice not learned"), `TestSearchLinksToHourAndTimestamp`, `TestSearchEmptyQuery`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** PASS.
- [ ] **Step 5:** Commit: `feat(web): add meeting pages, speaker naming and search`.

---

### Task 15: Dashboard — settings

**Files:**
- Create: `internal/web/settings.go`, `templates/settings.html`
- Test: `internal/web/settings_test.go`

**Interfaces:**
- Consumes: `Server.Cfg`, `Server.SaveCfg`, `Voices.List/Delete`, KV `rule_suggestions`.
- Produces routes: `GET /settings`, `POST /settings`, `POST /settings/rule-suggestion/{accept|dismiss}`, `POST /settings/voice/{name}/delete`.

Fields: retention days, close time, rollup weekday, idle minutes, pause auto-resume, excluded apps, excluded title keywords (one per line), categories (one per line), rules (`app = Category` per line), voice profiles with delete buttons. Exclusion changes take effect after the recorder restarts; the page says so and offers a "Restart recorder" button (`Control.Send("quit")` → the supervisor restarts it with new args; `quit` is in the socket contract).

- [ ] **Step 1: Write failing tests:** `TestSettingsRoundTrip` (POST retention 14 → `SaveCfg` called with 14; the page re-renders with 14), `TestSettingsRejectsInvalid` (retention `0` → 400, message contains `retention_days`, `SaveCfg` not called), `TestAcceptRuleSuggestion`, `TestDeleteVoice`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** PASS.
- [ ] **Step 5:** Commit: `feat(web): add settings page`.

---

### Task 16: Close-day, weekly rollup, week page

**Files:**
- Create: `internal/journal/{closeday.go,rollup.go}`, `internal/web/week.go`, `templates/week.html`
- Test: `internal/journal/journal_test.go`, `internal/web/week_test.go`

**Interfaces:**
- Consumes: `skills.Runner`, `skills.Markdown`, store, paths.
- Produces:
  - `func CloseDay(ctx, r skills.Runner, st *store.Store, p config.Paths, day string) error`: first processes any remaining pending hours for that day (via an injected `func(ctx) error`), builds the close-day input, runs `close-day`, and writes `journal/<day>.md` atomically. A day with no hours writes nothing and returns nil.
  - `func Rollup(ctx, r skills.Runner, st *store.Store, p config.Paths, week string) error`: inputs are that ISO week's daily journals plus totals for this week and the prior week.
  - Routes: `GET /week/{yyyy-Www}`, `POST /week/{yyyy-Www}/rollup`. The page renders the Markdown (`github.com/yuin/goldmark`, raw HTML disabled) and per-day category totals.

- [ ] **Step 1: Write failing tests:** `TestCloseDayWritesMarkdown`, `TestCloseDaySkipsEmptyDay`, `TestCloseDayInputHasTotals` (the fake Runner captures the input file; totals in minutes match the seeded segments), `TestRollupUsesPriorWeekTotals`, `TestWeekPageRendersMarkdownSafely` (`<script>` in the Markdown is not rendered as a tag).
- [ ] **Step 2:** FAIL. **Step 3:** Implement. **Step 4:** PASS.
- [ ] **Step 5:** Commit: `feat: add close-day, weekly rollup and week page`.

---

### Task 17: CLI wiring, setup, doctor, README

**Files:**
- Create: `cmd/journal/{main.go,app.go,setup.go,doctor.go}`, `README.md`
- Test: `cmd/journal/doctor_test.go`

**Interfaces:**
- Consumes: everything above.
- Produces the CLI: `journal serve | setup | doctor | process [--day D --hour H] | close-day [--day D] | rollup [--week W]`, each taking `--config` (default `~/WorkJournal/config.toml`).
  - `app.go`: `func Build(cfg config.Config) (*App, error)` constructs Store, Pipeline, SkillSummarizer, Scheduler, Supervisor, Server, and a `Jobs` implementation (including `EnrollSpeaker`). `serve` runs the Supervisor, Scheduler and HTTP server under one context and stops cleanly on SIGINT/SIGTERM, flushing the recorder with `Control.Send("quit")`.
  - `setup`: `Paths.Ensure`; writes `config.toml` if missing; downloads the whisper model `https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-<model>.bin` and the sherpa models chosen in `docs/spike-diarization.md` into `models/` (skipping files that exist; verifying size > 0); runs `recorder/build.sh`; runs `tessl install file:./work-journal/skills/work-journal --yes` from the repo root.
  - `doctor`: `type Check struct{ Name string; OK bool; Fix string }`, `func RunChecks(deps DoctorDeps) []Check` with injectable lookups. Checks: `whisper-cli` on PATH, whisper model present, sherpa models present (if GO), `claude` on PATH, skills installed (`.claude/skills/*summarize-hour*` exists), recorder app built, `recorder selftest` passes (which covers the permissions), free disk ≥ `min_free_gb`. Prints `✓`/`✗` with each fix; exits 1 if any check fails.

- [ ] **Step 1: Write failing tests:** `TestDoctorReportsMissingWhisper` (the fake lookup fails → `Check{Name:"whisper-cli",OK:false}` with a Fix containing `brew install whisper-cpp`), `TestDoctorAllOK`.
- [ ] **Step 2:** FAIL. **Step 3:** Implement; write `README.md` (prerequisites from Task 0, `journal setup`, `journal doctor`, `journal serve`, a privacy section on where data lives, exclusions and voice profiles, and a link to `docs/manual-checklist.md`).
- [ ] **Step 4:** `make test` → all PASS; `make build`; `bin/journal doctor` → every check `✓` (the human grants permissions if prompted).
- [ ] **Step 5: End-to-end smoke (human-observed):** run `bin/journal serve` across an hour boundary with a short test call; confirm the hour reaches `done` with notes, the meeting page shows separated speakers, search finds a spoken word, and "Close the day" writes `~/WorkJournal/journal/<today>.md`. Record the results at the bottom of `docs/manual-checklist.md`.
- [ ] **Step 6:** Commit: `feat: wire journal CLI with setup and doctor`.
