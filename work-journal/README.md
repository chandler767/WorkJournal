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

## Screen capture

Screenshots follow what you do rather than a fixed timer: one when you switch apps or windows,
one when the screen changes (watched cheaply at low resolution), never more than one per
`screenshot_min_gap_sec` (3 s; a change inside the gap is taken at its end), and at least one per
`screenshot_idle_backstop_sec` (60 s; 5 s in meetings) even when nothing changes. Set
`screen_change_sensitivity` to `low` (big changes only), `medium` or `high` (small edits too), and
`capture_on_app_switch` to skip the switch trigger. All four are on the Settings page under
Screen capture and apply after Restart recorder. `screenshot_interval_sec` is no longer used.

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

## Meetings

A meeting is time in a meeting app or window (`meeting_apps`, `meeting_title_keywords`), a
call where your mic and the system audio both carry speech (`meeting_min_two_way_minutes`),
or an **in-person** meeting: a conversation only your mic hears, where speech fills at least
`in_person_speech_ratio` (0.4) of a rolling 5-minute window for at least
`in_person_min_minutes` (5; pauses under 2 minutes do not split it). A locked or idle screen
does not stop detection. With speaker identification on, an in-person meeting also needs
at least two voices on the mic; one voice (dictation, or your side of a call on another
device) is not a meeting and stays "me". A TV or a speakerphone on another device can still
look like an in-person meeting. In-person meetings show as "In person". An app or call meeting
wins over the same time: a short call during a conversation splits it, and each remaining part
of at least `in_person_min_minutes` is its own in-person meeting. A meeting that is no longer
detected when its hour is processed again is removed, unless you confirmed a participant's name.

All active, idle and locked-screen time inside any meeting (app, call or in person) is
categorized **Meetings**, ahead of your own `[rules]`; paused, excluded and off time stays
uncounted.

Isolated "You", "Thank you", "Bye" and similar lines that whisper invents on silence are
dropped; the same words within 3 s of real speech on either track are kept.

## Speaker identification

With `diarization = "sherpa"` (the default), each system-audio chunk is split into speaker
turns locally with [sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx): pyannote
segmentation 3.0 finds the turns and NeMo TitaNet-small gives each speaker a voice
embedding. Your microphone is "me", except during an in-person meeting: there the mic
is diarized too, since everyone in the room is on it. The diarizer works one 5-minute chunk
at a time and splits one voice into many pieces, so the pieces are then grouped into voices
across a whole meeting (all its chunks, in every hour it spans) and, outside meetings,
across the hour. Voices are labelled `Speaker 1`, `Speaker 2`, … in order of first
appearance within the meeting (one numbering for both tracks; a voice keeps its number in
every hour of the meeting), or by name once you have named them. A voice with less than
`speaker_min_seconds` (default 20) of speech joins the most similar voice, or is
`Speaker ?` when it sounds like nobody else; in-meeting mic speech no speaker turn covers
is `Speaker ?` too. Only real voices are listed as meeting participants. To be "me" in
in-person meetings, name your own voice exactly `me` once on an in-person meeting's page. Nothing is sent
anywhere; diarization adds a few seconds per 5-minute chunk.

- Models: `journal setup` downloads `sherpa-onnx-pyannote-segmentation-3-0/model.onnx`
  and `nemo_en_titanet_small.onnx` into `~/WorkJournal/models/`; `journal doctor` checks
  them, and that speaker separation works on a short synthesized two-voice recording.
  Without them, system speech is labelled "others" (a warning is logged). When
  diarization fails on more than half of an hour's chunks, `logs/serve.log` gets one
  `WARNING: speaker separation failed …` line with the first error; failed chunks are
  diarized again on the next pass. A chunk whose converted WAV the diarizer cannot read is
  still transcribed; only its speakers are unknown (and it counts as a failure).
- Naming: on a meeting's page, name a speaker. While the meeting's raw audio still exists
  (`retention_days`), the voice embeddings of that speaker's lines (from the cached
  diarization, or the chunks diarized again) are saved as a voice profile in
  `voices/<name>.json`, and their lines in that meeting are relabelled with the name (an
  hour whose "Speaker N" sounds different, e.g. one labelled by an older version, is left
  alone). Later recordings of a voice that matches a profile get the
  name automatically. After retention removed the audio, the name is saved but no voice
  is learned.
- Voice profiles stay in `voices/` on this machine and are never passed to Claude.
  Delete them under Settings → Voice profiles.
- Thresholds: `voice_match_threshold` (default 0.60) is the cosine similarity at which a
  voice matches a profile. Raise it if different people get the same name; lower it if a
  known voice is not recognized. `speaker_cluster_threshold` (default 0.45, 0.2–0.9) is
  the similarity at which pieces of speech count as one voice: raise it if different people
  share a label, lower it if one person shows up as several speakers. Two similar voices
  may still be merged into one speaker.
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
