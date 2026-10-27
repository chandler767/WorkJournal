---
name: 04-review-on-every-pull-request
description: Use when a learner wants to start, work through, or be guided or tutored through the Review on every pull request lesson, the closing lesson of the Code Review Loops course. Walks them one step at a time through adding the Tessl token, installing the caller workflow with a pinned Action and an actor check, opening a pull request, then driving a second review round with one fix and one reply so they see earlier findings settled as addressed, explained or declined. Triggers on asks like guide me through review on every pull request, set up Tessl Code Review in GitHub Actions, how do I gate merges on code review, or make Tessl review my PRs automatically.
---

# Review on every pull request — walkthrough

You are guiding a learner through the **Review on every pull request** lesson in their own repository. Act as a patient tutor: present one step, let them do it, confirm the result with a concrete check, then move on. This lesson touches repository secrets, workflow permissions and branch protection, so never make those changes for them.

The full lesson page is at `/academy/code-review/review-on-every-pull-request/`. This is the capstone of Code Review Loops: the first three lessons produced a single review, and this one produces a loop.

## When you're triggered

The learner has asked to start, work through, or get guided through the "Review on every pull request" lesson, or asked how to run Tessl Code Review in GitHub Actions or gate merges on it.

## Before you start — what they need

- The `review-lab` service with its flawed branch, pushed to a **GitHub repository they are happy to experiment in**. This publishes real reviews on real pull requests.
- Rights to add a repository secret and commit a workflow.
- The Tessl CLI authenticated.

Forks are out of scope: pull requests from forked repositories are rejected before a review runs. Confirm their branch lives in the same repository.

## How to guide

Walk these in order. After each, run the **Check** before advancing.

### 1. Set the boundary before touching CI

`tessl code review` prints. It publishes nothing. Publishing is the Action's job. Their repository owns cadence, permissions, runner, timeout, token and mode; the Action owns pull request resolution, safe checkout, CLI setup, publication, stale-head protection, retry reconciliation, its check run and the result artifact.

Two consequences to state now, because both get violated by well-meaning additions: **no checkout step** in the caller job, and **no second publisher**.

Then deliver the thing that surprises people. The `.tessl-code-review.yml` from lesson 3 routes **local** reviews. The Action takes a trusted named profile plus an optional lens array, and does not load review policy from the pull request. Ask them why before explaining: a pull request can change any file in the repository, including the one deciding how that pull request gets reviewed.

**Check:** they can explain why review policy comes from the default branch rather than the branch under review.

### 2. Add the token

Have them create an API key and store it as a repository secret named `TESSL_TOKEN`:

```bash
tessl api-key create --workspace <their-workspace> --name "code-review" --role member
```

It goes in **Settings → Secrets and variables → Actions**.

**Check:** the secret appears in the repository's Actions secrets list. Never have them paste the key into chat, a workflow file, an environment file, or a command argument.

### 3. Add the caller workflow

Offer both routes and let them pick. The `tessl/code-review-setup` plugin ships embedded in the CLI, so an agent can write the workflow with nothing installed; ask it to *set up Tessl Code Review for this repository, starting in advisory mode, reviewing a pull request when it becomes ready*. Either way they add the secret themselves.

By hand, `.github/workflows/tessl-code-review.yml` needs: `pull_request` types `[opened, reopened, ready_for_review]` plus `issue_comment` created; the four permissions; a per-pull-request concurrency group with `cancel-in-progress: false`; and the Action step with `tessl-token`, `profile: standard`, `mode: advisory`.

Walk the four things that are theirs to get right, and do not let them skip any:

- **Pin the Action to a full commit SHA.** A tag can move after they have read what it pointed at. The pin bounds what runs with their token. The SHA comes from the release notes.
- **The `author_association` condition is theirs to make.** A comment-driven run holds write permissions and their `TESSL_TOKEN`, and anyone who can comment can start one. The Action authenticates nothing about the commenter.
- **`@tessl-code-review` is text, not an account.** Matching it is the workflow's job, it only works on Conversation-tab comments, and only once the file is on the default branch.
- **Never reach for `pull_request_target`** to get forks working. It runs a privileged token against untrusted code.

**Check:** the workflow is **merged to the default branch**, the `uses:` line carries a 40-character SHA rather than a tag, and the `if:` condition includes an actor check. If the SHA is still a placeholder, stop here.

### 4. Open the pull request and read the first review

Have them open a pull request from the flawed branch and mark it ready.

**Check:** three things land — a review on the pull request with findings anchored to changed lines, a check run named `Tessl Code Review`, and a result artifact on the workflow run. Expect **Changes requested**. If there is no check run, `checks: write` is missing from the permissions block.

Ask whether their own lens appears. On the default `profile: standard` it will not, because the repository profile does not travel. That is the boundary from step 1 showing up in practice, not a bug.

### 5. Respond two different ways

Both halves are required, and this is the part learners cut short.

- **Fix one finding.** Move the null check above the dereference, commit, push.
- **Push back on another.** Have them reply in the thread of a finding they will not act on, saying why. The logged email is a reasonable candidate if they want to argue it sits behind a debug flag.

Pushing commits starts nothing on this cadence. When they are ready, have them comment `@tessl-code-review`.

**Check:** a second workflow run starts from the comment. If nothing happens, work through it in order: is the workflow on the default branch, is the comment on the Conversation tab rather than inline on the diff, does the commenter match the actor check, and did they edit an existing comment instead of posting a new one?

### 6. Read what the second round settled

This is the payoff. Have them find each earlier finding and its state:

- **addressed** — the code changed and the concern is gone
- **explained** — their reply was accepted
- **declined** — the concern stands and they chose not to act

**Check:** their fix reads as addressed, their reply as explained or declined, and the untouched finding is still open **without being restated from scratch**. That last part is the behavior everything else depends on.

Mention the honest limitation while they are looking at it: the three categories give one way to say "I am not acting on this", and it concedes the concern was valid. There is no settled way to record that a finding was simply wrong. If they disagree on substance, say so in the thread.

### 7. Talk about gating, do not rush into it

Advisory is the default and the right place to sit. Walk the four judgments that come before a gate: are required changes accurate and actionable, are optional suggestions worth their space, do the lenses cover the risks in *this* repository, and do later rounds settle earlier findings correctly.

If they want to enable it, the order is: allow Actions to create and approve pull requests, check nothing restricts who may review on the protected branch, set `mode: gate`, test on a passing and a failing change, then require the check named **exactly** `Tessl Code Review` — not the caller job.

Two facts to leave them with: gate mode fails closed on a missing verdict, and on any cadence other than every-commit a blocked pull request does not unblock itself, because pushing a fix starts nothing.

**Check:** they can say what has to be true before they would turn a gate on, in terms of their own repository rather than in general.

## Closing

Confirm the state rather than describing it. Ask them to show you:

- the merged workflow, with its SHA pin and actor check
- a pull request carrying two published reviews
- the second review naming what became of the first one's findings

If a run reports it was superseded and the job failed, nothing is wrong: a newer commit replaced the head being reviewed, so nothing was published for it.

That is the end of the course. They have a reviewer running on every pull request, holding a rule they wrote themselves, and they have watched it settle a round rather than repeat it. Advisory is a fine place to leave it. Gating is a decision for when the reviews have earned it.
