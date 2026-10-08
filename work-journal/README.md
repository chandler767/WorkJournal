# work-journal

A local work journal for macOS. A small recorder app captures screenshots, system audio,
your microphone and which app/window is active. The `journal` binary turns each finished
hour into a transcript, screen text and notes, writes a daily journal entry at your close
time and a weekly rollup, and serves a dashboard at http://127.0.0.1:4747.

Summaries are written by Claude Code skills (`summarize-hour`, `summarize-meeting`,
`categorize-time`, `close-day`, `weekly-rollup`) run headlessly with `claude -p`.

## Prerequisites

- macOS 14 or later
- Xcode Command Line Tools (`xcode-select --install`) for `swiftc` and `codesign`
- Go 1.26 or later (`brew install go`)
- whisper.cpp: `brew install whisper-cpp` (provides `whisper-cli`)
- Claude Code (`claude` on your `PATH`), signed in
- The Tessl CLI (`tessl`), used to install the skills

## Setup

From this directory:

```sh
make build              # builds bin/journal (and copies the sherpa-onnx dylibs next to it)
bin/journal setup       # data dir + config, builds the recorder (if not built), downloads the
                        # whisper model (~148 MB for base.en) and the speaker models (~47 MB),
                        # installs the skills
bin/journal doctor      # checks everything below; fix anything marked ✗
bin/journal serve       # recorder + scheduler + dashboard; Ctrl-C to stop
```

Every command takes `--config PATH` (default `~/WorkJournal/config.toml`) and `--repo DIR`
(the repo root whose `.claude/skills` holds the installed skills; found automatically by
walking up from `bin/journal`).

One-shot jobs, useful when `serve` is not running:

```sh
bin/journal process                          # process every finished, pending hour
bin/journal process --day 2026-10-07 --hour 14   # reprocess one hour
bin/journal close-day [--day 2026-10-07]     # write journal/<day>.md (default today)
bin/journal rollup [--week 2026-W41]         # write journal/<week>.md (default this week)
```

`make build` copies `libsherpa-onnx-c-api.dylib` and `libonnxruntime.dylib` from the Go
module cache into `bin/` and links `bin/journal` to load them from its own directory, so it
keeps working after `go clean -modcache`. Keep the three files together if you move them. A
plain `go build ./cmd/journal` also works, but that binary loads the dylibs from the module
cache.

## The current hour

Finished hours are processed a couple of minutes after they end. The hour in progress is
summarized too, every `process_interval_minutes` (default 15, so at :15, :30 and :45; 5, 10,
15, 20, 30 or 60, also on the Settings page; 60 turns this off). Its notes are marked **in progress** on the
dashboard and replaced by the full summary once the hour ends. The **Process now** button on
today's page asks the recorder to close its current audio chunks and processes everything
recorded up to that moment (or up to the start of a chunk the recorder could not close).
Otherwise only finished audio chunks are used: the newest chunk of each
track waits until the recorder has rotated it or not written it for 30 s.

Each audio chunk is transcribed (and diarized) and each screenshot OCRed once; the results
are kept in `cache/<day>/` and reused by later runs and the final pass. A cache entry is
redone when its file's size or modification time changes, or the whisper model changes. An
unchanged in-progress hour is not summarized again.

## Start at login

```sh
bin/journal autostart on       # run `journal serve` at login, and start it now
bin/journal autostart status
bin/journal autostart off      # stop it now and at login
```

It runs as a per-user LaunchAgent (`~/Library/LaunchAgents/local.workjournal.serve.plist`) that
logs to `~/WorkJournal/logs/serve.log` and is restarted if it crashes. Stop any `serve` you
started in a terminal first. The PATH captured when you run `autostart on` is used to find
`whisper-cli` and `claude`, so run it from a normal terminal. Use the menu-bar Pause to stop
recording temporarily; quitting the recorder from its menu only restarts it.

## Speaker identification

With `diarization = "sherpa"` (the default), each system-audio chunk is split into speaker
turns locally with [sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx): pyannote
segmentation 3.0 finds the turns and NeMo TitaNet-small gives each speaker a voice
embedding. Your microphone is always "me". Other voices are labelled `Speaker 1`,
`Speaker 2`, … within each hour, or by name once you have named them. Nothing is sent
anywhere; diarization adds a few seconds per 5-minute chunk.

