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

## Time zones

Times are kept and shown on the clock where they were recorded. A meeting recorded at 10:05 in
London still reads 10:05 after you fly to New York, marked with its zone (**10:05 BST**)
whenever that zone is not the Mac's. The zone is named only when the offset leaves no doubt;
otherwise it reads, for example, **UTC+01:00**. Each day is processed in the zone it was
recorded in, which comes from the offsets in that day's activity log. Days already processed
in another zone can be reprocessed, and their cached speech is reused.

Each moment belongs to exactly one hour: the hour its clock read at the time, using the zone
in force when it happened. A flight is filed under the departure clock's hours until the
first activity on the new clock.

The recorder logs a `zone` event whenever the Mac's time zone or UTC offset changes, and
when it starts or a new day begins. That marks exactly when the clock moved. Without one
(an older recorder, for example), a recording made between the change and the next
activity event is placed by its name: 10:30 after 09:10 CDT and before 10:45 EDT is
10:30 EDT. It stays in the hour it is named in, and an exclusion or pause in force then
still removes it.

Overnight, the hours of a new day before its first event use that day's first offset. If
the zone changed overnight with no event logged at the change, the first hour of the new day
can overlap the end of the previous one. The `zone` event logged at the start of each day
usually prevents this.

Caveat: when the clock goes back during a day (flying west, or the autumn DST change), the
same clock hour happens twice. Both stretches are filed under that one hour of the day, so
they read as one hour, though nothing is counted twice. Where an audio chunk or screenshot
name repeats, only one file can exist. Speech in the repeated stretch can then be placed on
the wrong side of the change.

## Claude usage limits

When Claude reports a usage or rate limit, work-journal stops asking Claude until the limit
resets (the time Claude gives, else about an hour, at most 24 hours; a reset time with a
date, such as "resets Oct 20, 9am", counts as unknown, and a clock time is read as local
time). Recording, transcription, speaker separation and OCR go on, and their results are
cached locally, so nothing is lost. Hours with speech stay pending until Claude resumes
(their speech must pass the privacy filter first); hours without speech wait as transcribed.
None are marked needs review, and the day's journal and weekly rollup wait too. Once the
limit resets, the waiting hours are processed oldest first. Meanwhile the day and hour pages say
"Claude usage limit reached — summaries paused until 3:00 PM" with a **Run now anyway**
button, which tries straight away (and pauses again if Claude is still limited).
`journal doctor` reports the limit and its reset time. Settings → Claude usage
(`pause_on_usage_limit`) turns pausing off; Claude is then asked again on the usual schedule.

## Ask your journal

Type a question into the **Ask** box at the top of the day page, or open **Ask** (`/ask`):
"What did we decide about the release?", "How long was I in meetings yesterday?", "What
was I doing at 2pm on Tuesday?". `journal serve` runs `claude -p` in the background with
only the journal's own read-only tools (no file, shell or web tools, no other MCP servers,
in an empty temporary directory so no project settings or skills load). Answers cite the
hours and meetings they come from as dashboard links, and say so when the journal does not
know. The page updates when the answer lands (up to 3 minutes); one question runs at a time,
and the last 20 questions and answers are kept on the Ask page. During a Claude usage-limit
pause, questions wait too; **Ask anyway** tries that one question now (summaries stay paused).
Ask loads no Claude settings files at all (`--setting-sources ""`), so no user hooks see the
journal data it reads. Because transcripts and screen text can contain anything you saw or heard,
Claude is told to treat them as untrusted data, and the dashboard shows no images from Claude
or the journals and keeps only links into the dashboard itself; a Content-Security-Policy
stops every page from loading anything from another site.

