---
name: summarize-hour
description: Use when the work-journal app asks to summarize one hour of recorded activity into notes, a main focus, and action items.
---

# summarize-hour

Headless skill for the work-journal app. The prompt names one input file.

## Rules

- Read only the input file named in the prompt. Read nothing else and run no commands.
- Reply with ONLY one JSON object matching the schema below. No prose, no code fences.
- Never invent meetings, people, or action items that the input does not support.
- Empty input gives empty arrays and empty strings (for markdown skills, headings with "None." under each).

## Input

JSON with `day`, `hour`, `segments` (app, title, minutes), `meetings`, `transcript` (speaker, text), and `ocr` (on-screen text lines).

## Rules specific to this skill

- `notes`: at most 6 short factual bullets, most important first.
- `main_focus`: one short phrase for what the hour was mostly about; `""` if nothing happened.
- `action_items`: only commitments explicitly stated in the transcript or on screen. `owner` is `"You"` for the mic track, otherwise the speaker label or name given; `""` if unknown.
- If segments, meetings, transcript and ocr are all empty, return `{"notes":[],"main_focus":"","action_items":[]}`.

## Output schema

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "summarize-hour output",
  "type": "object",
  "additionalProperties": false,
  "required": [
    "notes",
    "main_focus",
    "action_items"
  ],
  "properties": {
    "notes": {
      "type": "array",
      "maxItems": 6,
      "items": {
        "type": "string"
      }
    },
    "main_focus": {
      "type": "string"
    },
    "action_items": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": [
          "text",
          "owner"
        ],
        "properties": {
          "text": {
            "type": "string"
          },
          "owner": {
            "type": "string"
          }
        }
      }
    }
  }
}
```
