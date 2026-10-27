# Code Review lenses

How to turn the candidates routed to lenses into repository-local Code Review
lenses, add them to the repository's review profile, and backtest them on
merged pull requests. Use this tier only when Code Review is enabled for the
repository.

Each section belongs to one step of the skill:

- **Step 4 (route):** "Check that Code Review is on" and "Fit the lens
  budget". Plan only; write nothing and run no review yet.
- **Step 6 (apply):** "Write each lens" and "The profile", only for the lenses
  the user picked in step 5.
- **Step 7 (backtest):** "Backtest".
- **Step 8 (open the pull request):** "Tell the user".

## Check that Code Review is on

Use the result from [step 1](../SKILL.md#1-baseline). The tier is on only when
`tessl code repo view <owner/repo> --json` succeeded and
`repository.review.enabled` is `true` and `repository.attentionReason` is
`null`. The GitHub App being installed, or the repository being listed, is not
enough. When `attentionReason` is set, for example because the installation is
suspended or missing permissions, the App reviews nothing: tell the user the
reason in one line and treat the tier as off.

When it is off, offer no lens. Drop each lens candidate and say in one line
that it needs Code Review, as [routing](routing.md#the-ladder) says.

Note `repository.review.reviewProfileStatus` from the same output:

- **`present`**: the default branch already has `.tessl-code-review.yml`.
  Extend it.
- **`absent`**: there is no profile yet. Create one.
- **`unknown`**: fetch the remote default branch and check for the file
  there with `git show origin/<default branch>:.tessl-code-review.yml`, not in
  the local checkout, which may be on another branch or out of date. Treat it
  as `present` or `absent` from what you find.

## Fit the lens budget

A review counts every lens with work in a round: each lens without `globs`,
and each lens whose globs select a file in the change. When that count passes
8, the whole review fails with no findings; the extra lenses are not just
skipped. One pull request can touch several unrelated areas, so lenses whose
globs do not overlap can still run together. Plan the lenses before writing
any of them:

- **Defaults:** keep the default lenses from
  [`tessl/code-review`](#the-profile). A new profile lists all four. An
  existing profile keeps every entry it already has.
- **Local lenses:** add at most 4, and keep the profile's total lens count,
  existing and new, at 8 or fewer. Go over 8 only when the scoped lenses'
  globs split the tree into areas that one pull request does not touch
  together, and only when every lens without `globs` plus the scoped lenses
  any one change could select stays at 8 or fewer.
- **Too many candidates:** merge lens candidates that review the same area
  into one lens. When they still do not fit, keep the ones with the most
  evidence and tell the user which were left out.

Give each local lens one area: a directory, a language, or a kind of file. A
lens that reviews everything competes with the default lenses for the same
findings.

## Write each lens

Write each lens with the `create-code-review-lens` skill from the
`tessl/code-review` plugin. The skills check in
[step 1](../SKILL.md#1-baseline) checks for it and handles a missing one.
Give it:

- the rules this lens enforces, each with its evidence from the candidate;
- the area it reviews, as the paths you will give it in `globs`;
- the rules a linter, test, or verifier already enforces in that area, so the
  lens stays silent on them.

Save each lens as a `SKILL.md` under one directory, such as
`review-lenses/<lens-name>/SKILL.md`. When the repository already keeps local
lenses somewhere, use that directory.

## The profile

Write or extend `.tessl-code-review.yml` at the repository root.

A new profile starts from the four code lenses of the default review, then
adds one entry per local lens with its `globs`:

```yaml
schemaVersion: 1
lenses:
  - ref: tessl/code-review@0.5.0#review-correctness-and-data-integrity
  - ref: tessl/code-review@0.5.0#review-security-and-privacy
  - ref: tessl/code-review@0.5.0#review-scale-and-resilience
  - ref: tessl/code-review@0.5.0#review-maintainability-and-code-quality
  - ref: ./review-lenses/api-routes/SKILL.md
    globs:
      - src/api/**
      - '!src/api/**/*.generated.ts'
```

When extending a profile:

- Keep every existing entry, field, and comment as it is.
- Add each local lens as a new entry at the end of `lenses`.
- Do not add a local lens whose `ref` is already listed.

Rules for every local lens entry:

- **`ref`:** a path relative to the profile that starts with `./` and stays
  inside the repository.
- **`globs`:** repository-relative paths, at least one positive glob, at most 32. A later `!` glob excludes; a later positive glob includes again.
- **No profile-wide settings:** leave `reviewMode`, `requestChangesAt`, and
  `effort` as they are, unless the user asks to change them.

For the full schema, see "Code Review configuration" in the configuration
reference on docs.tessl.io.

## Backtest

Run each new lens on its own against 3 to 5 pull requests that touch its
globs. Pick them and the version of each to replay as
[apply](apply.md#pick-and-replay-backtest-pull-requests-step-7) says.
When a lens enforces more than one rule, leave out the evidence pull
requests of every rule it enforces, and say the backtest is weak for each
rule with fewer than 3 replays that touch its files. Report the three
result lists per rule, attributing each finding to the rule it names.

Each run is one `tessl change review` and spends credits. Before running any,
tell the user how many runs the backtest makes, one per lens and pull request,
and that they spend credits (https://tessl.io/pricing/). Put them in the
single paid-testing plan from
[apply](apply.md#paid-runs-and-backtests-step-7) and ask once for it. Ask again
only past the approved total, or before a rerun after a fix.

For each pull request:

1. Check out the replayed commit in a temporary worktree outside the
   repository: the review-start commit, or the merge commit when the replay
   uses the merged result.
2. From that worktree, run
   `tessl change review --skill <absolute path to the lens SKILL.md> --base <base> --path <pathspec>...`,
   where `<base>` is the replay base.
   `tessl change review` does not read the profile, so pass the files the App
   would route to the lens as `--path` pathspecs. When no positive glob
   follows a `!` glob, pass each of the lens's `globs` as a pathspec, writing
   a `!` glob as `':(exclude)<glob>'`. Otherwise git cannot express the
   order, because it applies every exclusion after every inclusion and so
   drops paths a later positive glob includes again. In that case, list the
   pull request's changed files with `git diff -M --name-status <base>`, so a
   rename shows both its old and new path. Apply the globs in order to each
   path. Select a rename when either of its paths ends up included, as the
   App does, and pass both of its paths. Pass each selected path as its own
   `--path ':(literal)<path>'`, so a name containing `*`, `?`, `[`, or a
   leading `:` selects only itself.
3. Compare the findings with what reviewers said on that pull request.

Then tell the user, per lens, its catches, false alarms, and the replays that
did not exercise it, as separate lists, as
[apply](apply.md#pick-and-replay-backtest-pull-requests-step-7) says. Among
the catches, say which reviewers also raised and which nobody raised. Say
which replays used the merged result, and whether the backtest was weak.

Drop a lens whose findings are only noise: delete its directory and remove
its profile entry. Fix
a lens with some false alarms by narrowing its rules or its `globs`, then run
it again on the same pull requests. Remove the temporary worktrees when you
are done.

## Tell the user

When you show the result, tell the user:

- The Tessl Review GitHub App looks for `.tessl-code-review.yml` on the
  default branch. When the profile is new, reviews keep running the default
  lenses until the pull request that adds it merges. When the profile is
  already on the default branch, what the App reads depends on the pull
  request's author. For an author with write access to the repository, or a
  GitHub App, it reads the profile and its local lenses from the pull request
  head, so an extension applies to the pull request that makes it. For any
  other author, such as a fork contributor, it uses the default branch copy.
- An author with write access can change the profile and its local lenses in
  their own pull request. Suggest a `CODEOWNERS` entry for
  `.tessl-code-review.yml` and for the directory of every local lens the
  profile references, including lenses it already had, with required
  code-owner approval.
