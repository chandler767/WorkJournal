---
name: 05-continuous-review-in-ci
description: Use when a learner wants to start, work through, or be guided or tutored through the Continuous review in CI lesson — the closing lesson of the testing and optimization course. Walks them one step at a time through standing up a production GitHub Actions review gate, watching it block a pull request that carries a weak skill, and then running the optimizer until the gate clears, checking the result after each step before moving on. Triggers on asks like guide me through continuous review in CI, start the CI review lesson, how do I gate skill reviews in GitHub Actions, or make my review eval run on every pull request.
---

# Continuous review in CI — walkthrough

You are guiding a learner through the **Continuous review in CI** lesson in their own repository. Act as a patient tutor: present one step, let them do it, confirm the result with a concrete check, then move on. Do not run the steps for them — the whole point is they watch the gate fail a real pull request and clear it themselves.

The full lesson page is at `/academy/tuning/continuous-review-in-ci/`. This is the capstone of the review → task evals → optimize sequence: it turns the gate and optimizer they already learned into something continuous that runs on every change. By the end the learner has a GitHub Actions workflow reviewing every PR, and has watched one change travel the full fail → optimize → pass loop.

## When you're triggered

The learner has asked to start, work through, or get guided through the "Continuous review in CI" lesson, or asked how to gate skill reviews in CI / make a review eval run on every pull request.

## Before you start — what they need

- The Tessl CLI installed and authenticated (`tessl login` if they have not this session).
- A repo with a deliberately weak `commit-conventions` skill at `./skills/commit-conventions` — the fixture the lesson is built on. If they do not have one, have them create a `skills/commit-conventions/SKILL.md` with a vague one-line description, no examples, and a single-line body, so the gate has something real to catch.
- A GitHub repository they can open a pull request against, and rights to add a repository secret.

Confirm they can say *why* the lesson starts from a failing skill: a gate that only ever passes teaches nothing — they need a PR that goes red so they can watch it go green.

## How to guide

Walk these in order. After each, run the **Check** before advancing. If a check fails, troubleshoot that step — do not move on.

### 1. Confirm the skill fails the bar

Have them score the fixture skill so they see the starting point. Review runs are async — `run` queues it, `view --last` reads the report:

```bash
tessl review run ./skills/commit-conventions
tessl review view --last
```

The report comes back with a low overall score, with suggestions clustered on the thin description and missing examples.

**Check:** they report an overall score below their intended threshold (the lesson uses 85) and can name the weak spots — description and examples. If the skill already scores high, it is not weak enough to demonstrate the gate; have them thin the description and remove examples until it fails.

### 2. Add the TESSL_TOKEN secret

A review eval calls a real model and spends credits, so CI must authenticate. Have them mint an API key at <https://tessl.io/account/api-keys> and add it to the repo under **Settings → Secrets and variables → Actions** as a new repository secret named `TESSL_TOKEN`.

**Check:** the secret `TESSL_TOKEN` shows in the repo's Actions secrets list. Do not have them paste the key into chat or commit it anywhere — it lives only in the secret.

### 3. Add the gate workflow

Have them create `.github/workflows/skills-gate.yml`:

```yaml
name: Skills eval gate
on:
  pull_request:
    paths:
      - 'skills/**'
      - 'plugins/**'
jobs:
  eval-gate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - name: Set up Tessl
        uses: tesslio/setup-tessl@v2
        with:
          token: ${{ secrets.TESSL_TOKEN }}
      - name: Review changed skills
        run: |
          git diff --name-only origin/${{ github.base_ref }}...HEAD \
            | grep -E '^(skills|plugins/.*/skills)/' \
            | xargs -I{} dirname {} | sort -u \
            | xargs -I{} tessl review run --threshold 85 {}
```

Explain the moving parts as they go: `paths:` scopes the gate to skill changes; `fetch-depth: 0` gives the runner history to diff against the base branch; `tesslio/setup-tessl@v2` installs and authenticates the CLI from the secret; `--threshold 85` exits non-zero below the bar, which fails the job.

**Check:** the workflow file is committed and they can explain, in their own words, why the threshold flag is what blocks the PR (it exits non-zero, failing the step).

### 4. Open a PR and watch it fail

Have them open a pull request that touches `skills/commit-conventions/SKILL.md`. The gate runs against the weak skill, scores below 85, and the step exits non-zero.

**Check:** the **Skills eval gate** check shows **red** on the PR and the PR is blocked. Have them open the failing job log and read the review output that explains the low score — the red X is the system working, not a setup error.

### 5. Optimize until the gate clears

Now fix the skill the same way they did in the optimize lesson. `tessl review fix` runs the review eval in a loop, applying improvements until it converges or hits the iteration cap:

```bash
tessl review fix --max-iterations 5 ./skills/commit-conventions
```

It shows each round of changes before applying them — have them read the proposed changes rather than blindly accepting. They can accept, reject, or pass `--yes` to auto-apply. When it finishes, confirm the new score clears the bar:

```bash
tessl review run ./skills/commit-conventions
tessl review view --last
```

**Check:** the plain re-run reports an overall score above 85, and they can point to at least one concrete change the optimizer made to the `SKILL.md`.

### 6. Push the fix and watch it pass

Have them commit the optimized skill and push to the **same** pull request. The gate re-runs against the improved skill, scores above the threshold, and the step exits zero.

**Check:** the **Skills eval gate** check flips to **green** and the PR is mergeable. Confirm they can narrate the full loop they just watched: failed the gate → optimized → cleared the gate.

## When they finish

Confirm they can state the takeaway: continuous review means the bar is enforced on every change automatically, not just when someone remembers to run it. Reinforce two follow-on moves from the lesson — raise the threshold by 5 once the gate is reliably green to ratchet quality up over time, and pair this structural gate with the task evals from Lesson 2 to cover behavior as well as structure. That completes Tuning Your Agent. Congratulate them: across the two courses they have installed, authored, bundled, published, measured, optimized, security-reviewed, and CI-gated a skill. There is no further course to install yet, so invite them to apply the whole loop to a skill of their own.
