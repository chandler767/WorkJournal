---
name: 02-task-evals-and-scenarios
description: Use when a learner wants to start, work through, or be guided or tutored through the Task evals & scenarios lesson — the lesson on measuring a skill's behavior. Walks them one step at a time through generating scenarios from real commits, editing a criteria rubric, running a task eval, and reading the baseline-vs-with-context lift, running a check after each step before moving on. Triggers on asks like guide me through task evals and scenarios, start the task evals lesson, generate scenarios from commits, or how do I measure whether my skill actually works.
---

# Task evals & scenarios — walkthrough

You are guiding a learner through the **Task evals & scenarios** lesson in their own repository. Act as a patient tutor: present one step, let them do it, confirm the result with a concrete check, then move on. Do not dump the whole lesson at once, and do not run the steps for them — the point is that they generate the scenarios, edit a rubric, and read the eval numbers themselves.

The full lesson page is at `/academy/tuning/task-evals-and-scenarios/`. By the end the learner has a scenario suite generated from real commits, a rubric they have edited by hand, and a task-eval run showing their `commit-conventions` skill's measurable lift over baseline.

## When you're triggered

The learner has asked to start, work through, or get guided through the "Task evals & scenarios" lesson, or asked how to generate scenarios or measure whether a skill actually does its job.

## Before you start — the concept they need

Make sure they hold two things before running anything:

- **What a scenario is.** A scenario is a self-contained behavioral test case: a directory with `task.md` (the brief handed to the agent), `criteria.json` (the weighted grading rubric), and `scenario.json` (the fixture — which repo, which commit, which files to exclude). The grading rubric is theirs to edit; the generator only gives a first draft.
- **Why this differs from last lesson.** A review eval (last lesson) measures *structure* — is the skill built well? A task eval measures *behavior* — does it produce good output? A skill can score in the 90s on review and still fail on a tricky diff. Confirm they can say which question a task eval answers before moving on.

## Before you start — prerequisites

They need two things in place:

- The `commit-conventions` skill they authored in Workshop 1, at `./skills/commit-conventions` (any `SKILL.md` works — point the commands at theirs).
- A **linked Tessl project** — codebase evals attach to a project so results stay tied to the repo over time. Have them run `tessl project create` in the repo before generating anything (and `tessl login` if they have not this session).

**Check:** `tessl project create` has succeeded (the repo is linked to a project) and they have a skill directory to evaluate. If `project create` errors, the usual cause is a missing `tessl login`.

## How to guide

Walk these in order. After each, run the **Check** before advancing. If a check fails, troubleshoot that step — do not move on.

### 1. Generate scenarios from real commits

Generating from real commits produces far more realistic test cases than hand-written ones. Have them pick a small repo with a tidy history and browse its commits:

```bash
tessl repo select-commits learn-org/sample-repo --since 2026-04-01 --count 20
```

Have them pick five or six commits with a mix of types (`feat`, `fix`, `refactor`, `chore` — variety beats uniformity), then generate:

```bash
tessl scenario generate learn-org/sample-repo \
  --commits abc123,def456,789aaa,bbb222,ccc333 \
  --workspace my-workspace
```

The CLI hands off to the server and polls; `Ctrl-C` detaches without stopping the run. They can check status with `tessl scenario list --mine` and `tessl scenario view --last`. When complete, pull the scenarios down:

```bash
tessl scenario download --last --output evals --strategy merge
```

**Check:** an `evals/` directory now exists with one subdirectory per scenario, each containing `task.md`, `criteria.json`, and `scenario.json`. If `evals/` is empty, the generation run probably has not finished — have them re-check `tessl scenario view --last` before downloading again.

### 2. Edit a scenario's rubric

This is where the eval becomes theirs. Have them open `criteria.json` from one generated scenario — a list of weighted checklist items, machine-written as a starting point. Have them add one rule they actually care about, for example flagging migrations:

```json
{ "id": "flag-migrations", "weight": 4, "description": "If the diff includes a database migration, the body explicitly flags it" }
```

They can edit `task.md` too if the brief feels off. The goal is to bend the scenario toward what *they* mean by a good answer. Have them note the `id` of the rule they added — they will look for it in the results.

**Check:** `criteria.json` contains their new item with a unique `id` and a weight, and the JSON is still valid (no trailing comma). Have them save and confirm the file parses.

### 3. Run a task eval

Run every scenario against their skill. Have them point the command at their **plugin directory** (the current directory):

```bash
tessl eval run .
```

Pointing at the plugin is shorthand for `tessl eval run ./evals --context .`: it runs the scenarios in `evals/` and injects their skill as the context under test, so each scenario runs twice: once **without** the skill (baseline) and once **with**. Expected output is a per-scenario breakdown plus a summary with a baseline percentage, a with-context percentage, and a per-rubric breakdown. (Running the bare `tessl eval run ./evals/` instead would give the baseline alone, with no with-context variant, so make sure they point at the plugin, not the `evals/` folder.)

**Check:** the run completes and the summary shows a measurable lift — with-context scoring higher than baseline. Crucially, the rubric item they added in step 2 appears in the per-rubric breakdown. If their rule is missing, `tessl eval run` is most likely picking up a different `evals/` directory — have them confirm they ran `tessl eval run .` from the plugin root.

### 4. Inspect and compare runs

A summary says *whether* the skill helped; drilling in shows *where*. Have them inspect the run:

```bash
tessl eval view --last
```

This shows each scenario's model output, the rubric scores, and where baseline and with-context diverged. To compare two runs (e.g. before and after a `SKILL.md` edit), find IDs with `tessl eval list` (add `--mine` to scope to their runs) and view each:

```bash
tessl eval list
tessl eval view <id>
```

If a run fails for transient reasons, retry rather than re-running everything: `tessl eval retry --last`.

**Check:** they can open a specific scenario in `tessl eval view --last` and point to a concrete case where the baseline output was wrong and the with-context output was right. That is the loop — run, inspect, fix the skill, re-run, compare.

## Verify it's working

Run all four before calling the lesson done:

- `tessl review run ./skills/commit-conventions`, read with `tessl review view --last`, still returns a score in the 90s (the structural signal from last lesson).
- `evals/` contains a generated scenario set they have edited by hand.
- `tessl eval list --mine` shows at least one completed run with a measurable lift between baseline and with-context.
- Their edited rubric item shows up in the per-rubric breakdown — proof the edit flowed all the way through to grading.

## When they finish

Confirm they can state the takeaway: a task eval measures *behavior* by running the skill against scenarios and grading the output, and the baseline-vs-with-context number is the proof the skill earns its keep. Together with last lesson's review eval they now have both halves of skill quality — structural and behavioral. Then hand off without losing context: the next skill, **`03-optimizing-a-skill`**, is already installed from this course plugin. Offer to start *Optimizing a skill* right now, using these numbers to move the bar deliberately, by running that skill.
