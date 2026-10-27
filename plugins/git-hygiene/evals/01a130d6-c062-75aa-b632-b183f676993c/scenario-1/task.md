# Add change-driven screen capture settings to the Settings page

The recorder recently switched from taking screenshots on a fixed timer to a smarter, event-driven approach — capturing when you switch apps, when the screen visibly changes, and at a configurable backstop frequency. This is already wired into the recorder binary itself, but the configuration fields aren't exposed in the app yet.

Right now there's no way to tune this behavior through the Settings page. Users who want to adjust how aggressively screenshots are taken (or disable app-switch captures entirely) have to edit the TOML config file by hand, which most users won't do.

## Expected Behavior

The Settings page should have a new "Screen capture" section where users can configure:

- How often screenshots are taken at minimum and maximum (a minimum gap to avoid bursts, and an idle backstop so something is always captured even when nothing changes)
- How sensitive the screen-change detection is (small edits vs. big visual changes)
- Whether switching to a different app or window triggers an immediate screenshot

Changes to these settings should take effect after restarting the recorder (same as exclusions). The existing config fields and their defaults should apply when users haven't set anything. Older config files that don't have these fields should continue to load without errors.

## Acceptance Criteria

- The Settings page renders current values for all screen capture fields
- Saving updated values persists them to the config file
- Invalid values (out-of-range numbers, unrecognized sensitivity values) are rejected with a clear error
- Restarting the recorder applies the saved screen capture settings

## When you're done

Stage your changes with `git add`. Then write the proposed commit message for the staged changes to `COMMIT_MSG.txt` at the repository root (do not stage that file). Do not run `git commit`.
