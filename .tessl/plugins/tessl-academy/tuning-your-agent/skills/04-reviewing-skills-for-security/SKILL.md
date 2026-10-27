---
name: 04-reviewing-skills-for-security
description: Use when a learner wants to start, work through, or be guided or tutored through the Reviewing skills for security lesson from the testing and optimization course. Walks them one step at a time through publishing a commit-conventions plugin that carries a real vulnerability, reading the Snyk security finding the publish-time scan surfaces on the registry, then installing it from a clean repo to feel the critical/high gating prompt and decide what to do with it, checking the result after each step before moving on. Triggers on asks like guide me through reviewing skills for security, start the security review lesson, how does the Snyk security review work, or what happens when I install a skill with a vulnerability.
---

# Reviewing skills for security — walkthrough

You are guiding a learner through the **Reviewing skills for security** lesson in their own environment. Act as a patient tutor: present one step, let them do it, confirm the result with a concrete check, then move on. Do not run the steps for them — the point is they publish a real finding and feel the install prompt from both sides themselves.

The full lesson page is at `/academy/tuning/reviewing-skills-for-security/`. This lesson covers the half of skill quality the evals can't measure: not *is this skill good?* but *is this skill safe to install?* When a plugin is published, Tessl runs a Snyk security review server-side; the score lands on the registry and gates installs on critical or high findings. By the end the learner has published a plugin with a real vulnerability, read the finding, and made a clear-eyed install decision from the consumer side.

## When you're triggered

The learner has asked to start, work through, or be guided through the "Reviewing skills for security" lesson, or asked how the Snyk security review works / what happens when they install a skill with a vulnerability.

## Before you start — what they need

- The Tessl CLI installed and authenticated (`tessl login` if they haven't this session).
- A workspace they can publish to, and **credits available** — the security review costs 100 credits per publish, and they'll publish at least once. Make sure they're happy to spend that before starting.
- A fresh, separate repo (or directory) to install into later, so they meet the gating prompt as a consumer rather than re-using the author's workspace.

Confirm they can say *why* the lesson starts from a deliberately vulnerable plugin: a clean plugin produces a clean scan and teaches nothing — they need a real finding to watch land on the registry and gate an install.

## How to guide

Walk these in order. After each, run the **Check** before advancing. If a check fails, troubleshoot that step — do not move on.

### 1. Build the plugin with something to find

Have them assemble a `commit-conventions` plugin whose bundled helper pins a library with a known advisory. The shape:

```
commit-conventions/
├── .tessl-plugin/
│   └── plugin.json
└── skills/
    └── commit-conventions/
        ├── SKILL.md
        └── scripts/
            ├── package.json
            └── parse-diff.js
```

The manifest is ordinary:

```json
{
  "name": "my-workspace/commit-conventions",
  "version": "0.1.0",
  "description": "Proposes a Conventional Commits message from the staged diff.",
  "skills": "skills"
}
```

The vulnerable surface is the helper's `package.json`, pinning an old `minimist`:

```json
{
  "name": "parse-diff",
  "version": "1.0.0",
  "dependencies": {
    "minimist": "1.2.0"
  }
}
```

`[email protected]` has publicly known prototype-pollution advisories (fixed in later 1.2.x). Have them swap `my-workspace` for their own workspace.

**Check:** the directory matches the tree, the manifest names their workspace, and the helper's `package.json` pins `[email protected]`. Confirm they can point to the one line that makes this plugin unsafe.

### 2. Publish, and let the scan run

Have them publish from the plugin directory:

```bash
tessl plugin publish ./commit-conventions
```

Remind them this spends the 100 credits for the security review. Publishing lints the plugin, validates each skill, runs a review eval, **and** hands every skill to the Snyk scanner server-side. The scan finishes within a few minutes.

**Check:** the publish command completes and reports the published version (`my-workspace/[email protected]`). If it fails on lint or validation, fix that before worrying about the scan.

### 3. Read the security score

Have them pull the registry details, then open the registry page in a browser:

```bash
tessl plugin info my-workspace/commit-conventions
```

On the registry page, the plugin shows a **security score** beside its quality score, listing each finding with a severity. The `[email protected]` pin appears as a prototype-pollution finding, critical or high.

**Check:** they can name the finding and its severity, and trace it back to the `minimist` pin in their helper — not to a phantom. Point out the **Report Incorrect Findings** link: if a scanner is ever wrong about their code, that's how a false positive gets corrected.

### 4. Install it as a consumer — and decide

Have them switch to the fresh repo and install the version they just published:

```bash
tessl install my-workspace/commit-conventions
```

Because the version carries a critical or high finding, the install doesn't silently proceed — Tessl surfaces the warning and asks for permission first. Installation is **never blocked**; they have three honest choices, and you should make them choose deliberately:

- **Proceed**, having read the finding and accepted the risk.
- **Pin the exact scanned commit**, to install precisely what they reviewed:

  ```bash
  tessl install my-workspace/commit-conventions@<commit-sha>
  ```

- **Pre-accept the warning** for automation with no human to answer, e.g. CI:

  ```bash
  tessl install my-workspace/commit-conventions --accept-warnings
  ```

**Check:** the plain `tessl install` prompted for permission (it did not proceed silently), and they can explain when they'd reach for `--accept-warnings` versus pinning a commit. `--accept-warnings` behaves the same way on `tessl update`.

### 5. Verify the review end to end

Confirm all four hold:

- The registry page shows a security score with at least one finding and a severity.
- The finding traces to the real cause — the `[email protected]` pin.
- A clean `tessl install` of the published version **prompts** rather than proceeding silently.
- `tessl install --accept-warnings` installs the same plugin with no prompt.

**Check:** if the install never prompted, the usual causes are the finding scored below high, or the workspace has the security review disabled — have them confirm the registry page actually shows the finding before assuming the gate is broken.

## When they finish

Confirm they can state the takeaway: the security review makes risk *visible* on every published version, for every consumer, without blocking anyone — it informs the install decision rather than making it. Reinforce the two-sided model they just lived: the scan runs on the author's publish, and the prompt fires on the consumer's install. Then hand off to the final lesson without losing context: the next skill, **`05-continuous-review-in-ci`**, is already installed from this course plugin. Offer to start *Continuous review in CI* right now, pairing this security review with an automated eval gate so structure, behavior, and safety are all enforced on every change, by running that skill.
