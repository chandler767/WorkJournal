---
name: close-day
description: Use when the work-journal app asks to write the end-of-day journal entry from the day's hour summaries, meetings, and category totals.
---

# close-day

Headless skill for the work-journal app. The prompt names one input file.

## Rules

- Read only the input file named in the prompt. Read nothing else and run no commands.
- The input has one record per line. If the file is long, read it in parts (offset/limit) until you have read all of it, then answer.
- Reply with ONLY one JSON object matching the schema below. No prose, no code fences.
- Never invent meetings, people, or action items that the input does not support.
- Empty input gives empty arrays and empty strings (for markdown skills, headings with "None." under each).

## Input

JSON with `day`, `hours`, `meetings`, and `totals` (category to minutes).
- Each hour is `{hour, narrative_lines, notes, main_focus, action_items}`. `narrative_lines` is a short account of the hour, split into pieces to join with spaces; it may be absent or `[]` for older hours. `notes` carry the specifics.
- Each meeting is `{id, title, start, end, participants, summary_lines, decisions, action_items}`; join `summary_lines` with spaces to get its summary.
- Prefer the concrete details in the narrative and `notes` (files, tickets, people, decisions) over generic phrasing when writing the Summary and Timeline.

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
