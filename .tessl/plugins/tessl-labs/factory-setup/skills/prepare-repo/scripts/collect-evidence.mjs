#!/usr/bin/env node
// Collects the evidence prepare-repo mines for rules into one evidence.json:
// review feedback on merged pull requests, bug-fix and revert pull requests,
// reverts, fixes, churn and co-change hotspots from git, CI failures on pull
// requests, the agent context inventory, and open Tessl findings.
//
// Every source is optional. A source whose tool is missing, unauthenticated or
// failing records `status: "skipped"` or `"failed"` with a reason, and the rest
// still run. Every source is bounded by the limits below.
//
// Usage: node collect-evidence.mjs [--root DIR] [--out DIR] [--days N]
//          [--repo OWNER/NAME] [--max-prs N] [--max-comments N]
//          [--max-comments-per-pr N] [--max-pages-per-pr N] [--max-runs N]
//          [--max-commits N] [--max-files N] [--max-findings N]
// Prints the path of the evidence.json it wrote and one line per source.

import { spawnSync } from 'node:child_process';
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  realpathSync,
  statSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import {
  basename,
  dirname,
  isAbsolute,
  join,
  relative,
  resolve,
} from 'node:path';
import { fileURLToPath } from 'node:url';

import { failed, sinceDate } from './lib/run.mjs';
import { collectContext } from './sources/context.mjs';
import { collectFindings } from './sources/findings.mjs';
import { collectGit } from './sources/git.mjs';
import { collectGithub } from './sources/github.mjs';

export const DEFAULTS = {
  days: 90,
  maxPrs: 40,
  maxComments: 600,
  maxCommentsPerPr: 40,
  maxPagesPerPr: 6,
  maxRuns: 100,
  maxCommits: 2000,
  maxFiles: 60,
  maxFindings: 50,
};

const LIMIT_FLAGS = {
  '--max-prs': 'maxPrs',
  '--max-comments': 'maxComments',
  '--max-comments-per-pr': 'maxCommentsPerPr',
  '--max-pages-per-pr': 'maxPagesPerPr',
  '--max-runs': 'maxRuns',
  '--max-commits': 'maxCommits',
  '--max-files': 'maxFiles',
  '--max-findings': 'maxFindings',
};

function positiveInt(flag, value) {
  const n = Number(value);
  if (!Number.isInteger(n) || n < 1)
    throw new Error(`${flag} needs a positive whole number, got "${value}"`);
  return n;
}

/** Parse command-line flags. Throws on an unknown flag or a bad number. */
export function parseArgs(argv) {
  const opts = {
    root: process.cwd(),
    out: null,
    repo: null,
    days: DEFAULTS.days,
    limits: {},
  };
  for (const [key, value] of Object.entries(DEFAULTS))
    if (key !== 'days') opts.limits[key] = value;
  for (let i = 0; i < argv.length; i++) {
    const flag = argv[i];
    const value = argv[i + 1];
    if (flag === '-h' || flag === '--help') return { help: true };
    if (value === undefined) throw new Error(`${flag} needs a value`);
    i++;
    if (flag === '--root') opts.root = resolve(value);
    else if (flag === '--out') opts.out = resolve(value);
    else if (flag === '--repo') opts.repo = value;
    else if (flag === '--days') opts.days = positiveInt(flag, value);
    else if (LIMIT_FLAGS[flag])
      opts.limits[LIMIT_FLAGS[flag]] = positiveInt(flag, value);
    else throw new Error(`unknown flag ${flag}`);
  }
  // A missing root makes every command fail with ENOENT, which would read as
  // git, gh and tessl all being absent.
  if (!statSync(opts.root, { throwIfNoEntry: false })?.isDirectory())
    throw new Error(`--root must be an existing directory, got "${opts.root}"`);
  if (opts.out) {
    const top = worktreeTop(opts.root);
    if (isInside(top, opts.out))
      throw new Error(
        `--out must be outside the repository (${top}), got "${opts.out}"`,
      );
  }
  return opts;
}

// `--root` may be a subdirectory of a Git worktree, so the output check
// guards the whole worktree. Outside Git, the root itself is the repository.
function worktreeTop(root) {
  const git = spawnSync('git', ['rev-parse', '--show-toplevel'], {
    cwd: root,
    encoding: 'utf8',
  });
  const top = git.status === 0 ? git.stdout.trim() : '';
  return top ? resolve(top) : root;
}

function isInside(root, path) {
  const rel = relative(realDestination(root), realDestination(path));
  return rel === '' || (!rel.startsWith('..') && !isAbsolute(rel));
}

/**
 * Where a path really lands: its nearest existing ancestor with symlinks
 * resolved, plus the part that does not exist yet.
 */
function realDestination(path) {
  if (existsSync(path)) return realpathSync(path);
  const parent = dirname(path);
  if (parent === path) return path;
  return join(realDestination(parent), basename(path));
}

/** Collect every source. Never throws for a failing source. */
export async function collectEvidence(opts, now = new Date()) {
  const ctx = {
    root: opts.root,
    repoFlag: opts.repo,
    since: sinceDate(opts.days, now),
    limits: opts.limits,
  };
  const [github, git, contextFiles, findings] = (
    await Promise.allSettled([
      collectGithub(ctx),
      collectGit(ctx),
      collectContext(ctx),
      collectFindings(ctx),
    ])
  ).map((r) =>
    r.status === 'fulfilled'
      ? r.value
      : failed(`collector crashed: ${r.reason?.message ?? r.reason}`),
  );
  return {
    collectedAt: now.toISOString(),
    root: opts.root,
    repository: github.repo ?? null,
    window: { days: opts.days, since: ctx.since },
    limits: opts.limits,
    sources: {
      reviewFeedback: github.reviewFeedback ?? github,
      bugFixPrs: github.bugFixPrs ?? github,
      ciFailures: github.ciFailures ?? github,
      reverts: git.reverts ?? git,
      fixCommits: git.fixCommits ?? git,
      hotspots: git.hotspots ?? git,
      contextFiles,
      findings,
    },
  };
}

function sourceLine(name, source) {
  if (source.status !== 'ok')
    return `${name}: ${source.status} (${source.reason})`;
  const n =
    source.items?.length ??
    source.recent?.length ??
    source.files?.length ??
    source.churn?.length ??
    0;
  const failedPrs = source.failedPrs?.length
    ? `, ${source.failedPrs.length} of ${source.prsRead} pull requests failed`
    : '';
  return `${name}: ok, ${n} entries${failedPrs}${source.truncated ? ' (truncated at limit)' : ''}`;
}

async function main() {
  let opts;
  try {
    opts = parseArgs(process.argv.slice(2));
  } catch (error) {
    console.error(`collect-evidence: ${error.message}`);
    process.exit(2);
  }
  if (opts.help) {
    console.log(
      'Usage: node collect-evidence.mjs [--root DIR] [--out DIR] [--days N] [--repo OWNER/NAME] [--max-* N]',
    );
    return;
  }
  const outDir =
    opts.out ?? mkdtempSync(join(tmpdir(), 'prepare-repo-evidence-'));
  const evidence = await collectEvidence(opts);
  mkdirSync(outDir, { recursive: true });
  const outPath = join(outDir, 'evidence.json');
  writeFileSync(outPath, `${JSON.stringify(evidence, null, 2)}\n`);
  console.log(outPath);
  for (const [name, source] of Object.entries(evidence.sources))
    console.log(sourceLine(name, source));
}

if (
  process.argv[1] &&
  resolve(process.argv[1]) === fileURLToPath(import.meta.url)
)
  await main();
