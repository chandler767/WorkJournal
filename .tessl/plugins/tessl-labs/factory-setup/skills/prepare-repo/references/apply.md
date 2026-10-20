# Applying the picked items

How to make the picked changes on one branch, check them, and land them as one
pull request the team can review. Each section names the
[SKILL.md](../SKILL.md) step it runs in.

Nothing here runs before the user picks in step 5. Lint and test proposals are
never applied; they go into the pull request body for the team to decide.

## Start the branch (step 6)

1. Fetch the remote default branch from the preflight and start one branch
   from `origin/<default branch>`, not from a local copy that may be out of
   date.
2. Name it `prepare-repo-for-agents`. If that name exists locally or on the
   remote, add `-2`, `-3`, and so on until the name is free.
3. Record the commit the branch starts from: `git rev-parse HEAD`. Call it the
   branch start. The re-score diffs against it.

## Write the files (step 6)

Make every picked change on that branch, one tier at a time, from the step 6
sections that [SKILL.md step 6](../SKILL.md#6-apply) lists. Write only what
the user picked. Leave every file outside those changes as it is.

Before writing each file, read it again on the new branch. When an item no
longer fits it, such as a heading that is not there, a line it already states,
or a verify group that is missing, skip the item and tell the user.

## Paid runs and backtests (step 7)

The step 7 sections that [SKILL.md step 7](../SKILL.md#7-backtest) lists
count existing violations and backtest each new verifier and lens. Every
count, sample, and backtest run there spends credits; the `tessl change verify
lint` and `--dry-run` commands are free and need no approval. Ask once for all of it.
Before asking, pick the verifier and lens backtest replays and dry-run the
verifier ones, as
[verifiers](verifiers.md#5-backtest) section 5 steps 1 to 4 say, so their
calls are known. Then show one plan listing every paid batch with
its calls and a firm total: each sample at its `--sample` size per verifier
(the dry-run plan line ignores `--sample` and shows the full count), each count
and verifier backtest from its `--dry-run` plan line, and the lens runs as one
`tessl change review` per lens and pull request, which has no dry run. A count
that can stop early is planned at its full dry-run number, so say that the
total is an upper bound for those counts, and afterwards report the calls
actually spent. Ask in the same message which rollout each verifier gets, as
[verifiers](verifiers.md#3-count-existing-violations) section 3 says. Say that
pricing is at https://tessl.io/pricing/, and ask once. Take every number from
those sources, not from an earlier rough figure, and when a number changes between
messages, say what changed. Ask again only when a later batch would take the
spend past the approved total, saying by how much and why, or before a rerun
after tuning. A run the user declines is skipped. A declined count leaves the new
verifier at `warn`, as [verifiers](verifiers.md#2-register-the-group) section
2 says. A diff-scope verifier whose backtest is declined, or that has no pull
requests left to replay, also drops to `warn`, since its backtest is its only
count; the pull request says it is uncalibrated. Any other declined backtest leaves the verifier or lens
untested; say so in the pull request.

## Pick and replay backtest pull requests (step 7)

The verifier and lens backtests share how they choose pull requests and which
version of each they replay. A backtest should show whether a rule catches a
problem before review fixes it, and how often it flags code that was fine.

Pick the pull requests:

1. Take merged pull requests from the evidence window that touch the files
   the rule covers.
2. Leave out every pull request behind the candidate's evidence, for
   verifiers and lenses alike: the pull requests its review comments and
   commits came from, and for each CI run, the pull request whose head
   branch it ran on (`ciFailures` in `evidence.json` records the branch).
   Take them from the evidence the step 5 list showed, not from a
   verifier's `context`, which may name none of them. The rule was built
   from those, so replaying them tests nothing new. A CI run names its pull
   request only when it is one of the `recent` runs and exactly one merged
   pull request in the window has its branch as head branch; a failure
   counted only in `byWorkflow` names no pull request. When any of a
   candidate's CI evidence cannot be tied to one pull request that way, its
   exclusion is incomplete: report that rule's backtest as weak and say a
   replay may be one of its own evidence pull requests.
3. Keep 3 to 5. When fewer than 3 are left, use the ones there are, and tell
   the user the backtest for that rule is weak and why: how many pull
   requests outside its evidence touch its files. Do not fill the gap with
   evidence pull requests. When none are left, the rule is not backtested;
   say so. A diff-scope verifier with none left drops to `warn`, as
   [Paid runs and backtests](#paid-runs-and-backtests-step-7) says.

Find the version of each pull request to replay, as it stood when review
started, so the problems review caught are still in the diff:

1. Read the pull request's reviews, inline review comments, and
   conversation comments from GitHub, with their times. These are the same
   feedback the evidence collector reads, but `evidence.json` keeps neither
   their times nor their commits, so this is a new read for the backtest
   pull requests only. Keep only what the collector keeps: records with a
   non-empty body. Leave out conversation comments from bots (a user of
   type `Bot`, or a login ending in `[bot]`) and from the pull request's
   author; only a conversation comment from another person counts. Time a review by its `submitted_at`, when it became
   visible, and skip one without it (a pending draft); time a comment by
   its `created_at`. The review-start commit is the commit the earliest
   record was made on: a review's `commit_id`, or an inline comment's
   `original_commit_id`. A conversation comment records no commit, and a
   commit's dates do not say when it was pushed, so when a conversation
   comment came first there is no review-start commit. Times have
   one-second precision; when the earliest second holds a conversation
   comment, or records that name different commits, there is no
   review-start commit either. With no review-start commit, skip to step 4.
2. Fetch `pull/<number>/head`, then the review-start commit by its SHA. A
   force-push can leave that commit unreachable.
3. The replay base is `git merge-base <review-start commit> <merge commit>^1`,
   and the replayed diff runs from that base to the review-start commit.
   Compare its changed files with the merged result's (step 4). When the
   replay changes a file the merged result does not, the pull request was
   stacked on another branch or retargeted when it was reviewed, so the
   base pulls in other pull requests' code: use the merged result instead.
4. When the pull request had no review feedback, step 1 found no
   review-start commit, that commit cannot be fetched, or step 3 found the
   replay stacked or retargeted, replay the merged
   result instead: the diff from the first parent of its merge commit to
   the merge commit. Tell the user which replays used the merged result and
   why (no feedback, a conversation comment or a tie as the first feedback,
   an unfetchable commit, or a stacked or retargeted pull request), since
   review fixes may already be in them.

Report each rule's results in three separate lists:

- **Catches:** real problems it flagged in the replayed diff, and for each,
  whether a reviewer raised the same point and whether it was fixed before
  merge.
- **False alarms:** findings where the code was fine.
- **Not exercised:** replays whose diff has no code the rule applies to.

A verifier or lens the user drops in step 7 is deleted: remove its file under
`verifiers/<group>/`, or its directory under the lens path, and its entries in
`tessl.json` or `.tessl-code-review.yml`. Verifiers load from the group's
`path` glob, so a file left in place still runs.

## Check the configuration (end of step 7)

Check the configuration before you commit, so every fix lands in the commit
of its category:

1. Run `tessl doctor --automations`. It checks only that `tessl.json` loads and
   validates its `actions`, `triggers`, and `schedules` blocks, which this
   branch does not change. Act only on a report that `tessl.json` does not
   load, and ignore its other sections, as
   [factory-setup step 7](../../factory-setup/SKILL.md#7-write-tessljson)
   does for the configuration it did not change.
2. For each verify group this branch added or changed, run
   `tessl change verify lint` on its files, then
   `tessl change verify --dry-run --all --group <group>`. Check that it lists
   only the verifiers that survived step 7, and no `coverage=target-stale` or
   `coverage=rule-stale`.
3. Nothing checks `.tessl-code-review.yml` locally. When the branch changed
   it, read it again against [The profile](lenses.md#the-profile) rules.

Fix what these find. Tell the user what each finding means for them, not what
it says.

## Commit (end of step 7)

Commit once the backtests are settled, so the tuning lands in the same commits.
Make one commit per category, in this order, and skip a category with no
picked items:

1. **Context:** the context files, and any `.tessl/memory/` files the picked
   items edited.
2. **Verifiers:** the verifier files that survived step 7 and `tessl.json`.
3. **Lenses:** the lens files that survived step 7 and
   `.tessl-code-review.yml`.

Follow the repository's commit message convention from its context files or
recent history. Stage each file by path, and check the staged list before each
commit. Never commit `evidence.json`, score files, backtest reports, or
temporary worktrees.

## Re-score (step 8)

When step 1 scored the repository, re-score the branch following
[the re-score in scoring](scoring.md#re-score-step-8), after every commit is
made. It re-judges only the criteria whose evidence the branch changed and
carries the step 1 result over for the rest, so the score moves only where the
branch did. Show the score before and after, and each re-judged criterion's
coverage before and after. The score directory is outside the repository, so
its files never reach the pull request.

## Push and open the pull request (step 8)

Ask: "Do you want me to push this and open a pull request?" If the user says
no, tell them the branch name and go on to the closing message. If yes, push
the branch, open one pull request against the default branch, and give the
user its link.

The pull request body says:

- each applied change, grouped by category, with its rule and evidence in one
  line, as the step 5 list showed it;
- for each verifier, its level, and when it is `warn` or has an `exclude`
  list, how many files break it today, as
  [verifiers](verifiers.md#3-count-existing-violations) says, or that it was
  not counted because the user declined the count or the count was skipped and why;
- for each backtest, its catches and false alarms, which replays did not
  exercise it or used the merged result, whether it was weak, and which
  items were not backtested;
- the lint and test proposals, in the format from
  [lint](lint.md#what-a-proposal-says), under a heading that says they are
  not applied and are for the team to decide, followed by any "needs
  <plugin>" items with the plugin each one needs;
- the score before and after, when step 1 scored the repository.

When the branch added or changed lenses, also give the user
[Tell the user](lenses.md#tell-the-user) from the lenses reference.

## Closing message (step 8)

End with one message, whether or not the user chose to push:

- the branch name, and the pull request link when there is one;
- the lint and test proposals, in the format from
  [lint](lint.md#what-a-proposal-says), saying none were applied;
- any "needs <plugin>" items, with the plugin to install for each;
- the final [progress timeline](../SKILL.md#progress-timeline).

## Offer to keep it current (step 8)

Offer the
[Keep the harness current](../../factory-setup/references/starters.md#keep-the-harness-current)
starter, so the repository keeps improving as new review feedback lands. If
the user agrees, set it up with factory-setup, not on this branch: run its
[preflight](../../factory-setup/SKILL.md#2-preflight),
[step 3](../../factory-setup/SKILL.md#3-learn-how-the-user-works) and
[step 4](../../factory-setup/SKILL.md#4-show-the-current-factory), then go to
[its step 5](../../factory-setup/SKILL.md#5-pick-the-first-automation) with
this starter chosen. Steps 3 and 4 find how changes reach the default branch,
what a cloud run needs installed, and the automations and environments already
set up. The user has already picked what to add, so skip their ticket, goal,
and what-to-do questions. Its steps 6 to 8 create or reuse the environment,
start a branch of their own, check the setup, and ask before pushing.
