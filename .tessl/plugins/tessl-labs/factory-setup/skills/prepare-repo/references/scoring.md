# Readiness score

Score the repository in a subagent, never in this session. Use the same steps
in every agent, `tessl agent` included.

## Baseline (step 1)

1. Make a temporary directory outside the repository, for example with
   `mktemp -d`. Call it the score directory.
2. Record the commit you score: `git rev-parse HEAD`. Call it the baseline
   commit.
3. Start a subagent with the repository as its working directory and give it
   these instructions:
   - Read [the readiness rubric](readiness-rubric.md) and assess the
     repository against it. Base every judgement on files you open.
   - Do not create, change, or delete any file in the repository.
   - Write the assessment JSON, in the shape the rubric documents, to
     `agent-readiness.json` in the score directory, not the repository root.
     Give every criterion two more fields: `applicableAppNames`, the apps it
     applies to, and `passingAppNames`, the apps that satisfy it, each using
     names from `apps` and matching `applicableApps` and `passingApps`. The
     re-score checks each app it flips against them.
   - From the score directory as the working directory, run
     `node <this skill's directory>/scripts/build-report.mjs agent-readiness.json`
     (use `bun` if `node` is unavailable). The script writes
     `agent-readiness-report.md` next to the JSON and refuses any output path
     outside its working directory.
   - Reply with only the summary line the script printed.
4. When the subagent returns, this session continues with the step that sent
   you here. Keep the summary line, the score directory's path, and the
   baseline commit. Leave `agent-readiness.json` in the score directory; the
   re-score reuses it.
5. Check that `git status` shows what it showed before the subagent ran. If it
   does not, tell the user which files changed and leave them for the user.

## Re-score (step 8)

Two scoring runs judge the same repository differently, by more than a branch
moves the score. So the re-score does not assess the repository again. It
re-judges only the criteria whose evidence the branch changed and keeps the
baseline result for every other criterion.

1. Write the files the branch changed, one path per line, to
   `changed-files.txt` in the score directory, with
   `git diff --name-only <branch start> HEAD`, using the branch start that
   [Start the branch](apply.md#start-the-branch-step-6) recorded. When the
   list is empty, the score is unchanged; show the baseline summary line and
   stop here.
2. When the branch start differs from the baseline commit, the default branch
   moved during the session. List those files with
   `git diff --name-only <baseline commit> <branch start>`, and tell the user
   in step 4 that the baseline was scored before them and the re-score does
   not judge them.
3. Start a subagent with the repository as its working directory and give it
   these instructions:
   - Read [the readiness rubric](readiness-rubric.md), the baseline
     assessment `agent-readiness.json` in the score directory, and
     `changed-files.txt`.
   - Pick the criteria whose evidence is in a changed file: a criterion the
     file could make pass or fail for an app. Leave every other criterion
     alone, even when you would judge it differently today.
   - For each picked criterion, start from the baseline rationale and its
     `applicableAppNames` and `passingAppNames`. List only the apps whose
     result a changed file flips, from failing to passing or back, following
     the rubric, and base every judgement on files you open. Every other app
     keeps its baseline result.
   - Do not create, change, or delete any file in the repository.
   - Write `rejudged.json` to the score directory, in this shape, with one
     entry per picked criterion and no entry for the rest:

     ```json
     {
       "criteria": [
         {
           "category": "Documentation",
           "name": "Agent Context File",
           "rationale": "All three apps now have an AGENTS.md with their commands.",
           "evidence": ["apps/cli/AGENTS.md"],
           "changes": [
             {
               "app": "apps/cli",
               "passes": true,
               "evidence": ["apps/cli/AGENTS.md"]
             }
           ]
         }
       ]
     }
     ```

     `category` and `name` match the baseline exactly. `rationale` describes
     the criterion after the branch. Each `app` is a name from the baseline's
     `apps` list; do not discover the apps again. `passes` is the app's new
     result, the opposite of its baseline result. `changes` is empty when no
     app flips. Each `evidence` lists the
     changed files the judgement rests on, each exactly as
     `changed-files.txt` writes it. A criterion still failing after the
     changes needs an `action`, as the rubric says.

   - From the score directory as the working directory, run
     `node <this skill's directory>/scripts/rescore.mjs` (use `bun` if `node`
     is unavailable). It applies the listed flips to each re-judged criterion's
     baseline passing apps, keeps its applicable apps, writes
     `agent-readiness-after.json` and `agent-readiness-after-report.md`, and
     refuses an app the criterion does not apply to, a flip to the result the
     app already had, or a file outside `changed-files.txt`. When it
     refuses, fix `rejudged.json` and run it again.
   - Reply with only the lines the script printed.

4. Show the user those lines: the score before and after, and each re-judged
   criterion's coverage before and after. Name any re-judged criterion that
   did not move. When the script lists changed files no re-judged criterion
   cites, name them and say which criterion, if any, they should have moved.
   Add the upstream files from item 2, when there are any.
5. Check that `git status` is clean.
