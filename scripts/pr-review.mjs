#!/usr/bin/env node
// PR review gate and renderer — the model-free halves of pr-review.yml.
//
// The workflow reviews pull requests the pipeline did not author
// (docs/PR_REVIEW_PASSES.md). The model job reads untrusted pull request
// content with no forge token; everything it writes passes through `render`
// here before the post job, which holds `pull-requests: write`, publishes it.
// The rules are closed: exact keys, bounded sizes, path and credential-shape
// checks, and a finding cap. Anything else is rejected and nothing is posted.
//
// Usage:
//   node scripts/pr-review.mjs gate --pr-json <file> --event <name>
//        [--pipeline-authors <csv>] [--has-key true|false]
//   node scripts/pr-review.mjs validate <findings.json>
//   node scripts/pr-review.mjs render --author <login> --reviewer <login> <findings.json>
//
// `gate` prints `run=` and `reason=` lines (to $GITHUB_OUTPUT when set, and
// to stdout). `render` prints the comment Markdown to stdout.
//
// Exit codes:
//   0  ok (for gate, both run=true and run=false)
//   1  findings file invalid (every violation is printed to stderr)
//   2  usage error or unreadable file
//   3  identity refusal: the reviewer is the author

import { appendFileSync, readFileSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { resolve } from "node:path";

// Mirrored from docs/PR_REVIEW_PASSES.md — change both together.
export const SCHEMA_VERSION = 1;
export const MAX_FILE_BYTES = 256 * 1024;
export const MAX_FINDINGS = 30;
export const NITPICK_CAP = 5;
export const MAX_CLAIM_CHARS = 300;
export const MAX_FIX_CHARS = 1000;
export const MAX_PATH_CHARS = 300;
export const MARKER = "<!-- nightgauge:pr-review -->";

export const PASSES = ["correctness", "security", "plan-compliance"];
export const SEVERITIES = ["blocker", "major", "nitpick"];
const FINDING_KEYS = ["pass", "file", "line", "claim", "severity", "fix"];
const SKIPPED_AUTHORS = new Set(["dependabot[bot]"]);

// The model runs in the same job as CLAUDE_CODE_OAUTH_TOKEN. A string shaped like a
// credential in its output is treated as a leak, not as a finding.
const CREDENTIAL_SHAPES = [/sk-ant-/, /\bgh[posu]_[A-Za-z0-9]/, /github_pat_/];

class UsageError extends Error {}

function parseArgs(argv) {
  const flags = {};
  const positional = [];
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (arg.startsWith("--")) {
      const value = argv[i + 1];
      if (value === undefined || value.startsWith("--")) {
        throw new UsageError(`flag ${arg} needs a value`);
      }
      flags[arg.slice(2)] = value;
      i++;
    } else {
      positional.push(arg);
    }
  }
  return { flags, positional };
}

function readJSON(path) {
  let size;
  try {
    size = statSync(path).size;
  } catch (err) {
    throw new UsageError(`cannot read ${path}: ${err.message}`);
  }
  if (size > MAX_FILE_BYTES) {
    return { error: `file is ${size} bytes, over the ${MAX_FILE_BYTES}-byte limit` };
  }
  try {
    return { value: JSON.parse(readFileSync(path, "utf8")) };
  } catch (err) {
    return { error: `not valid JSON: ${err.message}` };
  }
}

/**
 * Decide whether a pull request gets reviewed.
 * @returns {{run: boolean, reason: string}}
 */
export function decide({ pr, event, pipelineAuthors, hasKey }) {
  const login = pr?.author?.login ?? "";
  if (!login) return { run: false, reason: "pull request has no author login" };
  if (pr.state && pr.state !== "OPEN") {
    return { run: false, reason: `pull request is ${pr.state.toLowerCase()}` };
  }
  if (pipelineAuthors.includes(login)) {
    return { run: false, reason: `authored by pipeline identity ${login}` };
  }
  if (SKIPPED_AUTHORS.has(login)) return { run: false, reason: `authored by ${login}` };
  if (pr.isDraft) return { run: false, reason: "draft pull request" };
  if (event === "pull_request" && pr.isCrossRepository) {
    return {
      run: false,
      reason:
        "fork pull request: no secrets under pull_request; a maintainer can run workflow_dispatch",
    };
  }
  if (!hasKey) return { run: false, reason: "no CLAUDE_CODE_OAUTH_TOKEN configured" };
  return { run: true, reason: `review ${login}'s pull request` };
}