The same tools are available to any Claude client through `journal mcp`, an MCP server over
stdio. It opens the database read-only, so it runs safely beside `journal serve`; its one
write tool, `add_note`, can only add a [note](#tell-the-journal) and never edits or deletes
anything (Ask does not get it). It returns only what the dashboard shows: summaries,
privacy-filtered transcripts (removed speech appears only as "4 min of non-work audio
removed"; hours whose privacy check failed show that note, not speech), meetings, time
totals, the journal entries and your notes. It never returns raw recordings, screenshots,
file paths, `cache/`, voice profiles or privacy decisions.

| Tool | What it returns |
| --- | --- |
| `search(query, from?, to?, limit≤50, offset?)` | ranked snippets from transcripts, screen text, notes and meeting summaries, with dashboard links |
| `get_day(date)` | the daily journal entry, each hour's status, narrative, notes and action items, minutes per category, the day's meetings |
| `get_hour(date, hour, include_transcript?, transcript_offset?)` | the hour's summary, timeline, activity and meetings; the transcript on request, paged |
| `list_meetings(from, to, person?)` | meetings with time, app, title, participants and a one-line summary |
| `get_meeting(id, include_transcript?, offset?)` | summary, decisions, action items, participants; the transcript on request, paged |
| `time_report(from, to, group_by)` | minutes by `category`, `day` or `app` |
| `get_week(week)` | the weekly rollup and minutes per category for each day |
| `list_notes(date, offset?)` | the notes you wrote on that day: id, kind, time range and text |
| `add_note(date, kind, text, from?, to?)` | adds a context note or a correction (see [Tell the journal](#tell-the-journal)); the only write |

Dates are `YYYY-MM-DD`, `today` or `yesterday`; weeks are `YYYY-Www` (e.g. `2026-W41`).
Each response is capped at about 40 KB; a cut response says which offset to ask for next.

Register it with Claude Code (once, for every project):

```sh
claude mcp add --scope user work-journal -- /absolute/path/to/work-journal/bin/journal mcp
```

Add `--config /path/to/config.toml` after `mcp` if your config is not
`~/WorkJournal/config.toml`. For Claude Desktop, add it to
`~/Library/Application Support/Claude/claude_desktop_config.json` and restart Claude Desktop:

```json
{
  "mcpServers": {
    "work-journal": {
      "command": "/absolute/path/to/work-journal/bin/journal",
      "args": ["mcp"]
    }
  }
}
```

`go test -tags=ask -run TestAskEval ./internal/mcpserver/` checks real answers over a
fixture day (it needs a logged-in `claude`).

## Tell the journal

You can add what the recordings miss, or correct what they got wrong, with a note on an
hour, a time range within a day, or a whole day. There are two kinds:

- **Context**: free text or a pasted document, such as "this was the Cloudflare planning
  call". A note with a time range goes to the hour and meeting summaries for that time; a
  whole-day note does not. Both go to the day's journal entry (and so to the weekly
  rollup), and search finds them. It is your note, never a
  recording: it adds no time to any category and cannot create or name a meeting on its
  own. A context note that names people does not name a speaker unless that speaker
  introduces themselves by that name in the transcript.
- **Correction**: an instruction about what was recorded, such as "13:10–13:20 was people
  next to me, not me; disregard it" or "the person called Speaker 2 at 15:00 is Maya". It
  goes to the [privacy filter](#privacy) as a strong hint for the speech it covers, and to
  the summaries as your own statement. Speech a correction says to disregard is removed
  just like the filter's own removals: it never reaches `derived/`, search, meeting
  detection, notes or the journal, and the hour page shows "speech removed by your note".

Add a note in the **Tell the journal** box on an hour or day page (kind, an optional time
range, and a large text box for pasting). Existing notes are listed there with edit and
delete. From the terminal:

```sh
journal note add --date 2026-10-21 --kind correction --from 13:10 --to 13:20 \
  --text "people next to me, not me; disregard it"
journal note add --date 2026-10-21 --kind context --from 14:00 --to 15:00 \
  --file ~/Desktop/planning-notes.md
journal note list --date 2026-10-21
```

or from a Claude chat with the MCP `add_note` tool. A note is at most 100 KB of text;
binary files are refused.

Saving, changing or deleting a note queues the hours it covers, and `journal serve` picks
them up within a minute: a correction clears those hours' cached privacy decisions (as
Reprocess does) and processes them again; a context note has them summarized again. If
the day's journal entry or that week's rollup is already written, it is written again.
Hours not processed yet just read the note when they are. **A correction cannot bring back
audio that retention has already deleted**: once `raw/<day>/` is gone (after
`retention_days`), the note is still saved and the summaries are written again with it,
but the transcript stays as it was. Notes are things you wrote, not recordings, so
retention never removes them; they live in `journal.db`. With the privacy filter turned off
(`privacy_filter = false`) a correction still reaches the summaries but removes no speech.

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

## Audio modes

Audio (microphone and system audio) has three modes; screenshots are not affected, and Pause
still stops everything:

- **On** (the default): audio is always recorded.
- **Off**: no audio is recorded and the microphone is not used.
- **Auto**: the microphone listens but nothing is written. The last two minutes of both
  tracks are kept in memory only. Recording starts when a call is detected (another app is
  using the microphone, or a meeting app or window is in front) or when the microphone hears
  a back-and-forth conversation (speech in at least 40% of the last minute, with at least
  four pauses of 0.3–2 seconds between turns). The two buffered minutes are written first,
  so the start of the conversation is kept, in chunks named `HHMMSS-…` for their start.
  Recording stops three minutes after the call or conversation ends. Buffered audio is never
  written unless recording starts, and pausing clears it. In Auto the microphone indicator
  stays on, because the microphone is listening.

Switch modes from the recorder's menu bar (Audio: On / Off / Auto), from the On / Off / Auto
control next to Pause on today's page, or set the default on the Settings page
(`audio_mode`). A mode picked in the menu bar is saved to the config within a minute, so it
survives a restart. While Auto records a call, today's page names the app using the
microphone ("Audio: Auto · recording (Zoom)"). The recorder logs `audio_mode`,
`audio_auto_start` (with the reason, `call` or `in_person`, and for a call the bundle id of
the app that triggered it) and `audio_auto_stop` events to `activity.jsonl`.

While audio is **Off**, the recorder reminds you with a notification to switch to Auto:

- when a call looks like it started (a meeting app or window is in front, or another app is
  using the microphone). The reminder clears when the call ends, and is not shown again
  within 10 minutes;
- when you come back after the Mac was idle (`idle_minutes`) or asleep. That reminder clears
  after 2 minutes.

Either reminder also clears as soon as audio leaves Off, and its **Switch to Auto** button
switches it. No reminder is shown while paused, while an excluded app or window is in front, or
while the disk guard has stopped audio. Allow notifications for WorkJournalRecorder
when macOS asks (System Settings → Notifications).

## Meetings

A meeting is time in a meeting app or window (`meeting_apps`, `meeting_title_keywords`), a
call where your mic and the system audio both carry speech (`meeting_min_two_way_minutes`),
or an **in-person** meeting: a conversation only your mic hears, where speech fills at least
`in_person_speech_ratio` (0.4) of a rolling 5-minute window for at least
`in_person_min_minutes` (5; pauses under 2 minutes do not split it). A locked or idle screen
does not stop detection. With speaker identification on, an in-person meeting also needs
at least 15 seconds of mic speech from someone other than the main voice (a short reply
counts, even one too brief to get its own speaker label); one voice (dictation, or your side
of a call on another device) is not a meeting and stays "me". A TV or a speakerphone on another device can still
look like an in-person meeting. In-person meetings show as "In person". An app or call meeting
wins over the same time: a short call during a conversation splits it, and each remaining part
of at least `in_person_min_minutes` is its own in-person meeting. A meeting that is no longer
detected when its hour is processed again is removed, unless you confirmed a participant's name.

All active, idle and locked-screen time inside any meeting (app, call or in person) is
categorized **Meetings**, ahead of your own `[rules]`; paused, excluded and off time stays
uncounted.

Isolated "You", "Thank you", "Bye" and similar lines that whisper invents on silence are
dropped; the same words within 3 s of real speech on either track are kept.

## Importing phone recordings

When you are away from the Mac (a meetup, an in-person meeting), record on your phone, for
example with Voice Memos, and import the recording afterwards. It goes through the same
steps as recorded meeting audio (whisper, speaker identification, the privacy filter and
redaction, then the meeting summary) and shows up as an **In person** meeting marked
**imported**: on that day's dashboard page, in `list_meetings`, `get_meeting` and `search`
over [MCP](#ask-your-journal), and in that day's journal entry. It covers its start time to
the start plus the recording's length. A short note you add (a title, and context such as
who was there, what the event was or what you care about) is given to the summary as your
own note, never as something said in the meeting.

**From the phone.** Turn on the upload page in `~/WorkJournal/config.toml` and restart
`journal serve`:

```toml
upload_addr = "0.0.0.0:4748"
upload_token = "a-long-random-secret"   # at least 16 characters; `openssl rand -hex 16` makes one
```

`journal serve` logs the address to open, for example
`journal: phone upload page on http://192.168.1.23:4748`. The phone and the Mac must be on
the same network (the same Wi-Fi), and macOS may ask once whether `journal` may accept
incoming connections. Open the address on the phone, enter the token once (the phone keeps
it), then pick the recording, check the start time and add a title and context. The start
time is filled in from the file's last-modified time less its length where the browser
provides them, in the phone's time zone; correct it if it is off. Files up to 1 GB are
accepted: `.m4a` (Voice Memos), `.mp3`, `.wav`, `.caf`, `.aac` and `.aif`/`.aiff`; other formats
and video are refused. The page says whether the upload worked. Processing of the
recording starts within a minute or so of the upload; it then takes as long as
transcription, speaker identification and the summary need, which for a long recording is
many minutes, and waits behind other processing (summaries also wait while Claude is paused
for a [usage limit](#claude-usage-limits)).

**From the Mac.** For a file AirDropped to the Mac, or to test:

```sh
journal import ~/Downloads/"New Recording 3.m4a" --start 2026-10-21T18:30:00-04:00 \
  --title "AI Tinkerers meetup" --context "Monthly meetup; Maya demoed. I care about speaker clustering."
```

`--start` is RFC 3339, with the time zone the recording was made in. The command copies the
file in and processes it straight away.

Each recording is kept in its own folder, `imports/<meeting id>/`: the audio and a
`note.json` with your title, context and start time. Nothing there is ever mixed with the
recorder's files. If processing fails, the recording is kept and the meeting's entry on the
dashboard shows the error; a failure such as whisper or Claude not running is tried again
an hour later, while a file `afconvert` cannot read stays as it is (import it again in
another format). Imported audio is deleted after `retention_days`, like other raw audio,
once it has been processed; its transcript, summary and note are kept.

**Security model.** The upload page is a second listener, off unless `upload_addr` is set,
and it serves only the upload form: never the dashboard, recordings, transcripts, search or
MCP, which stay on the loopback-only dashboard. Every request to it needs `upload_token`;
without it every route answers 401. `journal serve` refuses to start when `upload_addr` is
set and `upload_token` is empty or shorter than 16 characters. The first visit asks for the
token and keeps it in an HttpOnly, SameSite=Strict cookie, so it is never in a URL. The page
is **plain HTTP, without TLS**: anyone on the same network who can watch its traffic could
read the token and the recordings you upload. Use it only on a network you trust (home or
office Wi-Fi, not a café's), and set `upload_addr = ""` to turn it off. Someone with the
token can add recordings to your journal but cannot read anything from it.

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
every hour of the meeting, and earlier hours are relabelled when later audio regroups the
voices), or by name once you have named them. Numbers can have gaps (a "Speaker 2" with no
"Speaker 1") because a voice keeps its number when another voice is named. A voice with less than
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
- `cache/` holds per-chunk transcripts, speaker turns and voice embeddings, per-screenshot
  text and the privacy filter's per-utterance decisions. It is owner-only and kept outside `derived/`, so it is never passed to Claude.
  Transcripts, screen text, time segments, summaries and journals are kept.
- Exclusions: while an excluded app (default: 1Password, Keychain Access) or a window
  title containing an excluded keyword (default: Private, Incognito) is in front, no
  screenshots are taken and audio is silenced. The microphone indicator stays on during
  exclusions, because audio is silenced rather than stopped.
- Ambient audio: the microphone hears whoever is near the laptop. Before an hour is
  written anywhere, the `privacy-filter` skill reads its speech and marks two kinds of
  conversation for removal: bystanders talking among themselves without you (outside a
  call or meeting), and your own personal non-work conversations with people who are not
  coworkers (a family call, say, both sides of it). Anything with coworkers, personal chat
  included, and system audio during a work call are kept; when unsure, speech is kept. Removed speech never
  reaches `derived/`, search, meeting detection, notes, the journal or the weekly rollup;
  the hour page shows only a neutral line such as "4 min of non-work audio removed
  (13:12–13:16)". Meetings are detected again without it, so a bystander conversation is
  never an in-person meeting. Spoken ID, account and card numbers and passwords are
  replaced by `[redacted]`. Settings → Privacy turns the filter (`privacy_filter`) and
  redaction (`redact_identifiers`, which needs the filter) off; `privacy_drop_confidence`
  (0.5–0.95, default 0.7) is how sure the filter must be to remove speech. Its decisions
  are kept per utterance in `cache/<day>/privacy.json`, so the in-progress hour's passes
  send only new speech (with up to two minutes of earlier speech as context). To decide,
  Claude does see that speech once; its input file is deleted right after the run. Reprocess clears
  the hour's decisions, so a wrong removal can be undone, and a [correction](#tell-the-journal)
  you write tells the filter what to remove (or keep) for the time it covers. If Claude cannot run, nothing
  about the hour is written and it is retried later; if its answer is invalid, the hour is
  marked needs review with its speech withheld ("Speech not shown: privacy check failed",
  no summary) until you reprocess it. Raw audio is not
  changed; it is deleted with the rest of `raw/<day>/` after `retention_days`.
- Voice profiles (`voices/`) never leave the machine and are never passed to Claude
  (see [Speaker identification](#speaker-identification)).
- Only text (transcripts, screen text, activity summaries) goes to Claude. Each `claude -p`
  run uses the repo root as its working directory, so it loads that repo's project
  settings (`.claude/`, `CLAUDE.md`) and installed skills. Skill inputs are written to,
  and read from, `derived/` only. The session has only the Skill and Read tools and no
  MCP servers (`--strict-mcp-config`). Only project settings load (`--setting-sources project`),
  so your user-level settings and hooks never see journal text, and sessions are not saved
  (`--no-session-persistence`). `derived/` is added with `--add-dir`, and raw media,
  `voices/` and `journal.db` are never passed to it. These are Claude Code's tool and
  directory settings, not an OS sandbox.
- Screen text sent to Claude is trimmed first: a line already on any of the previous 10
  screenshots is not sent again, and an hour (or meeting) sends at most `ocr_chars_per_hour`
  (default 30000) characters, picked evenly across it. If a skill input is still over
  `input_max_bytes` (default 70000), less screen text is sent; speech is never cut.
  Inputs are written one record per line so Claude can read large ones in parts.
- The dashboard binds to loopback only; `journal serve` refuses any other `listen_addr`.
  The optional phone upload page (`upload_addr`) is a separate listener that only accepts
  recordings, behind `upload_token` (see [Importing phone recordings](#importing-phone-recordings)).
- Imported recordings live in `imports/<id>/` (audio and `note.json`); their transcripts are
  in `derived/imports/` and their whisper and speaker results in `cache/imports/`. Their
  audio is deleted after `retention_days` once processed.

## More

- Manual recorder checks: [docs/manual-checklist.md](docs/manual-checklist.md)
- Design: [docs/superpowers/specs/2026-10-07-work-journal-design.md](docs/superpowers/specs/2026-10-07-work-journal-design.md)
- Settings: [config.example.toml](config.example.toml), or the dashboard's Settings page
