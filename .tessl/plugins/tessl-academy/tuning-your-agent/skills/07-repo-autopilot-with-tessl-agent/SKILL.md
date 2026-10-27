---
name: 07-repo-autopilot-with-tessl-agent
description: Use when a learner wants to start, work through, or be guided or tutored through the Repo autopilot with Tessl Agent lesson — the lesson on connecting a repo to Tessl Agent and reviewing the context fixes it proposes. Walks them one step at a time through installing the GitHub App, surfacing a recurring pattern from PR and CI history, reviewing the improvement PR, and verifying it with an eval, running a check after each step before moving on. Triggers on asks like guide me through repo autopilot with tessl agent, start the tessl agent lesson, connect my repo to tessl agent, or how does tessl agent find and fix recurring issues.
---

# Repo autopilot with Tessl Agent — walkthrough

You are guiding a learner through the **Repo autopilot with Tessl Agent** lesson. Act as a patient tutor: present one step, let them do it, confirm the result with a concrete check, then move on. Do not do the steps for them — the point is that they connect the repo, surface a real pattern, and review the fix themselves.

The full lesson page is at `/academy/tuning/repo-autopilot-with-tessl-agent/`. By the end the learner has a repo connected to Tessl Agent, a recurring pattern surfaced from their real PR and CI history, and an improvement PR against their context that they have reviewed and verified.

## When you're triggered

The learner has asked to start, work through, or get guided through the "Repo autopilot with Tessl Agent" lesson, or asked how Tessl Agent finds and fixes recurring issues.

## Before you start — the concept they need

Make sure they hold this before doing anything:

- **Tessl Agent fixes context, not code.** Connected to a repo, it reads PR review comments, CI results, and session logs for the corrections that keep recurring, then opens PRs that change your **rules, skills, and verifiers** so the pattern stops. It does not write application code, and it does not merge on its own — they review every PR. Confirm they can say what kind of thing the agent changes before moving on.

## How to guide

Walk these in order. After each, run the **Check** before advancing. If a check fails, troubleshoot that step — do not move on.

### 1. Connect the repo

Connecting is a one-time integration in the registry, not a CLI command. Have them:

1. Go to `https://tessl.io/registry` and log in.
2. Select their **workspace** from the left menu.
3. Click **Integrations**, then **Enable Integration**.
4. Select **Configure** next to the organization to add the app to.
5. Choose **specific repositories** (recommended for a first run) or all of them.
6. Click **Install & Authorize**.

**Check:** the integrations screen shows **Connected** for their organization. If it doesn't, the usual cause is missing GitHub permissions on the org — have them re-run Configure and confirm they can grant the app access.

### 2. Surface a recurring pattern

Have them point the agent at recent history and ask for the pattern — this runs the `find-optimizations` skill over PR review history and CI results:

```
Look over PRs from the last 2 weeks and tell me the pattern.
```

The agent returns findings grouped by fix type (**rule**, **skill**, **verifier**, **refactor**) with evidence linking to specific PRs. Have them read the findings and pick one.

**Check:** at least one pattern came back with real PR references as evidence. If nothing surfaced, the repo may lack enough recent PR/CI history — have them widen the window (e.g. the last month) or point at a busier repo.

### 3. Review the improvement PR

Have them turn a finding into work and let the agent draft the fix as a change to their context:

```
Create a ticket from item 2.
Implement the fix from item 1.
```

The fix arrives as an improvement PR. Have them review it like a teammate's: read the diff, confirm it targets a rule/skill/verifier (not application code), and that it addresses the pattern.

**Check:** an improvement PR exists and changes their context, not application code. If it patches app code, it hasn't fixed the recurrence — have them send it back toward a rule, skill, or verifier.

### 4. Verify the loop

Have them prove the fix works, using scenarios built from the PRs that exposed the problem:

```
Generate eval scenarios from PRs #1189, #1201, and #1218, then run the eval with and without my new context.
```

**Check:** the eval passes **with** the new context where it failed **without** it. That green-with-context result on the exact PRs that showed the problem is the signal the fix holds. If there's no lift, the fix isn't targeting the real cause — walk back to the finding.

## Wrapping up

Confirm they can state the loop in one line: *the agent read our history, found a recurring correction, and opened a context fix we reviewed and verified.* Point them back to the lesson page for reference.
