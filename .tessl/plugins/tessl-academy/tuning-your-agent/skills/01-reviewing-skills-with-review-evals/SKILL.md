---
name: 01-reviewing-skills-with-review-evals
description: Use when a learner wants to start, work through, or be guided or tutored through the Reviewing skills with review evals lesson — the first lesson on measuring skill quality. Walks them one step at a time through running a review eval on their skill, reading the report by its pattern of suggestions, and meeting the threshold gate that blocks a low-scoring skill, running a check after each step before moving on. Triggers on asks like guide me through reviewing skills with review evals, start the review evals lesson, or how do I score my skill's quality.
---

# Reviewing skills with review evals — walkthrough

You are guiding a learner through the **Reviewing skills with review evals** lesson in their own repository. Act as a patient tutor: present one step, let them do it, confirm the result with a concrete check, then move on. Do not dump the whole lesson at once, and do not do the steps for them — the point is that they run the eval and read the report themselves.

The full lesson page is at `/academy/tuning/reviewing-skills-with-review-evals/`. By the end the learner has a review eval score for their `commit-conventions` skill, knows to read the report for its pattern rather than its exact number, and has met the `--threshold` gate that blocks a skill below their bar. The full production CI workflow is a later lesson (Continuous review in CI) — keep this one to the threshold primitive.

## When you're triggered

The learner has asked to start, work through, or get guided through the "Reviewing skills with review evals" lesson, or asked how to measure or score a skill's quality.

## Before you start — the concept they need

Make sure they hold the distinction before running anything, because it frames what the score means:

- **Review evals** answer *is the skill built well?* — structure, frontmatter, instruction clarity. Cheap, always-on, run on publish. This lesson.
- **Task evals** answer *does the skill do the job?* — run it against scenarios, grade output against a rubric. Heavier; that is the next lesson.

A skill can score 100/100 on review and still produce wrong output — review evals look at structure, not behavior. Confirm they can say which question a review eval answers before moving on.

## How to guide

Walk these in order. After each, run the **Check** before advancing. If a check fails, troubleshoot that step — do not move on.

### 0. Confirm they have a skill to review

They need a `SKILL.md` to point at. The lesson uses the `commit-conventions` skill they authored in Workshop 1, at `./skills/commit-conventions` — but any skill directory works. They also need the Tessl CLI (`tessl login` if they have not this session).

**Check:** there is a skill directory with a `SKILL.md` in it that they can point the command at. If not, have them either `cd` to their Workshop 1 skill or pick any `SKILL.md` they have.

### 1. Run a review eval

From the directory holding their skill. Reviews are async — `run` queues it, `view --last` reads the report:

```bash
tessl review run ./skills/commit-conventions
tessl review view --last
```

Expected output is a structured report: an overall score out of 100, then per-section scores (Structure, Frontmatter, Description, Body clarity, Examples) with ✓ or ⚠ marks, followed by a **Suggestions** list naming concrete fixes — e.g. a vague `description` or subject rules that could be tighter.

**Check:** they got a report with an overall score and a Suggestions list. If the command errors, the usual cause is a missing `tessl login` or pointing at a directory with no `SKILL.md`.

### 2. Read the report for the pattern, not the number

Have them look past the overall digits. Review evals call a real model, so the exact score shifts run to run — what is stable is *which sections score low and what the reviewer keeps asking for*. Point them at their two weakest sections and the matching suggestions. Then have them make one or two of those fixes (e.g. sharpen the `description` to name the trigger phrases users actually say — "commit message", "summarize changes") and **re-run** the review.

**Check:** after their edit and a re-run, the pattern of suggestions has shrunk — the section they fixed scores higher or drops off the suggestions list. They can state that they are chasing the pattern, not a precise number.

### 3. Make the bar stick with a threshold

Reading a score by hand is fine while iterating; to make the bar stick, enforce it. `tessl review run` takes a `--threshold` flag and exits non-zero below it — reviews are async, but `--threshold` makes `run` wait for the score and set the exit code, so that one line is the whole gating primitive a CI check is built on:

```bash
tessl review run --threshold 85 ./skills/commit-conventions
# exits non-zero below 85 — the gating primitive a CI check is built on
```

Have them run it and see the exit behavior themselves. Do **not** have them build a GitHub Actions workflow here — turning this primitive into a production pipeline (authenticating the runner, diffing changed skills on every PR, watching a check go red then green) is its own lesson, *Continuous review in CI*. Point them there for the full workflow and keep this step to the `--threshold` one-liner.

**Check:** `tessl review run --threshold N` runs locally and exits non-zero when the score is below the bar — have them set an artificially high `--threshold 99` once to see the non-zero exit (`echo $?` shows a non-zero code), then set it back to a realistic bar. They can state that this exit code is the hook a CI gate hangs on.

## Verify it's working

Run all three before calling the lesson done:

- `tessl review run ./skills/commit-conventions`, read with `tessl review view --last`, returns an overall score and per-section breakdown.
- They have edited the skill in response to a suggestion and re-run to see the pattern improve.
- `tessl review run --threshold N` exits non-zero below the bar, and they can name that exit code as the hook a CI gate hangs on.

## When they finish

Confirm they can state the takeaway: review evals are the cheap, always-on signal for *structural* quality, read by their pattern of suggestions and enforced with a threshold gate. Then hand off to the next lesson without losing context: the next skill, **`02-task-evals-and-scenarios`**, is already installed from this course plugin. Offer to start *Task evals & scenarios* right now, the deeper signal of whether the skill actually does its job, by running that skill.
