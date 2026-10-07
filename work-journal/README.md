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
make build              # builds bin/journal
bin/journal setup       # data dir + config, builds the recorder, downloads the
                        # whisper model (~148 MB for base.en), installs the skills
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

## Permissions

The recorder (`recorder/build/WorkJournalRecorder.app`) needs, in System Settings →
Privacy & Security:

- Screen & System Audio Recording
- Microphone
- Accessibility (for window titles)

`journal doctor` runs the recorder's selftest and tells you which grant is missing. The app
is ad-hoc signed, so **every rebuild resets these grants**: after `journal setup` or
`./recorder/build.sh`, remove the stale entry and add the app again.

## Privacy

- Everything stays in `~/WorkJournal` (or your `data_dir`): `raw/` holds screenshots,
  audio and activity; `derived/` and `journal.db` hold transcripts, screen text and
  summaries; `journal/` holds the daily and weekly Markdown.
- Raw screenshots and audio are deleted after `retention_days` (default 7), once their
  hours are processed. Transcripts, summaries and journals are kept.
- Exclusions: while an excluded app (default: 1Password, Keychain Access) or a window
  title containing an excluded keyword (default: Private, Incognito) is in front, no
  screenshots are taken and audio is silenced. The microphone indicator stays on during
  exclusions, because audio is silenced rather than stopped.
- Voice profiles (`voices/`) never leave the machine and are never passed to Claude.
  Speaker diarization is currently off (`diarization = "off"`), so no profiles are made.
- Only text (transcripts, screen text, activity summaries) goes to Claude. Each `claude`
  run gets only the Skill and Read tools, no MCP servers, and read access to the data dir.
- The dashboard binds to loopback only; `journal serve` refuses any other `listen_addr`.

## More

- Manual recorder checks: [docs/manual-checklist.md](docs/manual-checklist.md)
- Design: [docs/superpowers/specs/2026-10-07-work-journal-design.md](docs/superpowers/specs/2026-10-07-work-journal-design.md)
- Settings: [config.example.toml](config.example.toml), or the dashboard's Settings page