- Models: `journal setup` downloads `sherpa-onnx-pyannote-segmentation-3-0/model.onnx`
  and `nemo_en_titanet_small.onnx` into `~/WorkJournal/models/`; `journal doctor` checks
  them. Without them, system speech is labelled "others" (a warning is logged).
- Naming: on a meeting's page, name a speaker. While the meeting's raw audio still exists
  (`retention_days`), its chunks are diarized again, that speaker's voice embedding is
  saved as a voice profile in `voices/<name>.json`, and their lines in that meeting are
  relabelled with the name (labels restart each hour, so a later hour whose "Speaker N"
  sounds different is left alone). Later recordings of a voice that matches a profile get the
  name automatically. After retention removed the audio, the name is saved but no voice
  is learned.
- Voice profiles stay in `voices/` on this machine and are never passed to Claude.
  Delete them under Settings → Voice profiles.
- Thresholds: `voice_match_threshold` (default 0.60) is the cosine similarity at which a
  voice matches a profile or a speaker heard earlier in the same hour. Raise it if
  different people get the same name; lower it if a known voice is not recognized. The
  speaker clustering threshold (0.7) is fixed. Two similar voices taking quick turns may
  be merged into one speaker.
- Set `diarization = "off"` to label all system speech "others" and hide speaker naming.

## Permissions

The recorder (`recorder/build/WorkJournalRecorder.app`) needs, in System Settings →
Privacy & Security:

- Screen & System Audio Recording
- Microphone
- Accessibility (for window titles)

`journal doctor` runs the recorder's selftest (including Accessibility) and tells you which
grant is missing.

**Keep grants across rebuilds:** create a self-signed code-signing certificate named
`WorkJournal Local Signing` (Keychain Access → Certificate Assistant → Create a Certificate…,
Identity Type "Self-Signed Root", Certificate Type "Code Signing"). `recorder/build.sh` signs with
it when present (override with `WORKJOURNAL_SIGN_IDENTITY`), so macOS keeps the grants after
rebuilds. Without it the app is ad-hoc signed and **every rebuild resets the grants**: remove the
stale entry with − and add the app again with +. A plain `journal setup` keeps an
already-built recorder.

## Privacy

- Everything stays in `~/WorkJournal` (or your `data_dir`): `raw/` holds screenshots,
  audio and activity; `derived/` and `journal.db` hold transcripts, screen text and
  summaries; `journal/` holds the daily and weekly Markdown.
- After `retention_days` (default 7), each day's whole `raw/<day>/` directory is deleted
  (screenshots, audio and `activity.jsonl`), together with its processing cache
  `cache/<day>/`, once all of that day's hours are processed.
- `cache/` holds per-chunk transcripts, speaker turns and voice embeddings and per-screenshot
  text. It is owner-only and kept outside `derived/`, so it is never passed to Claude.
  Transcripts, screen text, time segments, summaries and journals are kept.
- Exclusions: while an excluded app (default: 1Password, Keychain Access) or a window
  title containing an excluded keyword (default: Private, Incognito) is in front, no
  screenshots are taken and audio is silenced. The microphone indicator stays on during
  exclusions, because audio is silenced rather than stopped.
- Voice profiles (`voices/`) never leave the machine and are never passed to Claude
  (see [Speaker identification](#speaker-identification)).
- Only text (transcripts, screen text, activity summaries) goes to Claude. Each `claude -p`
  run uses the repo root as its working directory, so it loads that repo's project
  settings (`.claude/`, `CLAUDE.md`) and installed skills. Skill inputs are written to,
  and read from, `derived/` only. The session has only the Skill and Read tools and no
  MCP servers (`--strict-mcp-config`); `derived/` is added with `--add-dir`, and raw media,
  `voices/` and `journal.db` are never passed to it. These are Claude Code's tool and
  directory settings, not an OS sandbox.
- The dashboard binds to loopback only; `journal serve` refuses any other `listen_addr`.

## More

- Manual recorder checks: [docs/manual-checklist.md](docs/manual-checklist.md)
- Design: [docs/superpowers/specs/2026-10-07-work-journal-design.md](docs/superpowers/specs/2026-10-07-work-journal-design.md)
- Settings: [config.example.toml](config.example.toml), or the dashboard's Settings page
