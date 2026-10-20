# WorkJournal

This repository holds the Work Journal app, a local work journal for macOS that lives in
`work-journal/`, plus the Tessl plugins and skills it uses.

To build and run the app, see [work-journal/README.md](work-journal/README.md).

## Layout

| Folder | What it holds |
| --- | --- |
| [`work-journal/`](work-journal/) | The app: the Go `journal` binary, the Swift recorder, docs, and the Claude Code skills that write the summaries (`work-journal/skills/`). |
| [`plugins/`](plugins/) | Tessl plugins developed here. `git-hygiene` bundles the commit-conventions skill, its rules and its evals. |
| [`skills/`](skills/) | Standalone Tessl skills, currently `commit-conventions`. |
| [`evals-subset/`](evals-subset/) | A subset of the `git-hygiene` eval scenarios, for running a smaller task eval. |

`tessl.json` lists the Tessl plugins installed in this repository.
