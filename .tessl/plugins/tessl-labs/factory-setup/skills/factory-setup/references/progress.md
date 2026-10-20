# Progress timeline

The timeline tells the user what they have done and what is left. Use these
stages, in this order:

| Stage                | Steps | What it does, for the "to do" line                     |
| -------------------- | ----- | ------------------------------------------------------ |
| Agree the plan       | 1     | explain the factory and agree to go ahead              |
| Check your setup     | 2     | check the CLI, login, project, credits, and GitHub App |
| Learn how you work   | 3, 4  | read how you ship work and what is already set up      |
| Choose an automation | 5     | pick the next job to hand to Tessl                     |
| Build it             | 6, 7  | create the cloud environment and write `tessl.json`    |
| Go live and test     | 8     | push the change and watch a real run                   |
| Recap                | 9     | what is on, how to stop it, and what to add next       |

Give each stage one line that starts with its status mark:

- `✓` done: what you did and what the user decided, in one short sentence.
  Name the real things you created or found: the workspace, the environment,
  the branch, the pull request, the ticket. Link pull requests and issues.
- `●` now: the stage you are starting.
- `◐` waiting on you: the user has to do something outside this session, such
  as install the GitHub App or run `tessl login`. Say what.
- `○` to do: the text from the table.
- `⊘` skipped: say why in a few words, for example "you already have a
  factory".

Use these marks, not emoji. Show the timeline as a list under the heading
"Your factory setup", for example:

```
Your factory setup

✓ Agree the plan: you said go ahead.
✓ Check your setup: linked to workspace acme; the GitHub App can write to acme/web.
✓ Learn how you work: you use pull requests and GitHub issues; CI runs the tests.
✓ Choose an automation: ship tickets, with answer reviews and fix checks.
● Build it
○ Go live and test: push the change and watch a real run.
○ Recap: what is on, how to stop it, and what to add next.
```

Keep it short. Do not repeat details the user has just seen, such as the full
diff. Never put a token or other secret in it.

When you stop before setup is finished, show the timeline with the stage you
stopped at marked `◐` waiting on you or `●` now, and say what the user needs to
do to carry on. If you go back to an earlier stage, for example from step 4 to
step 1 because nothing is set up yet, mark that stage `●` now again.

## Adding another automation

When the user adds an automation to a factory that already has one, head the
timeline "Adding an automation", and name it once it is chosen, for example
"Adding review pull requests". Mark "Agree the plan" skipped only when step 1
was skipped; if the user agreed in step 1, keep it done. When you come back
from step 9, mark "Check your setup" done only if the step 2 recheck passes.
If it finds uncommitted changes, mark it `◐` and say to commit them or put
them aside. Mark "Learn how you work" done, with "done earlier in this
session". Show what was set up before as one line above the list, for example
"Already on: ship tickets, answer reviews and fix checks."
