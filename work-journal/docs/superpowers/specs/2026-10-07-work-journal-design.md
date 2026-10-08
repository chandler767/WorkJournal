# Work Journal — Design Spec

**Date:** 2026-10-07
**Status:** Approved in conversation, pending written-spec review

## 1. Purpose

Automatically capture what happens on the user's Mac — screen, system audio,
microphone, active apps — and turn it into a work journal that answers:

- **Daily review:** what did I do today, and where did my time go?
- **Searchable memory:** what was said / on screen at a given moment?
- **Weekly rollups:** themes, time trends, and a draft status update.

AI interpretation (summaries, categorization, meeting notes, rollups) is done
by **skills** run through Claude Code, not hard-coded logic, so behavior can
change by editing `SKILL.md` files.

### Success criteria (v1)

1. Running `journal serve` all workday produces, without manual steps, hourly
   notes, a categorized timeline, and a daily journal Markdown at close time.
2. Any moment within the retention window can be found by search and replayed
   (screenshot + audio).
3. Meetings are detected, transcribed with speakers separated, and summarized
   with decisions and action items; known voices are named automatically.
4. Raw media older than `retention_days` (default 7) is deleted; derived text
   and journals are kept indefinitely.

### Out of scope (v1)

Goal tracking (planned next; data model must not preclude it), multi-Mac
sync, sharing/export beyond Markdown, cloud transcription, Windows/Linux.

## 2. Constraints & decisions

| Decision | Choice |
|---|---|
| Platform | macOS 14+ (ScreenCaptureKit system audio) |
| Main language | Go 1.23+ (no Python) |
| Native helper | Small Swift app — capture + OCR only |
| Processing | Local: whisper.cpp transcription, Apple Vision OCR, sherpa-onnx diarization |
| Raw retention | Keep raw media; `retention_days = 7`, configurable in Settings |
| AI layer | Skills in a Tessl plugin, invoked via `claude -p` |
| Cadence | Hourly summaries + daily close + weekly rollup |
| UI | Local web dashboard at `127.0.0.1:4747` + recorder menu-bar icon |

## 3. Architecture

```
work-journal/
├── recorder/              Swift → WorkJournalRecorder.app (capture, menu bar, `ocr`)
├── cmd/journal/           Go CLI: serve | setup | doctor | process | close-day | rollup
├── internal/
│   ├── config/            load/save config.toml
│   ├── capture/           supervise recorder process, pause/resume, gap logging
│   ├── process/           per-hour: transcribe, OCR, diarize → derived JSON
│   ├── meetings/          meeting detection, speaker naming, voice profiles
│   ├── skills/            SkillRunner: `claude -p`, JSON schema validation, retry
│   ├── categorize/        rule-based categorization before falling back to skill
│   ├── scheduler/         hourly tick, close time, Friday rollup, wake catch-up
│   ├── retention/         raw-media deletion
│   ├── store/             SQLite (modernc.org/sqlite, pure Go) + FTS5
│   └── web/               net/http + html/template dashboard
├── skills/work-journal/   Tessl plugin (see §6)
└── config.example.toml
```

Every external dependency is behind a Go interface (`Recorder`, `Transcriber`,
`OCR`, `Diarizer`, `SkillRunner`, `Clock`) so packages are testable with fakes.

### Data directory (`~/WorkJournal/`, configurable; never inside the repo)

```
config.toml
journal.db                         SQLite, rebuildable from derived/
raw/YYYY-MM-DD/                    deleted after retention_days
  audio/HHMM-system.m4a            5-min chunks, others' audio
  audio/HHMM-mic.m4a               5-min chunks, user's mic
  screens/HHMMSS.jpg               every 10s (5s in meetings), skipped if unchanged
  activity.jsonl                   app/window/idle/pause/excluded events
derived/YYYY-MM-DD/HH.json         transcript, OCR text, segments, status — kept
journal/YYYY-MM-DD.md              daily journal — kept
journal/YYYY-Www.md                weekly rollup — kept
voices/<person>.json               speaker embeddings — local only, deletable
models/                            whisper + sherpa-onnx models
```

