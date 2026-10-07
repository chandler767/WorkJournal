# Recorder manual checklist

Run these by hand after building the recorder (`./recorder/build.sh` from the module root).
The app is ad-hoc signed, and macOS ties its permissions to the exact build, so after every
rebuild you may need to grant them again (remove the old entry and re-add it if it is stale).

Launch through LaunchServices (`open`) so permissions attach to `WorkJournalRecorder.app`
and not to your terminal.

## 1. Permissions granted

- [ ] Run the selftest:
  `T=$(mktemp -d); open -W -n recorder/build/WorkJournalRecorder.app --args selftest --data-dir "$T"; cat "$T/selftest.result"`
- [ ] Allow the prompts, then re-run until the result reads `selftest ok`. In System Settings → Privacy & Security, WorkJournalRecorder should be listed and enabled under:
  - [ ] Screen & System Audio Recording
  - [ ] Microphone
  - [ ] Accessibility (needed for window titles; without it, `title` is always empty)

## 2. Start the recorder

```sh
open -n recorder/build/WorkJournalRecorder.app --args run \
  --data-dir ~/WorkJournal --socket ~/WorkJournal/recorder.sock \
  --interval 10 --meeting-interval 5 --chunk-minutes 5 --width 1440 \
  --idle-minutes 5 --pause-auto-resume 1 --dashboard-url http://127.0.0.1:4747 \
  --exclude-app 1Password --exclude-app "Keychain Access" \
  --exclude-title Private --exclude-title Incognito \
  --meeting-app zoom.us --meeting-app "Microsoft Teams" --meeting-app FaceTime \
  --meeting-title Huddle --meeting-title "Google Meet" --meeting-title "Meet -"
```

`--pause-auto-resume 1` keeps the auto-resume check short; the default is 60. Below, `D` means
`~/WorkJournal/raw/$(date +%F)`.

- [ ] The menu bar shows `record.circle`.

## 3. Both audio tracks audible in a test call

- [ ] Join a test call (for example a FaceTime or Zoom test meeting) and talk for a couple of minutes while the other side or the test audio plays.
- [ ] Quit the recorder (menu bar → Quit) so the chunks finish.
- [ ] `ls D/audio` shows `HHMM-system.m4a` and `HHMM-mic.m4a` for the call.
- [ ] `afplay D/audio/<HHMM>-system.m4a`: you hear the other side of the call.
- [ ] `afplay D/audio/<HHMM>-mic.m4a`: you hear your own voice.
- [ ] `D/screens/` gained JPEGs about every 5 s during the call (meeting cadence) and every 10 s otherwise, with no new file while the screen did not change.

## 4. 1Password is excluded

- [ ] Restart the recorder. Note the time, switch to 1Password for about 30 s, then switch back.
- [ ] `tail D/activity.jsonl` shows `{"ts":…,"type":"excluded"}` (no app, bundle or title), then `included`, then an `app` event for the app you returned to. No `app` event names 1Password.
- [ ] No `D/screens/HHMMSS.jpg` has a timestamp inside the 1Password span.
- [ ] The audio chunk that was open ended when 1Password came up (`afinfo` shows it ending there), and the next `-system`/`-mic` chunks are named for the minute you left 1Password. Nothing was recorded in between.
- [ ] While in 1Password, `echo status | nc -U ~/WorkJournal/recorder.sock` reports `"state":"excluded"`, `"screens":false`, `"audio":false`.

## 5. Pause and resume from the menu bar

- [ ] Menu bar → Pause: the icon becomes `pause.circle`, the item reads Resume, and `activity.jsonl` gains `pause`. No screenshots or audio are written while paused, and the orange mic indicator goes away.
- [ ] Menu bar → Resume: the icon returns to `record.circle`, `activity.jsonl` gains `resume` and an `app` event, and new audio chunks are named for the resume minute.
- [ ] Menu bar → Open Dashboard opens the dashboard URL in the browser.

## 6. Auto-resume after the configured minutes

- [ ] Pause from the menu bar and wait for the `--pause-auto-resume` minutes (1 in the command above).
- [ ] The recorder resumes on its own: the icon is `record.circle` and `activity.jsonl` has `resume` about 1 minute after `pause`.

## 7. Control socket returns JSON

- [ ] `echo status | nc -U ~/WorkJournal/recorder.sock` prints one line such as
  `{"audio":true,"meeting":false,"screens":true,"since":"2026-10-07T14:05:00+01:00","state":"recording"}`.
- [ ] `echo pause | nc -U …` prints `ok` and status shows `"state":"paused"`; `echo resume | nc -U …` prints `ok`.
- [ ] `echo screens_off | nc -U …`: `"screens":false`, `"audio":true`. `audio_off`: both false. `normal`: both true. Each one logs its event.
- [ ] `echo quit | nc -U …` prints `ok`, the recorder exits, `activity.jsonl` ends with `stop`, the last `.m4a` files play, and the socket file is gone.

## 8. Degraded start

- [ ] Start with `--degraded` added: the icon is `exclamationmark.circle` and the menu shows a disabled "Recorder restarted after errors" item.
