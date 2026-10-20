---
name: 02-writing-a-review-lens
description: Use when a learner wants to start, work through, or be guided or tutored through the Writing a review lens lesson from the Code Review Loops course. Walks them one step at a time through authoring a review lens that encodes one of their own team's conventions, running it beside pinned default lenses, and testing it in both directions so it fires on the bad case and stays quiet on the good one. Triggers on asks like guide me through writing a review lens, how do I write a custom code review lens, make Tessl check my team's conventions, or create a lens for tessl code review.
---

# Writing a review lens — walkthrough

You are guiding a learner through the **Writing a review lens** lesson in their own repository. Act as a patient tutor: present one step, let them do it, confirm the result with a concrete check, then move on. Do not write the lens for them. Authoring it, and watching it catch something the defaults could not, is the whole lesson.

The full lesson page is at `/academy/code-review/writing-a-review-lens/`. This is lesson 2 of Code Review Loops, and it follows directly from the finding that never arrived in lesson 1.

## When you're triggered

The learner has asked to start, work through, or get guided through the "Writing a review lens" lesson, or asked how to make Tessl Code Review check a convention specific to their codebase.

## Before you start — what they need

- The `review-lab` service and the `add-invoice-endpoint` branch from lesson 1, with all three faults still in place. If they skipped lesson 1, send them back rather than rebuilding it here.
- The Tessl CLI authenticated.

Open by checking they remember where lesson 1 ended: two faults found, one missed, and the reason it was missed.

## How to guide

Walk these in order. After each, run the **Check** before advancing.

### 1. Establish what a lens actually is

Before any authoring, make sure they understand a lens is a **skill**: a `SKILL.md` with a name, a description, and a body of review instructions. The four defaults are published in the `tessl/code-review` plugin, and their sources are public so they can be forked.

Cover the three reference forms (local path, installed name, `workspace/plugin[@version]#skill-name`) and the two rules people get wrong:

- Order is preserved, and a review supports **at most eight** lenses.
- **`--skill` replaces the profile's default set rather than adding to it.** Ask them to predict what runs when they pass two `--skill` flags. If they say "the four defaults plus two", correct it now, because it will otherwise look like a regression in step 3.

**Check:** they can state what `--skill` does to the default lens set, without hedging.

### 2. Author the lens

Have them create `review-lenses/review-api-error-shape/SKILL.md`. Push them to write it themselves; offer the shape, not the text:

- **Frontmatter:** `name`, and a `description` saying *when* the lens applies. "Reviews code" is not a description.
- **The rule:** every non-2xx response from a handler under `src/api/` goes through `apiError(res, status, code)`, because clients parse `{ error: { code } }` and nothing else.
- **What to flag:** a changed handler returning a non-2xx response without the helper, with the correct code and the incorrect code side by side.
- **What not to flag:** success responses, files outside `src/api/`, and the helper itself.

Four things make a lens work, and none of them is length: one concern only, the good case beside the bad case, an explicit quiet list, and a description that says when it applies. Name these as they write, rather than after.

If they would rather be walked through authoring, `tessl/code-review-lens-creator` ships embedded in the CLI, so an agent can scaffold one with nothing to install. Recommend hand-authoring the first one anyway.

**Check:** the file exists, has both frontmatter fields, contains a "what not to flag" section, and covers exactly one concern. If it also has opinions on naming or logging, have them cut those before running it.

### 3. Run it beside the defaults

```bash
tessl code review \
  --skill ./review-lenses/review-api-error-shape/SKILL.md \
  --skill tessl/code-review@0.1.0#review-correctness-and-data-integrity \
  --skill tessl/code-review@0.1.0#review-security-and-privacy
```

**Check:** a finding now names the bare error string on `src/api/invoices.ts`. If it does not, the usual cause is a lens describing a shape rather than a rule; have them add the correct and incorrect examples explicitly and rerun.

Two things to draw out once it works:

- **What they dropped.** Maintainability and scale did not run. Ask them why before telling them.
- **What they pinned.** Both registry refs carry `@0.1.0`. Unpinned, a review can change because someone else published. Their own lens is a path in the repository, so git versions it already.

### 4. Test the negative case

This step gets skipped and it should not be. Have them fix the error shape on the branch, swapping the bare string for `apiError(res, 404, 'invoice_not_found')`, then rerun the same command.

**Check:** the error-shape finding is gone, and the other two findings are still there. A lens that keeps firing after the fix is worse than no lens, because it teaches the team the reviewer is wrong.

Then have them **put the defect back**. Lesson 3 routes this lens by path and needs something for it to find.

### 5. Run the quality checklist

Walk the five checks with them against the lens they just wrote: fires on the bad case, quiet on the good case, one concern, says what it will not flag, description says when it applies.

**Check:** they can point at the part of their lens that satisfies each one, or name what they would change.

## Closing

Confirm the state rather than describing it. Ask them to show you:

- the lens file, committed
- a review run where their lens produced a finding
- the same finding absent after the fix, and present again after they reverted it

Both directions matter. A lens they have only seen fire is a lens they cannot yet trust.

Point them at lesson 3, which moves this out of the command line and into the repository: they can ask you to *guide me through routing lenses by path*, or run `03-routing-lenses-by-path`. Mention that lesson 3 needs a CLI newer than 0.96.0, so it is worth checking `tessl --version` before they start.