## 4. Capture (Swift recorder)

- **Screen:** ScreenCaptureKit; main display by default (`all_displays` option);
  JPEG at 1440px width per display (side by side with `all_displays`); perceptual-diff skip when unchanged.
- **Audio:** two tracks — system (ScreenCaptureKit) and mic (AVAudioEngine) —
  written as 5-minute AAC `.m4a` chunks.
- **Activity:** frontmost app + window title on change; idle after
  `idle_minutes` (default 5) without input.
- **Exclusions:** when an excluded app/window is frontmost (defaults:
  1Password, Keychain Access, window titles containing "Private" /
  "Incognito"; user-editable), screenshots and audio stop; only an
  `excluded` event is logged.
- **Pause:** from menu bar or dashboard; auto-resume after
  `pause_auto_resume_minutes` (default 60).
- **Menu bar:** status icon (recording / paused / error), Pause/Resume,
  Open Dashboard, Quit.
- **Control interface:** Go launches the recorder with `--data-dir`; control
  via a Unix socket (`pause`, `resume`, `status`, `rotate` — finish the
  current audio chunks now and start new ones). `recorder ocr <files…>`
  prints JSON OCR results using Vision. `recorder --selftest` verifies capture.
- **Permissions:** Screen & System Audio Recording, Microphone, Accessibility
  — attached to the ad-hoc-signed `WorkJournalRecorder.app`.

## 5. Processing pipeline (Go)

At each hour boundary (and on catch-up after sleep), for every hour with
status `pending`:

1. **Transcribe** each audio chunk with `whisper-cli` (model `base.en`
   default, configurable) → timestamped segments; mic = "me".
2. **Diarize** system-track chunks with sherpa-onnx (Go bindings) →
   `Speaker N` labels per segment; match embeddings against `voices/`
   (cosine similarity ≥ `voice_match_threshold`, default 0.75) to name known
   people.
3. **OCR** screenshots via `recorder ocr` → text per screenshot (deduplicated).
4. **Activity segments** from `activity.jsonl`.
5. **Meeting detection:** a meeting spans time where a meeting app (Zoom,
   Teams, FaceTime, Slack huddle, browser titles matching Meet/Teams/Zoom) is
   active, or both audio tracks have sustained speech ≥ 2 min.
6. Write `derived/YYYY-MM-DD/HH.json`; status → `transcribed`.
7. **Categorize:** apply `config.toml` rules; send unmatched segments to
   `categorize-time`.
8. **Summarize:** `summarize-hour`; for each meeting ending in the hour,
   `summarize-meeting`. Status → `done` (or `needs_review` on failure).

Daily close (`close_time`, default 18:00, or the "Close the day" button)
runs `close-day`. Fridays at close time (or on demand) run `weekly-rollup`.

## 6. Skills (`skills/work-journal` Tessl plugin)

Invocation:
`claude -p "<instruction naming the skill and input file>" --output-format json --allowedTools Read`
Output is validated against a JSON schema per skill. Invalid → one retry →
`needs_review` (input preserved; re-runnable from the UI).

| Skill | Input | Output |
|---|---|---|
| `summarize-hour` | derived hour JSON | 3–6 notes, main focus, action items |
| `categorize-time` | uncovered segments + hour summary + categories | category + confidence per segment |
| `summarize-meeting` | meeting transcript (speaker-labeled), meeting OCR, title | participants with proposed names + confidence, summary, decisions, action items (owner) |
| `close-day` | day's hour summaries, meetings, category totals | `journal/YYYY-MM-DD.md` |
| `weekly-rollup` | week's daily journals + totals vs prior week | `journal/YYYY-Www.md` incl. draft status update |

Default categories: Deep work, Meetings, Communication, Admin,
Browsing/Research, Break/Idle. Rules like `"Figma" = "Design"` are matched
first, without calling Claude.

