---
name: prepare-repo
description: >-
  Prepare an existing repository so Tessl agents and Tessl Code Review work
  well in it: better agent context, verifiers, Code Review lenses, and lint
  rules drawn from how the team already works. Use when someone wants agents
  to make fewer mistakes in their repo, wants to prepare or improve their repo
  for agents, or wants to turn recurring review feedback into checks.
---

# Prepare a repository

This skill looks at how a repository is built, reviewed, and fixed, and
proposes changes that help agents get work right the first time. Each
proposal is a rule the team already follows, placed where it is enforced
best: a lint rule or test, a verifier, a Code Review lens, or agent context.

It works on its own, or as a next step after `factory-setup` once the first
automation is live.

Rules for the whole session:

- Ask one question per message, even when your question tool can hold several,
  and wait for each answer.
- Use plain words. See [Plain words](../factory-setup/SKILL.md#plain-words).
- Show the [progress timeline](#progress-timeline) in the first message you
  send after a stage changes, when you stop before the end, and at the end.
- Change nothing in the repository, the Tessl workspace, or GitHub until the
  user has picked what to apply in step 5.

## Preflight

Run the checks in [factory-setup step 2](../factory-setup/SKILL.md#2-preflight)
that apply here: the Git repository and GitHub remote, `tessl.json`,
`git status` for uncommitted changes, `tessl whoami`, and the GitHub repo and
its default branch. Stop where those checks stop.

Then fetch the default branch and compare `HEAD` with
`origin/<default branch>`. Steps 1 to 5 read the checked-out files, and step 6
branches from `origin/<default branch>`. When they differ, tell the user and
ask them to switch to an up-to-date default branch before you go on.

## 1. Baseline

Read before you ask:

1. Score the repository following [scoring](references/scoring.md). If you
   cannot start a subagent, skip the score, tell the user in one line, and go
   on to the next item.
2. List the agent context files: `AGENTS.md`, `CLAUDE.md`, rules, skills, and
   anything `tessl.json` installs.
3. Find the linters, CI workflows, and the test and build commands.
4. Check Code Review with `tessl code repo view <owner/repo> --json`. Note
   whether `repository.review.enabled` is true. If the command fails, for
   example because the user is in several organizations without `TESSL_ORG`
   set, Code Review is not available for the organization, or the repository
   is not selected for the GitHub App, treat Code Review as off, tell the user
   in one line, and continue without the lens tier.

Tell the user what you found in two or three sentences and ask them to
correct it.

Then check the skills this needs: `tessl-verify`, and
`create-code-review-lens` only when item 4 found Code Review on. In Claude
Code and Cursor they install as `tessl__<name>`, so match either name. If any
are missing, ask once, in one question, whether to install only the plugins
for the missing skills (`tessl/tessl-verify` for `tessl-verify`,
`tessl/code-review` for `create-code-review-lens`) for their account with
`tessl install -g <plugin>`, which leaves the repository unchanged. After the
install, confirm each skill is visible; if an install fails or a skill is
still not visible, say so in one line. A skill that is still missing, because
the user declined, the install failed, or it is not visible after the install,
makes its tier's items "needs <plugin>" items, shown but not applied, as
[the proposal list](references/routing.md#the-proposal-list) says. Tell the
user this once.

## 2. Gather evidence

Collect where agents and reviewers keep tripping, from the sources in
[evidence](references/evidence.md). Each source is optional; skip one that is
missing and say so.

## 3. Build candidate rules

Turn the evidence into candidate rules, each one a thing the team already
expects and can name. Follow [routing](references/routing.md).

## 4. Route each candidate

Give each candidate one tier, following [routing](references/routing.md):

- lint or test: [lint](references/lint.md)
- verifier: [verifiers](references/verifiers.md)
- Code Review lens, only when Code Review is enabled for the repository:
  [lenses](references/lenses.md)
- agent context: [context](references/context.md)

## 5. Pick what to apply

Show the routed proposals, grouped by tier, each with its evidence in one
line, following [the proposal list](references/routing.md#the-proposal-list).
Lint and test proposals come first, and "needs <plugin>" items stay in their
tier's group; both are unnumbered, with no yes or no choice, and are never
applied. Only the other items are numbered. Ask which to apply; if there are
none, or the user picks none, stop.

## 6. Apply

Make the picked changes on one new branch, following
[apply](references/apply.md#start-the-branch-step-6). Write each tier's files
from its step 6 sections:

- agent context: [context](references/context.md#edit-an-existing-file),
  the step 6 sections its opening list names, ending with "Before you finish"
- verifiers: [verifiers](references/verifiers.md#1-write-one-file-per-rule),
  sections 1 and 2
- Code Review lenses: [lenses](references/lenses.md#write-each-lens), "Write
  each lens" and "The profile"

## 7. Backtest

Run each new verifier and lens on a few historical pull requests, following
the step 7 sections:

- verifiers: [verifiers](references/verifiers.md#3-count-existing-violations),
  sections 3 to 5
- Code Review lenses: [lenses](references/lenses.md#backtest), "Backtest"

Each count, sample, and backtest run spends credits; lint and dry-run
commands are free. Decide `warn` or an exclude list per verifier before
counting, skip a full count that cannot change a level, and ask once for one
plan with a firm total, as
[apply](references/apply.md#paid-runs-and-backtests-step-7) says. Tell the
user each one's catches, false alarms, and the replays that did not exercise
it, which replays used the merged result, and whether its backtest was weak.
Drop or fix the ones the user
rejects; a dropped verifier or lens is deleted. Then check the configuration
and commit, one commit per category, as
[apply](references/apply.md#check-the-configuration-end-of-step-7) says.

## 8. Open the pull request

Follow the step 8 sections of [apply](references/apply.md#re-score-step-8):

1. Re-score, when step 1 scored the repository: re-judge only the criteria
   the branch changed, and show the score before and after.
2. Ask: "Do you want me to push this and open a pull request?" If yes, push,
   open it with each change and its evidence and the lint proposals for the
   team to decide, and give the user its link.
3. Send the closing message, on either answer.
4. Offer the "Keep the harness current" starter from
   [starters](../factory-setup/references/starters.md#keep-the-harness-current),
   so the repository keeps improving as new review feedback lands.

## Progress timeline

Use the marks and format from
[the factory-setup timeline](../factory-setup/references/progress.md), under
the heading "Preparing your repository", with these stages:

- Baseline (step 1): score the repo and find its tools
- Gather evidence (step 2): read where agents and reviewers trip
- Propose rules (steps 3, 4): build candidate rules and pick a home for each
- Choose (step 5): you pick what to apply
- Apply and test (steps 6, 7): make the changes, try them on past pull
  requests, and check the setup
- Open the pull request (step 8): re-score, push, and wrap up
