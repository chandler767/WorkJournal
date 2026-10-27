---
name: commit-conventions
description: |
  Use when the user asks for a commit message or wants help summarizing staged changes, e.g. "write a commit" or "git commit message". Reads the staged diff, picks the Conventional Commits type (feat, fix, refactor, docs, test, perf, chore), and writes an imperative subject of at most 72 characters, adding a body only for breaking changes, migrations or security fixes.
---

# commit-conventions

You suggest commit messages that follow the Conventional Commits specification.

## When you're triggered

A change is staged without a commit message, and the user wants one.

## What to do

1. Read only the staged changes with `git diff --cached`. Ignore unstaged edits.
2. Pick the type from the table below that describes the change as a whole. If changes are mixed, pick the type that covers all of them, not the largest file.
3. Write the message as `<type>(<optional scope>): <subject>`, following the subject line rules.
4. Add a short body only for something a future reader must know: a breaking change, a migration, a security fix.
5. Do not commit, just suggest it to the user.

## Type table

| Type | When |
|---|---|
| `feat` | Adds behavior a user or caller can now use. Empty files and stubs don't count. |
| `fix` | Corrects behavior that was wrong before this change. |
| `refactor` | Restructures code with no change in behavior. |
| `docs` | Changes documentation only. |
| `test` | Adds or changes tests only. |
| `perf` | Makes existing behavior measurably faster or lighter. |
| `chore` | Tooling, dependencies, build, CI or scaffolding: nothing a user would notice. |

## Subject line rules

- Imperative mood: "add", not "added" or "adds".
- Lowercase first letter, no trailing period.
- 72 characters at most, including the type prefix.
- Add a scope only when the change sits in one clear area, such as `feat(auth):`.
