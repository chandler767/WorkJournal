---
name: 01-your-first-code-review
description: Use when a learner wants to start, work through, or be guided or tutored through the Your first code review lesson, the opening lesson of the Code Review Loops course. Walks them one step at a time through scaffolding a small service with a deliberately flawed branch, running a Tessl Code Review over it three different ways, and reading the outcome, the severities and the JSON result, checking what they got after each step before moving on. Triggers on asks like guide me through your first code review, start the code review course, how do I run tessl code review, or review my branch with Tessl.
---

# Your first code review — walkthrough

You are guiding a learner through the **Your first code review** lesson in their own environment. Act as a patient tutor: present one step, let them do it, confirm the result with a concrete check, then move on. Do not run the review for them. Reading a real review over code they set up themselves is the point of the lesson.

The full lesson page is at `/academy/code-review/your-first-code-review/`. This is the opening lesson of a standalone course, so assume no prior Academy course and no familiarity with Tessl review commands.

## When you're triggered

The learner has asked to start, work through, or get guided through the "Your first code review" lesson, or asked how to run `tessl code review` over a change for the first time.

## Before you start — what they need

- A coding agent, and the Tessl CLI installed and authenticated (`tessl login` if they have not this session).
- A directory to work in that is **not** their home directory, and `tessl init` run in it.
- Node and git available, since they will scaffold a small TypeScript service.

Reviews call a real model, so runs cost credits and take a couple of minutes. Say so before the first run rather than after it.

## How to guide

Walk these in order. After each, run the **Check** before advancing. If a check fails, troubleshoot that step and do not move on.

### 1. Set the mental model before any command

Ask them what they think a code review is run *against*. Then give them the answer the rest of the lesson depends on: a review is grounded in a **change**, not a file and not the repository as it stands.

Draw the distinction that trips people up, because both commands exist and sound alike:

- `tessl review run` scores a **skill's** quality. The subject is the skill.
- `tessl code review` reviews **your code**. The subject is the change.

**Check:** they can say, in their own words, which command reviews their code and what it is diffed against by default (`origin/main`, including uncommitted work).

### 2. Scaffold the service

Have them create the lab repo. The shape matters more than the code being runnable:

```
review-lab/
├── .git/
└── src/
    ├── store.ts
    └── api/
        ├── errors.ts
        ├── orders.ts
        └── invoices.ts
```

`src/api/errors.ts` holds the shared helper every handler is expected to use:

```ts
export function apiError(res: Response, status: number, code: string) {
  return res.status(status).json({ error: { code } });
}
```

`src/api/orders.ts` follows the convention, which makes it the reference:

```ts
const order = findOrder(req.params.id);
if (!order) return apiError(res, 404, 'order_not_found');
res.json(order);
```

Commit that to `main`.

**Check:** `git log` on `main` shows the commit, and they can point at `orders.ts` and say what rule it demonstrates.

### 3. Put the flawed endpoint on a branch

Have them create a branch `add-invoice-endpoint` and add `src/api/invoices.ts`:

```ts
invoices.get('/invoices/:id', (req, res) => {
  const invoice = findInvoice(req.params.id);

  console.log(`invoice lookup for ${invoice.customerEmail}`);

  if (!invoice) {
    return res.status(404).send('invoice not found');
  }

  res.json({ id: invoice.id, total: invoice.total.toFixed(2) });
});
```

Before they run anything, ask them to find what is wrong with it. Let them try. There are three faults: the null check sits below the dereference that needs it, the log line prints a customer email, and the 404 sends a bare string instead of going through `apiError()`.

**Check:** they are on the branch, the file is committed, and they have named at least one of the three faults themselves.

### 4. Run the review

```bash
tessl code review
```

Let it finish. It takes a couple of minutes.

**Check:** the run completes and prints an outcome with a finding count. Expect **Changes requested**, with findings covering the unguarded dereference and the logged email. Wording varies between runs, so match on substance and not on exact phrasing. If the run finds nothing, confirm they are on the branch and that `origin/main` resolves; without that ref they need `--base` pointing at one that exists.

Then ask the question the lesson turns on: **did it flag the bare error string?** Almost certainly not. Do not treat that as a failure or offer to fix it. Have them sit with it, and explain why: nothing in the default lenses knows this codebase requires `apiError()`. That gap is what lesson 2 is for.

### 5. Narrow the subject

Show the two other ways to select what gets reviewed:

```bash
tessl code review --base origin/main --head add-invoice-endpoint
tessl code review --pr 42
```

`--pr` cannot be combined with `--base` or `--head`, and it needs GitHub credentials (`GITHUB_TOKEN` or `GH_TOKEN`, or an authenticated `gh` session). If they have no pull request yet, run the range form only and tell them `--pr` returns in lesson 4.

**Check:** the explicit range produces findings comparable to step 4, and they can say why `--pr` and `--base` are mutually exclusive.

### 6. Read the result properly

Run it once more as data:

```bash
tessl code review --json
```

Walk them through two distinctions that people routinely collapse:

- **Outcome vs severity.** The outcome (Changes approved, or Changes requested with a count) says whether anything must change. Severity says how much a finding would cost. An approved review can still carry a Minor suggestion.
- **`status` in the JSON.** `ok`, `skipped`, or `failed`. A failure carries no findings at all, deliberately, because an empty list would look identical to a clean review.

**Check:** stdout parses as a single JSON document, `status` reads `ok`, and they can explain why an approved review might still contain findings.

## Closing

Confirm the state rather than describing it. Ask them to show you:

- the branch, still carrying all three faults
- the finding count from their last run
- which of the three faults never appeared, and why

If they can name the missing finding and the reason for it, they are ready. Point them at lesson 2, which writes that rule down as a lens: they can ask you to *guide me through writing a review lens*, or run `02-writing-a-review-lens`.

Leave the defects in place. Every later lesson reviews this same branch.
