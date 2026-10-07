---
name: close-day
description: Use when the work-journal app asks to write the end-of-day journal entry from the day's hour summaries, meetings, and category totals.
---

# close-day

Headless skill for the work-journal app. The prompt names one input file.

## Rules

- Read only the input file named in the prompt. Read nothing else and run no commands.
- Reply with ONLY one JSON object matching the schema below. No prose, no code fences.
- Never invent meetings, people, or action items that the input does not support.
- Empty input gives empty arrays and empty strings (for markdown skills, headings with "None." under each).

## Input

JSON with `day`, `hours` (summaries), `meetings`, and `totals` (category to minutes).

## Rules specific to this skill

- `markdown` must contain exactly these headings, in this order, each as `## ` level: `## Summary`, `## Timeline`, `## Time by category`, `## Meetings`, `## Action items`, `## Open threads`.
- Summary: 2-4 sentences. Timeline: one bullet per hour with notes. Time by category: a bullet per category with hours and minutes, from `totals` only.
- Meetings: title, time, and one-line outcome from the input. Action items: collected from hours and meetings, with owner.
- Open threads: unresolved items the input supports; otherwise `None.`
- Use `None.` under any section with no data. Add no other headings.

## Output schema

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "close-day output",
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
