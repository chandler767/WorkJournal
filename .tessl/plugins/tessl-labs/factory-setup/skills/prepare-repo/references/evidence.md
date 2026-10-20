# Gathering evidence

Collect where agents and reviewers keep tripping into one file, then ask the
user what the history cannot show.

## 1. Run the collector

From the repository root, run the collector in this skill's `scripts/`
directory with Node 18 or later:

```bash
node <skill-dir>/scripts/collect-evidence.mjs
```

It reads the last 90 days and writes `evidence.json` to a new temporary
directory, outside the repository. The first line it prints is the file's
path. Each line after that is one source and its status. It changes nothing in
the repository, GitHub, or the Tessl workspace.

Change the window or the bounds only when the user asks, or when a source says
it was truncated and the evidence looks thin:

- **`--days N`**: how far back to read. Default 90.
- **`--repo owner/name`**: the GitHub repository, when the Git remote does not
  name it.
- **`--out DIR`**: where to write `evidence.json`. The collector refuses a
  directory inside the repository, including the whole Git worktree when
  `--root` is a subdirectory of it.
- **`--max-prs N`**: merged pull requests whose review feedback is read.
  Default 40.
- **`--max-comments N`**: review feedback entries kept in total. Default 600.
- **`--max-comments-per-pr N`**: feedback entries kept per pull request.
  Default 40.
- **`--max-pages-per-pr N`**: API pages of feedback read per pull request,
  counting pages whose comments are all blank. Default 6.
- **`--max-runs N`**: failed CI runs listed. Default 100.
- **`--max-commits N`**: commits read for reverts, fixes, and hotspots. Default 2000.
- **`--max-files N`**: hotspot files and context files kept. Default 60.
- **`--max-findings N`**: open Tessl findings kept. Default 50.

Read `evidence.json` once. Do not re-run the commands it already ran.

## 2. Read each source

Every source has a `status`:

- **`ok`**: the source ran. `truncated: true` means it hit its bound, so
  there is more of the same in the history. In `reviewFeedback`, `failedPrs`
  lists the pull requests whose feedback could not be read; the summary line
  counts them.
- **`skipped`**: a prerequisite is missing, such as `gh` not installed or not
  logged in, or the directory not linked to a Tessl project. `reason` says
  which.
- **`failed`**: the source tried and could not finish. `reason` has the error.
  `reviewFeedback` is `failed` when the feedback of every pull request it read
  failed, such as on a rate limit.

Tell the user in one line which sources were skipped or failed and what would
turn them on. Continue with the rest. Do not stop the session for a missing
source.

The sources, under `sources` in `evidence.json`:

- **`reviewFeedback`**: review bodies, inline comments, and conversation
  comments on the most recent merged pull requests in the window. Each entry
  has the pull request, `kind` (`review`, `inline`, or `conversation`), the
  `author`, `bot` (true for CodeRabbit, `tessl-code-review[bot]`, and other
  apps), the file and line for inline comments, and the body cut to 1,200
  characters. Repeated points across pull requests from people are the
  strongest evidence. A bot's comment is a suggestion, not a team position.
  When you cite a comment, name its author type and login ("person `alice`",
  "bot `pullfrog[bot]`"), and never attribute a bot's comment to a person or
  to "the maintainer". Read `bot` on each entry before you write who said it.
  An entry with no `author` has an unknown source: cite it as "unknown author"
  and count it as neither a person nor a bot.
- **`bugFixPrs`**: merged pull requests in the window whose title or label marks
  them as a fix or a revert. A fix that keeps landing in the same area points
  at a missing check.
- **`ciFailures`**: failed CI runs on pull requests, grouped by workflow, and
  the failed jobs and steps of the most recent ones. A step that fails often
  on pull requests is a check agents do not run before pushing.
- **`reverts`** and **`fixCommits`**: commits in the window whose subject marks
  them as a revert or a fix, with the files they touched.
- **`hotspots`**: the files changed most often in the window (`churn`), and for
  the top ones, the files they usually change with (`coChange`). Lockfiles,
  snapshots, and generated files are left out. A pair that always changes
  together is a rule waiting to be written: "when you change X, update Y".
- **`contextFiles`**: the agent context the repository already has, such as
  `AGENTS.md`, `CLAUDE.md`, `.cursor/rules`, Copilot instructions,
  `CONTRIBUTING`, skills, MCP configuration, and `tessl.json`. Skills and rules
  a plugin installed are not listed. A file whose real path is outside the
  repository, such as a symlink to a home directory, is listed with
  `readable: false` and is not read. Check a candidate rule against these
  before you propose it, so you never propose a rule the repository already
  states.
- **`findings`**: open Tessl findings for the linked project, with their
  analysis, recommendation, and evidence links. A finding that names a
  recurring problem is already a candidate.

## 3. Ask the user

The history shows what happened, not what the team already knows. Ask each
question below in order, in a plain message, not with a question tool, and
offer no answer choices. Keep each answer next to the evidence:

1. "What do reviewers keep saying on pull requests here?"
2. "What has broken in production, or nearly broken, that a check could have
   caught?"
3. "What do new people on the team get wrong in their first few weeks?"

Accept "nothing" or "not sure" and move to the next question. When an answer
matches something in `evidence.json`, say so, because that pairing makes the
strongest candidate.

## 4. Clean up

Delete the temporary directory once the candidates are built. Never copy
`evidence.json` into the repository: it holds review text and author names.
