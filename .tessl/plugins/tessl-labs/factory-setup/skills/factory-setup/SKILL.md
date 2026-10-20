---
name: factory-setup
description: >-
  Set up, explain, or change a Tessl factory on a repository: agents that act
  on events in the repo, such as triaging new tickets, shipping tickets as
  pull requests, answering reviews and fixing checks on those pull requests,
  reviewing code, or running a job on a schedule. Use when someone wants Tessl agents to do work in their
  repo, asks what a factory is or what theirs does, or wants to add to,
  change, or customize it.
---

# Factory setup

A factory is a set of automations on a repository. Each one is a rule: when
something happens (a ticket is filed, a pull request gets a review, a time of
day arrives), a Tessl agent runs a skill in the cloud and does one job.

Your job is to help the user pick the first automation that is useful to
them, set it up safely, show it working, and then help them see what else
they could add. Fit the factory to how the user works. Do not assume they use
pull requests, Linear, or any other tool until you have checked.

Rules for the whole session:

- Ask about one thing at a time.
- Use plain words. See [Plain words](#plain-words).
- Never put a generated identifier, such as a workspace name that starts with
  `ws-`, in a question or a choice. When there is only one workspace, call it
  "your Tessl workspace".
- Show the [progress timeline](references/progress.md) in the first message
  you send after a stage changes, even if you change stage without asking
  anything, when you stop before setup is finished, and in the recap.
- Change nothing in the repository, the Tessl workspace, or GitHub until the
  user has said yes in step 1, or, when adding to an existing factory, to
  "Is this what you want?" in step 5.
- Use `--workspace <workspace>` only with these Tessl commands:
  `project link`, `project create`, and `integrations show linear`.
- Do not pass it to `tessl doctor` or `tessl install`. `gh` and `git` commands
  do not take it.
- Give the user a browser link whenever you create or check a Tessl resource
  they may want to look at. Use the `webUrl` or `Web URL:` value the Tessl
  command prints. Never guess a link or link to an API URL. If a command prints
  no link, say where to find the page in the web app instead. Write every link
  as the plain URL, never as markdown link text, so a terminal shows it. See
  [Browser links](references/links.md).

## 1. Orient the user

If the user has already said they want to add an automation to an existing
factory, skip this step. Do not describe what already exists. Say in one line
that new runs use Tessl credits, link to
[Tessl pricing](https://tessl.io/pricing/), and go to step 2.

Before you run anything that changes state, tell the user in a few short
sentences:

1. What a factory is (the paragraph at the top, in your own words).
2. Three or four examples from [the starter automations](references/starters.md)
   that could fit this kind of repository.
3. What setup will create: a Tessl project linked to the repo, one cloud
   environment for the agents, the Tessl GitHub App on this repo, a few
   entries in `tessl.json`, and maybe a label. Code Review uses a second
   GitHub App, Tessl Review.
4. That runs use Tessl credits, and link to [Tessl pricing](https://tessl.io/pricing/)
   for current details.
5. That every automation can be turned off by removing it from `tessl.json`.

Then show the [progress timeline](references/progress.md), with every stage to
do, so the user sees the whole path before they agree. Ask: "Do you want to go
ahead?" Stop if they say no.

## 2. Preflight

1. Check that the Tessl CLI is up to date. Run `tessl --version` first. If it
   is obsolete, follow [Check the Tessl CLI](references/cli-version.md) and
   stop. Otherwise run `tessl cli update --dry-run`. If it reports an
   available update, tell the user to run `tessl cli update`, then check which
   CLI the shell runs, as the same reference says. Do not run `tessl update`:
   that updates plugins, not the CLI. If it fails because self-update is
   disabled, the CLI came from winget: follow the same reference. If the
   version check fails for another reason, tell the user to resolve the CLI
   update before continuing.
2. Work from the repository root. Confirm that the folder is a Git repository
   with a GitHub remote. If it is not a Git repository, or has no GitHub remote,
   tell the user that automations run against a GitHub repository. Ask whether
   to point at an existing repository or create one. Stop without changing
   anything until the user chooses.
   Check whether the repository is a fork
   (`gh repo view --json isFork,parent`). If `isFork` is true, say in one line
   that it is a fork of the `parent` repository and that the factory will run
   on the fork, not on the parent, then continue. Never move the user onto a
   fork to get write access, for example from an upstream with read-only
   access to their fork.
3. If there is no `tessl.json`, tell the user to run `tessl init` and stop.
   If the repository is a fork and `tessl.json` already has `actions`,
   `triggers`, or `schedules`, they came from the parent. Before you link or
   create a project, list them in plain words: what starts each one and what
   it does. Say that they start running on the fork once its project exists
   and the file is on the default branch, and that any action naming an
   environment from the parent is refused until an environment with that name
   exists in the user's workspace. Ask whether to keep, change, or remove each
   one, and make that change in step 7.
4. Read `mode` from `tessl.json`. If it is `vendored` and
   `git ls-files .tessl/plugins` lists nothing, the plugins installed here
   would be committed with the setup, often thousands of lines. Tell the user
   in two sentences: managed mode keeps installed plugins out of git, like
   `node_modules`, and every clone and cloud run installs them from
   `tessl.json`; vendored mode commits them. Recommend managed, and ask. If
   they choose managed, set `"mode": "managed"` in `tessl.json` and run
   `tessl install`, which writes the ignore rules. If `.tessl/plugins` is
   already committed, leave the mode as it is unless the user asks.
5. Run `git status`. If there are uncommitted changes, say so and ask the user
   to commit them or put them aside before you continue. Do not move or stash
   them yourself.
6. Run `tessl whoami`. If it fails, ask the user to run `tessl login`.
7. Run `tessl project repair --json`. The workspace is
   `linkedProject.workspaceName`, and `webUrl` is the project's browser link.
   If there is no linked project, and step 1 was
   skipped, do not link or create one: tell the user this repository has no
   factory yet and go to step 1. Otherwise list the workspaces with
   `tessl workspace list`. If there is one, use it without asking. If there
   are several, ask which to use. Then run `tessl project link --workspace <workspace>`, or
   `tessl project create --workspace <workspace>` if no project matches. Then
   run `tessl project repair --json` again. Keep `linkedProject.id` and
   `webUrl` for the rest of the session, and give the user the project link.
8. Run `tessl org list --json`. If the organization's `credits.state` is not
   `ok`, tell the user that factory runs will not start until the
   organization has credits, and ask whether to continue.
9. Find the GitHub repo and its default branch
   (`gh repo view --json nameWithOwner,defaultBranchRef`). Use
   `defaultBranchRef.name` wherever these steps say `<default branch>`. Check
   that the Tessl GitHub App can write to it:
   `tessl integrations show github-app-agent --json` and the exit status of
   `tessl github token --repo <owner/repo>`. Run the
   token check with both output streams redirected to `/dev/null`, and use only
   its exit status. Never capture, print, log, or repeat its output because it
   contains a live installation token. If either check fails, give the user a
   link from the integration JSON: `installUrl` when the app is not installed.
   When it is installed on the repository owner's account, give that entry's
   `manageUrl` (the GitHub page where repository access is granted) first,
   then `webUrl`. When no entry is on that account, give `webUrl` and ask them
   to install the app on the owner's account. If there is no link, send them
   to the organization settings, under Integrations, GitHub. Ask them to install the Tessl GitHub
   App there, or, if it is already installed, to grant it access to this
   repository. Say that it asks for read and write access so agents can
   comment, push branches, and open pull requests. Show the timeline with
   "Check your setup" marked `◐` and wait until they say it is done. Then run
   both checks again. If they pass, carry on with step 3. If not, say which
   check still fails and wait again.

## 3. Learn how the user works

Read before you ask:

- `CLAUDE.md`, `AGENTS.md`, and `CONTRIBUTING.md`, for rules about branches,
  pull requests, and releases.
- `git log --first-parent -30 <default branch>`. Merge commits mean work
  arrives through pull requests. Plain commits mean people push to the
  default branch.
- `gh pr list --state merged --limit 10` and
  `gh api repos/<owner>/<repo>/rules/branches/<default branch>`.
- Where tickets live: open GitHub issues (`gh issue list --limit 5`), or a
  Linear connection (`tessl integrations show linear --json`).
- CI: the files in `.github/workflows/`.
- What a cloud run needs to build and test: package manifests, lockfiles,
  toolchain files, and the install steps in CI.

Tell the user what you found in two or three sentences, for example "You push
straight to main, file GitHub issues, and have no CI." Ask them to correct
it. If the user has already said what they want to add to an existing
factory, skip the first-ticket question and the goal question below.
Otherwise, if there are
no open GitHub issues and either no Linear connection or no open Linear issues,
recommend a small, safe first ticket, such as a README
fix, and ask: "Want me to file a small first ticket, such as adding a README,
and show it working?" Otherwise, ask: "Is there one thing you want Tessl to
take off your plate first?" Do not create an issue until the user says yes.
If the repository needs tools or dependencies installed to build or test, tell
the user which ones. Say that Tessl runs `.tessl/cloud-launch-setup.sh` before
every cloud run, and offer to write it to install them safely more than once.
Ask before writing the hook. Do not mention the hook when the repository needs
no setup.

## 4. Show the current factory

Fetch the default branch (`git fetch origin <default branch>`) and read
`tessl.json` from the remote copy
(`git show origin/<default branch>:tessl.json`). The local file can be behind,
for example after a setup pull request merged on GitHub. If the local file
differs, say so. If the fetch fails, say so and stop. If the fetch works but
the default branch has no `tessl.json`, nothing is set up there yet. Find the
`actions`, `triggers`, and `schedules`, and the environments they use
(`tessl environment view <env> --json`). On a fork whose project was created
in this session, entries inherited from the parent are not on yet: describe
them as preflight did, not as automations already running. Check Code Review for this
repository with `tessl code repo view <owner/repo> --json`, and read
`repository.attentionReason` and, under `repository.review`, `enabled`,
`triggerPolicy` (`open-and-command` reviews when a pull request opens,
`every-push` on every push), `publicationMode`, and `autoApproveWhenResolved`.
Tell the user whether Code Review is on, how it publishes, and whether it will
review the setup pull request. It reviews only when `enabled` is true and
`attentionReason` is null; a non-null reason, such as
`installation-suspended` or `missing-permissions`, means the GitHub App cannot
act on the repository, so name the reason. A missing Loop or factory entry
does not mean Code Review is off. If the repository command fails or the
repository is not listed, run
`tessl integrations show github-app-code-review --json`:

- If its `integrations` list is empty, the Tessl Review app is not connected
  to this workspace. Say Code Review is not set up yet, and keep the `webUrl`
  it prints for step 5.
- If the list has an entry, the app is connected but cannot see this
  repository. Say the repository's Code Review state is unknown. Do not say
  Code Review is absent.

If nothing is set up, say so. If you skipped step 1, go back to step 1 now.
Otherwise, go to step 5.

If something is set up, tell the user in plain words which automations are
on, what starts each one, and which skills are their own copies. Give the
link to each one's Loop: the `webUrl` of each entry in
`tessl schedule list --project-id <linked project id> --all --json`, with the
project ID from `linkedProject.id` in `tessl project repair --json`. Then
suggest one or two automations from [the starters](references/starters.md)
that fit what they already have and how they work, and say what each would
save them. For example: "Tessl already ships your labeled issues. Code
Review could review every pull request, including the ones your team opens."
If an implement action is set up and no `follow-ticket-changes` action is, suggest adding
"Follow ticket changes" first: without it, runs keep going after a person takes an issue back, pull
requests stay open after their issue ends, and an answered or unblocked issue
waits for a person to move it.
Ask what they want to do: add an automation, remove one, change what starts
one, or [edit a skill](references/customize.md). If they
want to add an automation, go to step 5. Steps 1 to 3 have already run; do not
repeat them. In step 6, reuse the environment the existing actions use.

## 5. Pick the first automation

Set up the first automation, with one exception: whenever "ship tickets" is
the starter being set up, offer "answer reviews and fix checks" at the same
time, and Code Review too when step 4 found it not set up or unknown.

- If the user named a goal in step 3, set up the starter that meets it.
- If not, offer two or three starters from [the starters](references/starters.md)
  that fit how they work, each in one sentence, with your recommendation
  first.
- If they push straight to the default branch, do not offer automations that
  need a pull request on every change. Offer the ones that work without pull
  requests, and say that "ship tickets" opens one pull request per ticket.
  "Answer reviews and fix checks" acts only on pull requests Tessl opens, so
  offer it with "ship tickets" even for these users.
- If they want something no starter covers, follow
  [Write your own automation](references/starters.md#write-your-own-automation).

Whenever "ship tickets" is the starter being set up, first say what it does in
plain words: what starts it, what the agent does, what it changes, and how to
stop it. Say that it comes with a small loop, "Follow ticket changes" (the
`follow-ticket-changes` action), that keeps it in step with people: it
cancels runs when a person takes an issue back, closes the pull request when
the issue ends, and wakes the issue when a person answers Tessl's question or
its blocker ends. Then explain that "answer reviews and fix checks" acts only on pull
requests Tessl opens: it answers review comments, fixes failing checks, and says
when the pull request is ready to merge. If step 4 found Code Review not set up
or unknown, also explain "review pull requests": Tessl reviews every pull
request, including the ones Tessl opens, and the agent answering reviews acts
on those findings. Say that it needs the Tessl Review app and the repository
turned on for review, two steps in the browser. Recommend all of them together.
Ask which they want: all of them, "ship tickets" with "answer reviews and fix
checks", or "ship tickets" alone. Set up only what they say yes to. Add the
`tessl.json` entries in one change, follow
[Review pull requests](references/starters.md#review-pull-requests) for Code
Review, and show each chosen part working in the test step.

For every other starter, before you build it, say what it does in the same
plain terms and ask: "Is this what you want?"

## 6. Create the environment

Use one environment named `<repo>-factory`. If it exists, show its access and
ask before you change it. If not, create it with the access that the chosen
starter lists. When both "ship tickets" and "answer reviews and fix checks"
are chosen, use this one environment for both. Both need
`--github-access write`. For Linear, also keep `--linear-access write`,
because the agent answering reviews reads the Linear issue:

`tessl environment create <repo>-factory <flags>`, then confirm it with
`tessl environment view <repo>-factory --json`. Give the user the environment
link from `webUrl`. When you tell the user, show the
timeline with "Build it" now.

If `tessl environment create --help` has no flag that the starter needs, ask
the user to create the environment in the web app, under workspace settings,
Environments, with that access.

## 7. Write tessl.json

1. Pick where the change goes. Fetch the remote default branch first, and
   start any branch from it, not from a local copy that may be out of date.
   Name the branch `setup-tessl-factory`. If that name already exists locally
   or on the remote, name it after the starter being added:
   `add-tessl-ship-tickets`, `add-tessl-answer-reviews`, or `add-tessl-<name>`
   in lowercase words joined by hyphens for any other starter. If that name
   exists too, add `-2`, `-3`, and so on until the name is free.
   - If the user works through pull requests, create that branch and make
     every file change on it.
   - If the user pushes straight to the default branch, ask whether to commit
     there or use that branch.
2. Add the entries that the chosen starter lists. If both "ship tickets" and
   "answer reviews and fix checks" were chosen, point both actions and the
   follow-ticket-changes action at the same environment and add their
   triggers in this one change. Keep every other key in the file. For "ship
   tickets" with GitHub issues, replace `<user-id>` in the trigger with the
   GitHub user ID of the person setting up (`gh api user --jq .id`). Tell
   them in one line that only they can start a run by adding the label, and
   how to let someone else, as [the triggers reference](references/triggers.md)
   says.
3. If the user agreed to the setup hook in step 3, add
   `.tessl/cloud-launch-setup.sh` in the same change. It runs from the clone
   root before every cloud run, and its install steps must be safe to run more
   than once. Make every required setup failure stop the hook with a non-zero
   exit, for example by using `set -e` or checking each command's exit status.
   Write it as [the setup hook reference](references/setup-hook.md) says:
   compatible version ranges from the repository's own declarations, not CI's
   exact versions.
   Then test it before the diff:
   - If there is no hook, skip this.
   - Name any step in the hook that changes the machine outside the clone
     (global installs, version managers, Corepack, `sudo`). Say the test may
     take a few minutes, and ask the user's agreement. If they decline, skip
     the test and say the first cloud run will test the hook.
   - Make a fresh clone of the repository in a temporary directory and copy
     the new hook into it. From its root, run
     `CI=true bash .tessl/cloud-launch-setup.sh < /dev/null` twice, to show it
     is safe to run more than once. Delete the copy afterwards.
   - If Docker is available, make both runs in a container like the cloud
     machine: `ubuntu:26.04`, `--platform linux/amd64`, a non-root user
     without sudo, no build tools, Node 24, and `CI=true`. Otherwise make them
     on the user's machine, and prefer the container for any step that
     changes the machine outside the clone. Do not require Docker.
   - If a run fails, show the failing step. Fix the hook only if the failure
     would also happen on the cloud machine, then test again. If it comes from
     a local difference (another OS, a missing package manager), say so and
     leave that step alone.
   - If both runs pass, tell the user in one line that the cloud machine can
     still differ (preinstalled versions, no Docker, missing build tools), and
     that the first cloud run is the real check.
4. If the starter uses a label, create it if it is missing, for example
   `gh label create tessl`.
5. If the starter works on pull requests, check the branch rules from step 3,
   auto-merge, and "dismiss approvals on push". Tell the user how each one
   changes what the automation does. If the default branch requires an
   approving review, also warn that `Comment only` Code Review mode cannot
   satisfy that rule. Tessl can approve only when step 4 found Code Review
   enabled with a null `attentionReason` and `publicationMode` is
   `enforcing`. It then approves a pull request whose review has no blocking
   findings, or, with `autoApproveWhenResolved` on, once every Tessl Reviewer
   thread on the pull request is resolved. With auto-approve off, warn that a
   pull request with blocking findings still waits for a human approval. If
   step 4 found Code Review off, needing attention, or unknown, say that Tessl
   pull requests will wait for a human approval.
   Tell the user to open **Code reviews**, select the
   repository, and choose **Approve or request changes** if they want Tessl's
   review to satisfy the rule. Change nothing without asking.
   When Code Review and "answer reviews and fix checks" are both on, say what
   the review's `triggerPolicy` means for Tessl's pull requests:
   `open-and-command` reviews a pull request when it opens or when someone
   asks, so a fix the agent pushes after an approval is not reviewed again;
   `every-push` reviews every push, and each review uses credits. Recommend
   `every-push`, and ask. Apply the choice with
   `tessl code repo update <owner/repo> --review-trigger <policy>`. Do not
   change it on a repository that already has Code Review on without asking.
6. Run `tessl doctor --automations`. Act only on the Automation Configuration
   section; ignore every other section. Fix problems in that section. Tell the
   user what each warning means for them, not what it says. For example, "event
   delivery not confirmed" means "Tessl has not received a GitHub event from
   this repo yet. This is normal until the change is on the default branch."
   If it reports `Model "claude-opus-5-5" is not available to agent "claude" in this workspace.`, remove `model` from the factory actions, then run `tessl doctor --automations` again. If it then reports `The workspace organization has no default model configured for cloud-run-solver.`, stop and tell the user that their model provider needs a default for cloud runs or that they can set `model` to a model their provider enables. The local configuration passes when this section reports that `tessl.json` is valid and lists no configuration problems. Do not take the setup live while Agent selections or Event delivery is Undetermined; those online readiness checks must run successfully. Before the change is on the default branch, the Loops subsection reports that the project has not processed a `tessl.json` yet. That is expected, and the delivery lines then warn that nothing runs. When "answer reviews and fix checks" is set up, doctor always reports the `shepherd-checks-failed` and `shepherd-checks-passed` triggers as events that cannot be marked. These Information lines are not problems, and they do not block going live. Tell the user what they mean: runs that start when a check finishes leave no mark on GitHub, so watch them in the Loops tab. If it reports that cloud runs will fail installing a dependency's `file:` source, say in plain words that cloud runs install every dependency in `tessl.json` and will fail on that one, and offer to switch it to the published plugin. If it says the source is not committed, include it in the commit instead. Do not change it without asking.
7. If the repository's own checks fail on files that Tessl installed, say so
   and ask before you change any lint, format, or CI configuration. Do not
   edit the installed plugin files.
8. Show the user the diff and say in one sentence what each entry does.
   Then show the timeline with "Build it" done and "Go live and test" now.

## 8. Go live and show it working

1. Ask: "Do you want me to push this?" If yes, push the commit. If it is on a
   setup branch, open a pull request to the default branch and give the user
   its link. Ask: "May I merge this setup pull request?" Merge it only if the
   user agrees. If the user declines, ask them to merge it. If the merge is
   refused, by the agent's permission system or by GitHub, do not retry or work
   around the refusal; ask the user to merge it. The automations start only
   once `tessl.json` is on the default branch.
2. Fetch the default branch and read `tessl.json` from
   `origin/<default branch>`. Proceed only if it is readable and reflects the
   whole submitted change: every added or changed value matches and every
   removed entry is absent. Otherwise, say that the configuration is not live
   and stop. After confirming it is live, ask whether the user wants their
   local default branch updated. Update it only if they agree. If the agent's
   permission system refuses the update, do not retry or work around the
   refusal; tell the user to run `git switch <default branch> && git pull`
   themselves.
3. Give the user the project's Loops link: the project `webUrl` from
   [Preflight](#2-preflight) with `/loops` added, ending in
   `/go/projects/<project id>/loops`. Say which Loops they
   will see and what starts each. Then
   [confirm the Loops appear](references/confirm-loops.md). Do not say setup is
   finished until they do.
4. Ask: "Do you want me to show you it working?" If yes, follow
   [Testing the factory](references/testing.md).

## 9. Recap and suggest what is next

Show the [progress timeline](references/progress.md) with every stage done or
skipped. Then tell the user:

- Which automations are on, what starts each one, and the link to its Loop.
- How to stop one: say the "Stop it" line for each automation that is on, from
  [the starters](references/starters.md). Once the change reaches the default
  branch, it prevents new runs; it does not cancel one already in progress.
- One or two automations from [the starters](references/starters.md) that
  would fit next, and what each would save them. Ask if they want to add one
  now. If they do, run `git status` as in step 2, then go back to step 5 for
  the new starter. The Tessl project and GitHub App already exist; step 6
  reuses the environment. Start a new timeline for that automation, as in
  [Adding another automation](references/progress.md#adding-another-automation).
- Once the first automation is live and the user has answered the question
  about adding another automation, offer the
  [prepare-repo](../prepare-repo/SKILL.md) skill for any existing codebase, as
  a question in a message of its own. Say what it does in plain words: it
  reads the repo's review history, CI failures, and fix commits, and proposes
  agent context, checks, and review rules, so the automations make fewer
  mistakes. It changes nothing in the repository until the user picks what
  to keep; a missing skill it needs is installed for their account only if
  they agree. When step 3 found substantial agent context, such as a full
  `AGENTS.md` or `CLAUDE.md`, word it as a tune-up of what is there, not a
  first setup. When prepare-repo has already run on this repo, because its
  verifier groups are under `verify.groups` in `tessl.json` or its lenses are
  in `.tessl-code-review.yml`, offer the
  [Keep the harness current](references/starters.md#keep-the-harness-current)
  starter instead.

## Plain words

Say the plain words. If a command or screen shows the Tessl term, say what it
means in one line.

| Plain words                                                                                                        | Tessl term            |
| ------------------------------------------------------------------------------------------------------------------ | --------------------- |
| automation                                                                                                         | loop, trigger, action |
| cloud environment: the machine and access the agent gets                                                           | environment           |
| check the setup                                                                                                    | `tessl doctor`        |
| review rules: what Code Review looks for                                                                           | lens                  |
| answer reviews and fix checks: on Tessl's pull requests, an agent answers review comments and fixes failing checks | shepherd              |
| wait time: Tessl waits about 90 seconds after an event, so quick events start one run                              | debounce              |