Each skill ships 3–5 fixture inputs used by skill evals (§9). Voice
embeddings are never sent to Claude; only names and text are.

## 7. Speaker identification

- Mic track is always the user.
- Unknown system-track speakers are labeled `Speaker N`; `summarize-meeting`
  proposes names from transcript cues and on-screen participant labels.
  Names below `name_confidence_threshold` (0.7) render as
  `Speaker 2 (Priya?)`.
- On the meeting page the user confirms/corrects a name; the speaker's
  embedding is saved to `voices/<person>.json` and used for future matching.
- Settings lists voice profiles with delete buttons.
- **Risk:** diarization quality is unverified on this hardware. Step 3b of the
  build is a spike; if quality is poor, fall back to me/others split plus
  skill-inferred names and record that decision.

## 8. Dashboard (`journal serve`, binds `127.0.0.1:4747` only)

Server-rendered HTML, minimal vanilla JS, one vendored chart script, no CDN.

- **Day** `/day/{date}`: recording status + pause, day summary, category-colored
  timeline, time-by-category bars, hourly notes (`needs_review` highlighted),
  "Close the day".
- **Hour** `/hour/{date}/{hh}`: notes, speaker-labeled transcript, screenshot
  scrubber, audio player, per-segment category override (saved as a rule
  suggestion).
- **Meeting** `/meeting/{id}`: participants, notes, decisions, action items,
  transcript, speaker naming.
- **Search** `/search?q=`: FTS5 over transcripts, OCR, notes; results deep-link
  to hour + timestamp.
- **Week** `/week/{yyyy-Www}`: rollup Markdown, per-day category totals,
  "Generate rollup".
- **Settings** `/settings`: retention days, close time, excluded apps,
  categories + rules, voice profiles; writes `config.toml`.
- Expired raw media shows "raw media expired"; text remains.

## 9. Error handling

| Failure | Behavior |
|---|---|
| Recorder crash | Restart with exponential back-off; log gap; menu bar red after 3 failures |
| whisper / sherpa / claude unavailable | Hour stays `pending`/`transcribed`; retried next tick |
| Mac asleep | Logged as gap (not idle); scheduler catches up on wake |
| Free disk < `min_free_gb` (5) | Stop screenshots, then audio; dashboard warning |
| Retention vs. unprocessed hour | Skip deleting raw files for hours not `done`/`needs_review` |
| Invalid skill output | One retry, then `needs_review` |

## 10. Testing

- TDD for all Go packages using interface fakes and a fake `Clock`.
- Store tests on temp SQLite (FTS, totals, rebuild from derived).
- Web tests with `httptest` on seeded data; settings round-trip.
- Skill evals behind `-tags=skills` against real `claude -p`: schema-valid,
  categories from allowed set, no invented meetings on empty transcripts,
  low-confidence names flagged.
- `recorder --selftest` plus a manual capture checklist (permissions,
  exclusions, pause, both audio tracks).
- `journal doctor`: checks permissions, `whisper-cli`, models, `claude`, disk.

## 11. Build order

1. Go skeleton, config, data dirs, SQLite store
2. Swift recorder (capture, exclusions, menu bar, socket, `ocr`, selftest); `journal doctor`
3. Hourly pipeline: transcription, OCR, scheduler, retention
3b. **Spike:** sherpa-onnx diarization from Go on a recorded test call
4. Skills plugin (`summarize-hour`, `categorize-time`, `summarize-meeting`) + SkillRunner
5. Dashboard: day, hour, meeting (speaker naming, voice profiles), search, settings
6. `close-day`, `weekly-rollup`, week page

## 12. Prerequisites

Xcode Command Line Tools, Go 1.23+, `brew install whisper-cpp`, `claude` CLI,
`tessl` CLI. `journal setup` downloads whisper `base.en` (~150 MB) and
sherpa-onnx segmentation + embedding models (~40 MB) into `models/`.
