# work-journal skills

Five skills the work-journal Go app runs headlessly with `claude -p`. The app writes an input JSON file, names it in the prompt, and expects a single JSON object back. Output schemas live in `work-journal/internal/skills/schemas/<name>.json`; each SKILL.md embeds its schema inline.

| Skill | Input | Output |
|---|---|---|
| summarize-hour | hour doc (segments, meetings, transcript, OCR) | narrative, timeline, notes, main_focus, action_items |
| categorize-time | categories, hour summary, segments | category and confidence per segment |
| summarize-meeting | meeting, transcript, screens | participants, summary, decisions, action_items |
| close-day | day's hours, meetings, totals | markdown daily journal |
| weekly-rollup | week's daily markdown, totals, prior totals | markdown weekly rollup |

Fixtures in `skills/<name>/fixtures/` are sample inputs. Install with `tessl install file:./work-journal/skills/work-journal`.