function oneLine(value) {
  return String(value).replace(/[\r\n]+/g, " ");
}

function runGate(flags) {
  if (!flags["pr-json"] || !flags.event) throw new UsageError("gate needs --pr-json and --event");
  const read = readJSON(flags["pr-json"]);
  if (read.error) throw new UsageError(`${flags["pr-json"]}: ${read.error}`);
  const pipelineAuthors = (flags["pipeline-authors"] ?? "")
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
  const { run, reason } = decide({
    pr: read.value,
    event: flags.event,
    pipelineAuthors,
    hasKey: flags["has-key"] === "true",
  });
  const out = `run=${run}\nreason=${oneLine(reason)}\n`;
  process.stdout.write(out);
  if (process.env.GITHUB_OUTPUT) appendFileSync(process.env.GITHUB_OUTPUT, out);
  return 0;
}

function checkString(errors, where, value, max, { singleLine }) {
  if (typeof value !== "string" || value.trim() === "") {
    errors.push(`${where}: must be a non-empty string`);
    return;
  }
  if (value.length > max) errors.push(`${where}: ${value.length} characters, over ${max}`);
  if (singleLine && /[\r\n]/.test(value)) errors.push(`${where}: must be one line`);
}

/**
 * Validate a findings document. Returns every violation, empty when valid.
 * @returns {string[]}
 */
export function validate(doc) {
  const errors = [];
  if (doc === null || typeof doc !== "object" || Array.isArray(doc)) {
    return ["top level must be an object"];
  }
  for (const key of Object.keys(doc)) {
    if (key !== "schema" && key !== "findings") errors.push(`unknown top-level key "${key}"`);
  }
  if (doc.schema !== SCHEMA_VERSION) errors.push(`schema must be ${SCHEMA_VERSION}`);
  if (!Array.isArray(doc.findings)) {
    errors.push("findings must be an array");
    return errors;
  }
  if (doc.findings.length > MAX_FINDINGS) {
    errors.push(`${doc.findings.length} findings, over the cap of ${MAX_FINDINGS}`);
  }
  doc.findings.forEach((f, i) => {
    const at = `findings[${i}]`;
    if (f === null || typeof f !== "object" || Array.isArray(f)) {
      errors.push(`${at}: must be an object`);
      return;
    }
    for (const key of Object.keys(f)) {
      if (!FINDING_KEYS.includes(key)) errors.push(`${at}: unknown key "${key}"`);
    }
    for (const key of FINDING_KEYS) {
      if (!(key in f)) errors.push(`${at}: missing key "${key}"`);
    }
    if (!PASSES.includes(f.pass)) errors.push(`${at}.pass: must be one of ${PASSES.join(", ")}`);
    if (!SEVERITIES.includes(f.severity)) {
      errors.push(`${at}.severity: must be one of ${SEVERITIES.join(", ")}`);
    }
    checkString(errors, `${at}.file`, f.file, MAX_PATH_CHARS, { singleLine: true });
    if (typeof f.file === "string") {
      if (f.file.startsWith("/") || /^[A-Za-z]:/.test(f.file) || f.file.includes("\\")) {
        errors.push(`${at}.file: must be a repository-relative path`);
      }
      if (f.file.split("/").includes("..")) errors.push(`${at}.file: must not contain ".."`);
    }
    if (f.line !== null && !(Number.isInteger(f.line) && f.line >= 1)) {
      errors.push(`${at}.line: must be a positive integer or null`);
    }
    checkString(errors, `${at}.claim`, f.claim, MAX_CLAIM_CHARS, { singleLine: true });
    checkString(errors, `${at}.fix`, f.fix, MAX_FIX_CHARS, { singleLine: false });
    for (const key of ["file", "claim", "fix"]) {
      if (typeof f[key] === "string" && CREDENTIAL_SHAPES.some((re) => re.test(f[key]))) {
        errors.push(`${at}.${key}: contains a credential-shaped string`);
      }
    }
  });
  return errors;
}

