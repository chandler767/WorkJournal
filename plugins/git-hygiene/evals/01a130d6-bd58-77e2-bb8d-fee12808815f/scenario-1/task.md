# Reprocess the current hour without crashing, and show useful error messages

## Problem

When I click "Reprocess" on the current hour (the one that's still being recorded), the page comes back with an error — a raw 500 response. That's not useful; I just want a refreshed summary of what has been recorded so far.

More generally, when any reprocess fails, I get a bare error page with no explanation. I can't tell whether to wait and try again, whether the data is gone, or whether something went badly wrong.

## Expected Behavior

- Clicking "Reprocess" on the hour that is currently being recorded should produce a fresh "summary so far" — the same kind of in-progress summary the dashboard already shows automatically. It should not return an error.
- When a reprocess does fail, the day page should show a clear, context-specific banner (the same kind of notice the page already shows after "Process now") instead of an error page. Three cases to cover:
  - The hour's audio is still actively being written and can't be fully processed yet.
  - The hour's raw recordings have already been removed by the retention policy.
  - Some other processing error (e.g., the transcription tool failed).

## Acceptance Criteria

- Reprocessing the currently-recording hour succeeds and produces a partial summary.
- A reprocess failure redirects to the day page with a visible, human-readable notice.
- Errors are logged before the redirect so they remain visible in the server log.
- The notice text is specific to the failure type (still recording vs. media expired vs. general failure).

## When you're done

Stage your changes with `git add`. Then write the proposed commit message for the staged changes to `COMMIT_MSG.txt` at the repository root (do not stage that file). Do not run `git commit`.
