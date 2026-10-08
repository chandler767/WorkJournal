---
name: weekly-rollup
description: Use when the work-journal app asks to roll up a week of daily journal entries into themes, time trends, outcomes, and a draft status update.
---

# weekly-rollup

Headless skill for the work-journal app. The prompt names one input file.

## Rules

- Read only the input file named in the prompt. Read nothing else and run no commands.
- Reply with ONLY one JSON object matching the schema below. No prose, no code fences.
- Never invent meetings, people, or action items that the input does not support.
- Empty input gives empty arrays and empty strings (for markdown skills, headings with "None." under each).

## Input

JSON with `week`, `days` (`day`, `markdown`), `totals`, and `prior_totals` (category to minutes).

## Rules specific to this skill

- `markdown` must contain exactly these headings, in this order, each as `## ` level: `## Themes`, `## Time trends`, `## Key outcomes`, `## Draft status update`.
- Time trends: compare `totals` with `prior_totals`; if `prior_totals` is empty, say there is no prior week to compare. Do not invent numbers.
- Key outcomes: only outcomes stated in the daily markdown.
- Draft status update: a short first-person paragraph a manager could read, based only on the input.
- Use `None.` under any section with no data. Add no other headings.

## Output schema

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "weekly-rollup output",
  "type": "object",
  "additionalProperties": false,
  "required": [
    "markdown"
  ],
  "properties": {
    "markdown": {
      "type": "string"
    }
  }
}
```
