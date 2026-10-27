# Verifiers

How to write `tessl change verify` verifiers for the candidates routed to
verifiers, register them so they do not fail on the repository's existing code,
and backtest them on merged pull requests.

The `tessl-verify` skill owns verifier authoring: the file shape, writing
`relevant_when` and the checklist, scopes, content filters, and tuning. Use it
for those. This page adds only what `prepare-repo` needs on top. Read
`tessl change verify --help` before running anything.

The `tessl-verify` skill does not ship with this plugin. The skills check in
[step 1](../SKILL.md#1-baseline) checks for it and handles a missing one.

Each section belongs to one step of the skill:

- **Step 4 (route):** use this page only to decide that a candidate belongs in
  the verifier tier. Write nothing yet.
- **Step 6 (apply):** sections 1 and 2, on the apply branch, only for the
  verifiers the user picked in step 5. They write the files, edit
  `tessl.json`, and run only free lint and dry-run commands.
- **Step 7 (backtest):** sections 3 to 5. The count and backtest runs in
  sections 3 and 5 spend credits, so each asks first with its rough cost.
  Section 4 runs only free lint and dry-run commands.

Write nothing and run no paid command before the user picks in step 5.

The sample, count, and backtest commands below pass `--verifier <id>` once
for each new verifier, so verifiers already in a reused group are not judged
or billed and do not mix into the results.

## 1. Write one file per rule

1. Pick a group name for this batch, such as the repository or the area the
   rules cover. Reuse an existing group in `tessl.json` when the new rules fit
   its `include` globs and its level.
2. Write one verifier JSON per picked candidate under `verifiers/<group>/`,
   named for the rule in kebab case, such as
   `verifiers/core/errors-name-a-next-step.json`. The file name, without
   `.json`, is the verifier id.
3. Write `context` from the candidate's evidence: why the rule matters, and
   where it was seen, such as "Raised in review on #31, #40, and #52" or "The
   `lint` job failed on this in 6 pull requests". For a bots-only candidate,
   write "Review bots flagged this on #31 and #40". Name pull requests
   and CI runs by number or link. Never paste review text, author names, or
   anything else from `evidence.json`.
4. When a lint rule or test enforces the whole rule (see
   [routing](routing.md#one-home-per-rule)), add a `covered_by` link, as in
   [Coverage links](#coverage-links).
5. Run `tessl change verify lint <file>` on each new file. Fix every error
   before going on.

## 2. Register the group

Edit the root `tessl.json`. Add or extend `verify.groups.<group>` and keep
every other key in the file as it is.

- **`path`**: the glob for the group's files, such as
  `verifiers/<group>/*.json`.
- **`include`**: globs for the source the rules apply to. Leave out tests,
  fixtures, generated files, and vendored code unless a rule governs them, by
  adding them to `exclude`.
- **`content`**: a prefilter, only when a rule applies just to files that
  contain certain code, such as `{ "containsAny": ["new UserFacingError("] }`.
  Put it on the group when it fits every rule, or under
  `verifiers.<verifier-id>.content` when it fits one. It must match every file
  that could break the rule; when you cannot be sure of that, leave it out.
- **`level`**: for a new group, set by the count in section 3. Start with
  `"warn"` until you have it. A reused group keeps its level; set each new
  verifier's level under `verifiers.<verifier-id>.level` instead.

Then run `tessl change verify --dry-run --all --show-files --group <group>`.
Check that each verifier lists the files you meant it to, and none that are
empty or wrong. Narrow `include` or the content filter until it does.

## 3. Count existing violations

Decide every verifier's rollout before any paid run, in the same single
question as the paid-testing plan
[apply](apply.md#paid-runs-and-backtests-step-7) describes. List every new
verifier in that question with the default rollout for all of them: `error` if
no file breaks it today, otherwise `warn`. The user can move a verifier to
`warn` until the existing code is fixed, which drops its count, or to `error`
with the files that break it today listed under
`verifiers.<verifier-id>.exclude`, which needs a full count. Only that
exclude-list rollout adds calls to the plan.

The plan takes its call counts from the sources apply lists, never from an
earlier rough number, and includes the samples, any counts, and the section 5
backtests. When it is above the default `--max-calls` of 300, also offer
narrower `include` globs.

Sample each new verifier first, with
`tessl change verify --all --group <group> --verifier <id> --sample 3`. A
single sample across all of them takes one file per verifier in turn, so a
small `n` leaves the later verifiers unsampled. Check that every new verifier
was judged. The sample is deterministic: sampling again after a tune judges the
same files. Read each failure. A failure where the code is fine is the
verifier overreaching: tune it with the `tessl-verify` skill and sample again.
A verifier whose sampled failures are not consistently real violations is left
out, not landed at `warn`: delete its file.

A real violation in the sample already rules out plain `error`. Unless the
user chose the exclude-list rollout, that verifier is `warn` whether or not the
rest of the files are counted: skip the full count for it, and for any verifier
the user chose to leave at `warn`, and say in the pull request that the count
was skipped and why. For the exclude-list rollout, a sampled violation is the
first entry in the list, and the full count below still runs.

For a verifier on the default rollout, count to decide between `error` and
`warn`. `tessl change verify` has no stop-at-first-failure flag, so count in
batches that double: the first 5 files its dry run listed, then the next 10,
then 20, and so on until a batch has a failure that is a real violation or the
list ends. Pass each batch's files as repeated `--include <path>` to
`tessl change verify --all --group <group> --verifier <id>`. A real violation
stops the count, and that verifier is `warn`. Only when the user
chose the exclude-list rollout does every file need judging: run
`tessl change verify --all --group <group> --verifier <id>` and count the
failures. When the planned count is above 300 judge calls, add
`--max-calls <planned count>`; without it the run stops before judging
anything. Overreaching failures still found here are tuned the same way and do
not count as violations.

Set each verifier's level from its real violations:

- **None found**: `error`.
- **Any found, or the count skipped**: `warn`, as a verifier override under
  `verifiers.<verifier-id>.level` when the rest of the group is `error`. This
  is the temporary rollout exception the `tessl-verify` skill allows: the rule
  is calibrated, and only existing code stops it gating. Record that reason and
  the count, or that it was skipped, in the pull request that adds the
  verifier. Instead, when the user chose it, keep `error` and the full
  `include`, and list the files that break it today under
  `verifiers.<verifier-id>.exclude`. That is added to the group's excludes, so
  new files are still checked.

Diff-scope verifiers do not run with `--all`. Count them in the backtest in
section 5 instead, and start them at `error` unless the backtest says
otherwise. When the user declines that backtest, or no pull request is left to
replay for one, set it to `warn` and say in the pull request that it is
uncalibrated.

For each verifier left at `warn` or with an `exclude` list, tell the user how
many files break it today (or that the count was skipped) and how to promote
it: fix those files, then set the
level to `error` or remove them from `exclude`, and run the count again.

## 4. Lint again

Run `tessl change verify lint` on every new or changed verifier file, then
`tessl change verify --dry-run --all --group <group>` to check that
`tessl.json` still loads.

## 5. Backtest

Pick 3 to 5 pull requests and the version of each to replay, as
[apply](apply.md#pick-and-replay-backtest-pull-requests-step-7) says. A pull
request counts for a verifier when it touches files that verifier covers.
Pick per verifier, so one pull request can be picked for some verifiers and
not others.

Replay each pull request in a temporary Git worktree outside the repository,
so the run sees that pull request's diff and nothing else:

1. Create the worktree detached at the replay base.
2. Copy in the new verifier files and the edited `tessl.json`, and commit
   them. Call this commit the baseline. Without it, the new verifiers count as
   changed and run against their whole scope.
3. Apply the replayed diff on top and commit it.
4. Run
   `tessl change verify --dry-run --base <baseline> --group <group> --verifier <id>...`,
   passing only the verifiers this pull request was picked for, and note the
   planned judge calls. A verifier whose evidence cites this
   pull request never runs on it.

Add up the planned judge calls across the replays and put them in the single
paid-testing plan from section 3. Pick and dry-run the replays before that plan
is shown. Once it is approved, run the same command without `--dry-run`, with
`--report-json` to a file outside the repository. Ask again only when the backtests would push the spend past the
approved total, or before a rerun after tuning, as
[apply](apply.md#paid-runs-and-backtests-step-7) says.

Remove each worktree when it is done.

For each verifier, tell the user:

- its catches, false alarms, and the replays that did not exercise it, as
  separate lists, as
  [apply](apply.md#pick-and-replay-backtest-pull-requests-step-7) says;
- which replays used the merged result, and whether the backtest was weak;
- roughly what it costs: the judge calls from the plan line, per pull request
  and in total.

Drop a verifier whose findings are mostly false alarms or points no reviewer
would raise: delete its file and any `verifiers.<verifier-id>` entry in
`tessl.json`, since the group's `path` glob loads every file left in place. Tune one that is close with the
`tessl-verify` skill and run it again. When a verifier found nothing in a
pull request, read that pull request's replayed diff and sort the result:

- **The diff violates the rule**: the verifier missed it. Confirm the
  violation in the replayed diff itself, not from what a reviewer said. Tune it with
  the `tessl-verify` skill and run it again, or delete its file until it
  catches that case.
- **The diff has code the rule applies to, and that code follows it**: a
  correct clean result. Keep the verifier, and tell the user it passed
  compliant code.
- **Nothing in the diff is code the rule applies to**: keep the verifier, and
  tell the user the backtest did not exercise it.

Say which you dropped and why.

## Coverage links

A `covered_by` entry tells verify that a lint rule or test already enforces
the rule, so the judge is skipped while the link is fresh. Each entry has:

- **`type`**: `lint`, `test`, or `other`.
- **`target`**: the repository-relative path of the lint rule or test file.
- **`hash`**: `sha256:` and the hex SHA-256 of the target file's bytes.
- **`verifier_fingerprint`**: `sha256:` and the hex SHA-256 of the verifier's
  rule. Build the hashed text from the trimmed `instruction`, then for each
  checklist item, in order, its `name`, trimmed `rule`, and trimmed
  `relevant_when` (empty when absent) joined by newlines. Join those parts
  with `\n---\n`.
- **`note`**: optional, what the target checks.

Compute both hashes with a short script once the verifier's wording is final.
`tessl change verify lint` prints each link as `rule fresh` or `rule-stale`;
fix a `rule-stale` link before going on. Lint does not check `target` or
`hash`, so then run `tessl change verify --dry-run --all --group <group>` and
confirm the verifier shows covered targets and no `coverage=target-stale` or
`coverage=rule-stale`. Editing the rule or the target later makes the link
stale, and the judge runs again.
