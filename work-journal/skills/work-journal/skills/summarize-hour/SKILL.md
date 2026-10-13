---
name: summarize-hour
description: Use when the work-journal app asks to summarize one hour of recorded activity into a narrative, a timeline, detailed notes, a main focus, and action items.
---

# summarize-hour

Headless skill for the work-journal app. The prompt names one input file.

## Rules

- Read only the input file named in the prompt. Read nothing else and run no commands.
- The input has one record per line. If the file is long, read it in parts (offset/limit) until you have read all of it, then answer.
- Reply with ONLY one JSON object matching the schema below. No prose, no code fences.
- Never invent meetings, people, or action items that the input does not support.
- Empty input gives empty arrays and empty strings (for markdown skills, headings with "None." under each).

## Input

JSON object with exactly these fields:
- `day` (string, YYYY-MM-DD) and `hour` (integer, 0-23).
- `segments`: array of `{index, app, title, start, minutes, category}` (`start` is `HH:MM`). `category` may be `""` and is informational only; do not copy it into the output.
- `meetings`: array of `{id, start, end, app}` (`start`/`end` are `HH:MM`).
- `transcript`: array of `{speaker, text}`; `You` is the mic track.
- `ocr`: array of on-screen text lines.

## Rules specific to this skill

Be concrete. Name the specific files, documents, URLs and sites, repos, tickets and PR numbers, commands, people and decisions visible in the window titles, OCR text or transcript. Quote short distinctive phrases (error messages, headings, a decision as worded) where useful. Never invent anything the input does not contain. Prefer specifics over generic phrasing: "Edited Capture.swift to recheck exclusions before screenshots", not "Worked on code".

- `narrative`: 3-5 sentences telling what happened over the hour in order, with the specifics above. `""` for an empty hour.
- `timeline`: chronological array of `{start, end, app, activity}`. `start` and `end` are `HH:MM` and must lie within the hour (an `end` of the next hour's `:00` is allowed); `end` is never before `start`. Take times from the segments' `start` and `minutes`, and from meeting times. Merge consecutive related segments into one entry. `activity` is one specific sentence fragment. `[]` for an empty hour.
- `notes`: at most 12 specific factual bullets, most important first. Each should carry a concrete detail (file, page, ticket, command, person, decision), not a restatement of the app name.
- `main_focus`: one short phrase for what the hour was mostly about; `""` if nothing happened.
- `action_items`: only commitments explicitly stated in the transcript or on screen. `owner` is `"You"` for the mic track, otherwise the speaker label or name given; `""` if unknown.
- If segments, meetings, transcript and ocr are all empty, return `{"narrative":"","timeline":[],"notes":[],"main_focus":"","action_items":[]}`.

## Output schema

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "summarize-hour output",
  "type": "object",
  "additionalProperties": false,
  "required": [
    "narrative",
    "timeline",
    "notes",
    "main_focus",
    "action_items"
  ],
  "properties": {
    "narrative": {
      "type": "string"
    },
    "timeline": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": [
          "start",
          "end",
          "app",
          "activity"
        ],
        "properties": {
          "start": {
            "type": "string"
          },
          "end": {
            "type": "string"
          },
          "app": {
            "type": "string"
          },
          "activity": {
            "type": "string"
          }
        }
      }
    },
    "notes": {
      "type": "array",
      "maxItems": 12,
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