// Model text is posted verbatim otherwise: neutralize HTML (including a forged
// review marker) and @-mentions, which would notify real accounts.
function escapeText(value) {
  return value
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/@/g, "@​");
}

function escapeCode(value) {
  return value.replace(/`/g, "'");
}

/**
 * Render a validated findings document as the comment body.
 * @returns {string}
 */
export function render(doc) {
  const rank = (f) => SEVERITIES.indexOf(f.severity);
  const sorted = doc.findings
    .map((f, i) => ({ f, i }))
    .sort((a, b) => rank(a.f) - rank(b.f) || a.i - b.i)
    .map(({ f }) => f);
  const nitpicks = sorted.filter((f) => f.severity === "nitpick");
  const shown = sorted
    .filter((f) => f.severity !== "nitpick")
    .concat(nitpicks.slice(0, NITPICK_CAP));
  const omitted = nitpicks.length - Math.min(nitpicks.length, NITPICK_CAP);

  const counts = SEVERITIES.map(
    (s) => `${sorted.filter((f) => f.severity === s).length} ${s}`
  ).join(", ");
  const lines = [MARKER, "## Nightgauge review", ""];
  if (sorted.length === 0) {
    lines.push("No findings from the correctness, security and plan-compliance passes.");
  } else {
    lines.push(`${counts}.`, "");
    for (const f of shown) {
      const where = f.line === null ? f.file : `${f.file}:${f.line}`;
      lines.push(
        `- **${f.severity}** (${f.pass}) \`${escapeCode(where)}\` — ${escapeText(f.claim)}`
      );
      const fix = escapeText(f.fix).split(/\r?\n/).join("\n    ");
      lines.push(`  - Fix: ${fix}`);
    }
    if (omitted > 0) {
      lines.push("", `${omitted} more nitpick${omitted === 1 ? "" : "s"} omitted.`);
    }
  }
  lines.push(
    "",
    "_Advisory review of a pull request the pipeline did not author; it does not block " +
      "merging. Passes and severities: `docs/PR_REVIEW_PASSES.md`._"
  );
  return lines.join("\n") + "\n";
}

function loadValid(path) {
  if (!path) throw new UsageError("a findings file is required");
  const read = readJSON(path);
  const errors = read.error ? [read.error] : validate(read.value);
  return { doc: read.value, errors };
}

function report(errors) {
  for (const err of errors) process.stderr.write(`pr-review: ${err}\n`);
  return 1;
}

function main(argv) {
  const [command, ...rest] = argv;
  const { flags, positional } = parseArgs(rest);
  switch (command) {
    case "gate":
      return runGate(flags);
    case "validate": {
      const { errors } = loadValid(positional[0]);
      return errors.length ? report(errors) : 0;
    }
    case "render": {
      if (!flags.author || !flags.reviewer) {
        throw new UsageError("render needs --author and --reviewer");
      }
      if (flags.author.toLowerCase() === flags.reviewer.toLowerCase()) {
        process.stderr.write(
          `pr-review: refusing to render: reviewer ${flags.reviewer} is the author\n`
        );
        return 3;
      }
      const { doc, errors } = loadValid(positional[0]);
      if (errors.length) return report(errors);
      process.stdout.write(render(doc));
      return 0;
    }
    default:
      throw new UsageError(`unknown command "${command ?? ""}" (want gate, validate or render)`);
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    process.exitCode = main(process.argv.slice(2));
  } catch (err) {
    if (!(err instanceof UsageError)) throw err;
    process.stderr.write(`pr-review: ${err.message}\n`);
    process.exitCode = 2;
  }
}
