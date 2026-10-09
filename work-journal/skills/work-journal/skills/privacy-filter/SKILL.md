---
name: privacy-filter
description: Use when the work-journal app asks which transcript utterances of an hour are bystander or personal non-work conversations to remove, and which spoken identifiers to redact.
---

# privacy-filter

Headless skill for the work-journal app. The prompt names one input file. The app records audio around the user's laptop; this skill keeps other people's conversations and the user's private life out of the work journal.

## Rules

- Read only the input file named in the prompt. Read nothing else and run no commands.
- Reply with ONLY one JSON object matching the schema below. No prose, no code fences.
- Never invent utterances, people, or indexes that the input does not contain.
- Empty input gives `{"drop":[],"redact":[]}`.

## Input

JSON object with exactly these fields:
- `day` (string, YYYY-MM-DD) and `hour` (integer, 0-23).
- `meetings`: array of `{id, app, start, end, participants}` (`start`/`end` are `HH:MM:SS`). `participants` are the speaker labels heard in the meeting and names stored for it. `app` is the meeting app (e.g. `zoom.us`), or `In person` for a conversation detected only from voices near the laptop.
- `known_people`: names of people whose voices the user has enrolled (coworkers met in meetings).
- `user_voice_enrolled`: whether the user's own voice is enrolled. If false, the user's speech inside an `In person` meeting is labelled `Speaker N` like everyone else's.
- `utterances`: array of `{index, time, track, speaker, text, context}` in time order. `time` is `HH:MM:SS`. `track` is `mic` (the laptop microphone: the user and anyone nearby) or `system` (audio the computer played: calls, videos). `You` is the user. `others`, `Speaker N` and `Speaker ?` are unidentified voices; a name is a known voice. `context: true` lines were judged earlier and are given so you can follow the conversation. Never put a context line in `redact`. A `drop` range may include context lines when they belong to a conversation that the new lines now show is removable (e.g. a chat that started ambiguously); do not drop context lines for any other reason.

## Rules specific to this skill

Decide per conversation, then list the utterance index ranges to remove.

Remove (`drop`):
- `bystander`: a conversation among people that does not include the user (`You` neither speaks in it nor is addressed by it) and is not part of a call or meeting. Typical: two unknown voices on the mic chatting about their weekend while the user is silent. In an `In person` meeting, judge the same way: it is a real meeting only if the user takes part (or, when `user_voice_enrolled` is false, if nothing shows the user is not one of the voices).
- `personal_non_work`: the user's own conversation with clearly non-work people about non-work matters: family, friends, partners, personal life (e.g. a call with a parent, dinner plans, a doctor's appointment). Both must hold: the other person is clearly not a coworker, and the topic is personal. This includes a whole call or meeting (any `app`, e.g. FaceTime) that is clearly a personal call with family or friends: drop both its `mic` and `system` lines.

Keep (never drop):
- Any conversation with coworkers, including personal chat with coworkers (weekend talk in a standup, coffee chat with a teammate) and off-topic work chat.
- System-track speech during a call or meeting (any `app` other than `In person`): it is work, unless the whole call is clearly a personal call with non-coworkers about personal matters (see `personal_non_work`).
- People in `known_people` or a meeting's `participants` who are named there are coworkers.
- Single remarks, dictation, and anything about work, tools, customers, code or the company.
- When unsure, keep. Only drop what the text clearly supports.

Ranges and confidence:
- `from` and `to` are utterance indexes, inclusive, of consecutive utterances of one dropped conversation; `reason` is `bystander` or `personal_non_work`.
- `confidence` 0-1: 0.9+ when the text plainly shows it (e.g. "love you, mom"); 0.5-0.7 when it is a judgement call. Do not drop below 0.5; keep instead.

Redact (`redact`), for every non-context utterance you keep:
- When it contains a sensitive identifier: a government ID or passport number, a bank account, routing, IBAN or card number, a card security code, a password, PIN or passcode, or a secret key or token. Return `{index, text}` where `text` is the utterance's text unchanged except each identifier replaced by `[redacted]`.
- Do not redact names, dates, amounts, times, ticket or PR numbers, order numbers, version numbers or phone extensions. Leave utterances without identifiers out of `redact`.

## Output schema

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "privacy-filter output",
  "type": "object",
  "additionalProperties": false,
  "required": [
    "drop",
    "redact"
  ],
  "properties": {
    "drop": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": [
          "from",
          "to",
          "reason",
          "confidence"
        ],
        "properties": {
          "from": {
            "type": "integer",
            "minimum": 0
          },
          "to": {
            "type": "integer",
            "minimum": 0
          },
          "reason": {
            "type": "string",
            "enum": [
              "bystander",
              "personal_non_work"
            ]
          },
          "confidence": {
            "type": "number",
            "minimum": 0,
            "maximum": 1
          }
        }
      }
    },
    "redact": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": [
          "index",
          "text"
        ],
        "properties": {
          "index": {
            "type": "integer",
            "minimum": 0
          },
          "text": {
            "type": "string"
          }
        }
      }
    }
  }
}
```
