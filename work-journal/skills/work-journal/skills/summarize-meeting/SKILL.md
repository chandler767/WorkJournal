---
name: summarize-meeting
description: Use when the work-journal app asks to summarize a recorded meeting transcript into participants, summary, decisions, and action items.
---

# summarize-meeting

Headless skill for the work-journal app. The prompt names one input file.

## Rules

- Read only the input file named in the prompt. Read nothing else and run no commands.
- The input has one record per line. If the file is long, read it in parts (offset/limit) until you have read all of it, then answer.
- Reply with ONLY one JSON object matching the schema below. No prose, no code fences.
- Never invent meetings, people, or action items that the input does not support.
- Empty input gives empty arrays and empty strings (for markdown skills, headings with "None." under each).

## Input

JSON with `meeting` (id, app, start, end, title), `transcript` (`t`, `speaker`, `text`), and `screens` (descriptions of on-screen content).
An imported recording (audio the user recorded on their phone) may also have `user_note`: `{title, context_lines}`, the user's own note about the recording; join `context_lines` with spaces.

## Rules specific to this skill

- The mic track speaker (`You`) is always the user; list it as `{"speaker":"You","name":"","confidence":1}` unless a name is given.
- Name a remote speaker only from transcript cues (for example "thanks, Priya") or on-screen participant labels. Otherwise `name` is `""`.
- `confidence` is 0-1; direct address or a clear label is high, a weak inference below 0.7. Do not guess from voice or role.
- List every distinct speaker label in `participants`.
- `summary`: 2-4 sentences. `decisions` and `action_items` only when stated; `owner` is `""` if unclear.
- `user_note` is written by the user, not said in the meeting. Use it for context: what the event was, who was there, what the user cares about. Let it shape the summary's focus, but never report as said or decided anything only the note states. A person the note names is not a speaker's name unless the transcript ties that name to that speaker: the speaker introduces themselves by it, or another speaker addresses them by it. A name a speaker says belongs to the person addressed, never to the one saying it (Speaker 1 saying "thanks, Maya" does not make Speaker 1 Maya), and a matching topic (the note says Maya demoed, a speaker talks about a demo) is not enough, so `name` stays `""`.
- Empty transcript gives `{"participants":[],"summary":"","decisions":[],"action_items":[]}`.

## Output schema

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "summarize-meeting output",
  "type": "object",
  "additionalProperties": false,
  "required": [
    "participants",
    "summary",
    "decisions",
    "action_items"
  ],
  "properties": {
    "participants": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": [
          "speaker",
          "name",
          "confidence"
        ],
        "properties": {
          "speaker": {
            "type": "string"
          },
          "name": {
            "type": "string"
          },
          "confidence": {
            "type": "number",
            "minimum": 0,
            "maximum": 1
          }
        }
      }
    },
    "summary": {
      "type": "string"
    },
    "decisions": {
      "type": "array",
      "items": {
        "type": "string"
      }
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
