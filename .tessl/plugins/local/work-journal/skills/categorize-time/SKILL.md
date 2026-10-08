---
name: categorize-time
description: Use when the work-journal app asks to assign a category and confidence to each activity segment of an hour.
---

# categorize-time

Headless skill for the work-journal app. The prompt names one input file.

## Rules

- Read only the input file named in the prompt. Read nothing else and run no commands.
- Reply with ONLY one JSON object matching the schema below. No prose, no code fences.
- Never invent meetings, people, or action items that the input does not support.
- Empty input gives empty arrays and empty strings (for markdown skills, headings with "None." under each).

## Input

JSON with `categories` (allowed names), `hour_summary`, and `segments` (`index`, `app`, `title`, `minutes`).

## Rules specific to this skill

- Use only names from `categories`, spelled exactly. Never invent a category.
- Idle, lock screen, or no meaningful app (for example `loginwindow`) maps to `Break/Idle`.
- Return one entry per input segment, keeping its `index`.
- Use `confidence` below 0.6 when unsure; use the hour summary as context only.
- No segments gives `{"segments":[]}`.

## Output schema

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "categorize-time output",
  "type": "object",
  "additionalProperties": false,
  "required": [
    "segments"
  ],
  "properties": {
    "segments": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": [
          "index",
          "category",
          "confidence"
        ],
        "properties": {
          "index": {
            "type": "integer",
            "minimum": 0
          },
          "category": {
            "type": "string"
          },
          "confidence": {
            "type": "number",
            "minimum": 0,
            "maximum": 1
          }
        }
      }
    }
  }
}
```
