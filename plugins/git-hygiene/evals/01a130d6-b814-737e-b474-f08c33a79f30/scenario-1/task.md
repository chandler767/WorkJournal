# Add Privacy Filter Skill to work-journal Plugin

## Problem/Feature Description

The work-journal app records audio throughout the workday, including ambient sound around the laptop. This means the journal captures conversations that shouldn't be in a work log: two colleagues chatting next to the user about their weekend, or the user taking a personal call with a family member.

We need a way to automatically identify and remove this private speech before it gets stored or summarized. The app already has a skill-based architecture for processing hours of audio — we need a new skill that receives an hour's worth of transcript utterances and identifies which parts should be dropped or have sensitive details redacted.

## Expected Behavior

When the work-journal app processes an hour of recorded audio, it should run the privacy filter skill against the transcript before further processing. The skill reviews all utterances in the hour and:

- Flags ranges of utterances as `bystander` (conversations among people near the laptop that don't include the user) or `personal_non_work` (the user's own conversations with family or friends about personal, non-work matters)
- Returns each flagged range with a confidence score and the drop reason
- Flags individual utterances that contain sensitive identifiers (bank account numbers, passwords, government IDs, etc.) with those identifiers replaced by `[redacted]`

Speech that should stay in the journal — coworker conversations, meeting audio, work discussions — must never be dropped. When it's unclear whether something should be dropped, it stays.

## Acceptance Criteria

- The skill can be installed as part of the `local/work-journal` plugin and invoked headlessly by the app
- Given an hour's utterances, meetings, and known coworker list, the skill returns `drop` ranges and `redact` entries as a single JSON object
- Bystander and personal non-work conversations are correctly identified and flagged with appropriate confidence scores
- Utterances containing sensitive identifiers are returned with the identifiers replaced by `[redacted]`, while other utterances are omitted from the redact list

## When you're done

Stage your changes with `git add`. Then write the proposed commit message for the staged changes to `COMMIT_MSG.txt` at the repository root (do not stage that file). Do not run `git commit`.
