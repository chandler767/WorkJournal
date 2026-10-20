# Detect In-Person Meetings from Microphone Activity

## Problem/Feature Description

Work Journal does a great job picking up virtual meetings — anything in Zoom, Teams, or detected via two-way audio. But when I'm at the office having a face-to-face conversation, nothing gets logged. My mic is picking up speech the whole time, but since there's no system audio going out (no call happening), the meeting detection doesn't fire.

It would be really useful if the system could recognize these in-person conversations as meetings too. I often forget to log them manually, and they can easily take up a big chunk of my day.

## Expected Behavior

When the user is speaking in person — their microphone detects sustained speech but there's no corresponding audio output from the computer — the system should recognize this as an in-person meeting and include it in the journal alongside virtual meetings. The detected meeting should appear as a distinct meeting type so it's clear it wasn't a video call.

These in-person meetings should be skipped or trimmed if they overlap with a virtual meeting already detected (the call takes priority). It's also fine if the computer is locked or idle during the conversation — the user is physically present and talking, not at their desk.

## Acceptance Criteria

- In-person conversations with sustained mic speech appear in meeting detection output, separate from video-call meetings
- Brief or sparse mic activity (background noise, short exchanges) does not produce false positives
- In-person meetings do not conflict with or duplicate virtual meetings detected during the same period
- Users can tune the detection sensitivity (minimum duration and required speech density) in the config file

## When you're done

Stage your changes with `git add`. Then write the proposed commit message for the staged changes to `COMMIT_MSG.txt` at the repository root (do not stage that file). Do not run `git commit`.
