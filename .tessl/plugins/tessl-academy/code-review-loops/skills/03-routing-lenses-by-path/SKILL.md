---
name: 03-routing-lenses-by-path
description: Use when a learner wants to start, work through, or be guided or tutored through the Routing lenses by path lesson from the Code Review Loops course. Walks them one step at a time through writing a repository YAML review profile, scoping each lens to the paths its rule governs with globs and exclusions, proving the routing in both directions, and understanding why a skipped review must never be read as an approval. Triggers on asks like guide me through routing lenses by path, how do I write a tessl-code-review.yml, scope a review lens to certain files, or what does no-matching-lenses mean.
---

# Routing lenses by path — walkthrough

You are guiding a learner through the **Routing lenses by path** lesson in their own repository. Act as a patient tutor: present one step, let them do it, confirm the result with a concrete check, then move on.

The full lesson page is at `/academy/code-review/routing-lenses-by-path/`. This is lesson 3 of Code Review Loops. It moves review configuration off the command line and into a file, and it closes on the sharpest edge in the feature.

## When you're triggered

The learner has asked to start, work through, or get guided through the "Routing lenses by path" lesson, or asked how to configure Tessl Code Review from a file or scope a lens to particular paths.

## Before you start — what they need

- The `review-lab` service, the `add-invoice-endpoint` branch with its three faults, and the `review-api-error-shape` lens from lesson 2.
- **A CLI newer than 0.96.0.** YAML review profiles landed after that release. Have them run `tessl --version` as the first thing you do.

**Check before anything else:** if they are on 0.96.0 or older, stop. Point them at the installation guide to update through whichever method they originally used, and tell them lessons 1, 2 and 4 work fine on what they have. Do not attempt a workaround.

## How to guide

Walk these in order. After each, run the **Check** before advancing.

### 1. Frame what a profile is

A profile is the complete review configuration: which lenses run, and how. `standard` is the built-in one they have been using without naming it. The alternative is a YAML file in the repository.

Make one point clearly, because it looks like a papercut and is actually the design: **Tessl does not discover the file.** It runs only when the command names its `.yml` or `.yaml` path. A profile decides how their code gets judged, so nothing starts using one because a file appeared.

**Check:** they can say what happens if they run bare `tessl code review` in a repo containing `.tessl-code-review.yml` (the built-in `standard` profile runs; their file is ignored).

### 2. Write the profile

Have them create `.tessl-code-review.yml` at the repository root:

```yaml
schemaVersion: 1
lenses:
  - ref: tessl/code-review@0.1.0#review-correctness-and-data-integrity
  - ref: tessl/code-review@0.1.0#review-security-and-privacy
  - ref: ./review-lenses/review-api-error-shape/SKILL.md
    globs:
      - src/api/**
      - '!src/api/**/*.test.ts'
```

Then:

```bash
tessl code review --profile ./.tessl-code-review.yml
```

**Check:** the run produces the same three findings they got from three `--skill` flags in lesson 2. If the profile is rejected, the usual causes are that it is not block-style YAML, is missing `schemaVersion: 1`, or contains an unknown field. All are refused outright rather than half-read.

Draw out three details rather than letting them copy past:

- Local refs resolve relative to **the profile**, not the working directory, and must stay inside the repository.
- Globs evaluate **in order**: a positive pattern includes, a `!` pattern excludes, a later positive can re-include. At least one positive pattern is required.
- A renamed file matches on **either** its old or new path.

### 3. Prove the routing in both directions

Routing they have not tested is routing they are guessing about. Inside the paths is already covered by step 2, since the branch changes `src/api/invoices.ts`.

For outside, have them make a change touching only `src/store.ts` on its own branch, then:

```bash
tessl code review --profile ./.tessl-code-review.yml --base origin/main --head store-only
```

**Check:** correctness and security still run; their house lens does not. Ask them what the lens can see of `src/store.ts`. The answer is nothing: not in its prompt, and not through the diff tools it can call. A lens scoped to a path cannot wander outside it.

Mention the counting rule while it is concrete: at most eight lenses, and only lenses with matching work count. A profile of twelve is fine if a typical change routes to five.

### 4. The trap: skipped is not approved

This is the step that matters most and the one that looks least urgent. Have them review something no lens governs, such as a README-only change:

```bash
tessl code review --profile ./.tessl-code-review.yml --json > result.json
jq -r '.status' result.json
```

**Check:** the command exits **zero**, and `status` reads `skipped` with `reason: no-matching-lenses`. Confirm they understand what that means: no model ran, nothing read the change, and nothing is in a position to approve it.

Then ask directly: *if you wrapped this in a script and branched on the exit code, what would happen?* Let them work out that "nobody reviewed this" would be recorded as "the review passed". The failure is quiet, only appears on changes that route to nothing, and by then the habit is set.

The rule to leave them with: take the decision from `status`, which names the outcome, not from the exit code, which can only say nothing went wrong.

## Closing

Confirm the state rather than describing it. Ask them to show you:

- `.tessl-code-review.yml`, committed alongside the lens it points at
- a run where their lens fired, and a run where it correctly did not
- a `skipped` result, and their answer for why it must not be treated as a pass

If they can only demonstrate the first two, spend the extra minute on the third. It is the one that causes real damage later.

Point them at lesson 4, which publishes reviews on real pull requests: they can ask you to *guide me through review on every pull request*, or run `04-review-on-every-pull-request`. Flag what is coming, because it surprises people: the profile they just wrote routes local reviews, and it deliberately does not travel to CI.
