#!/usr/bin/env node
// Proposal-artifact validator — the gate between the model job and the write
// job in release-watchdog.yml and continuous-improvement.yml.
//
// Both scheduled workflows used to run `claude -p` with Bash over text the
// repository does not control (third-party release notes; this repo's own
// history) while the same job held `contents: write`, `issues: write` and a
// GH_TOKEN. That is a prompt-injection-to-write path. The workflows are now
// split: an `analyze` job with read-only permissions and no forge token
// writes ONE JSON file, and an `apply` job with the write scopes files issues
// from it — but only after this script has said the file is exactly the shape
// the apply job expects. Everything the model can influence passes through
// here, so the rules are closed: exact keys, bounded sizes, a label allowlist
// and a proposal cap. Anything else is rejected.
//
// Schema (version 1):
//
//   {
//     "schema": 1,
//     "kind": "release-watch" | "continuous-improvement",
//     "proposals": [
//       { "title": string, "body": string, "labels": [string, ...] }
//     ]
//   }
//
// Usage:
//   node scripts/validate-proposal-artifact.mjs --kind <kind> [--max-proposals N] <file>
//
// Exit codes:
//   0  valid
//   1  invalid (every violation is printed to stderr)
//   2  usage error or unreadable file

import { readFileSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { resolve } from "node:path";

export const SCHEMA_VERSION = 1;
export const MAX_FILE_BYTES = 1024 * 1024;
export const MAX_PROPOSALS_DEFAULT = 10;
export const MAX_TITLE_CHARS = 200;
export const MAX_BODY_CHARS = 20000;
export const MAX_LABELS = 6;

const PRIORITY_LABELS = ["priority:critical", "priority:high", "priority:medium", "priority:low"];
const TYPE_LABELS = ["type:feature", "type:bug", "type:chore", "type:spike", "type:fix"];

// Per-kind label allowlist, and the one label every proposal of that kind
// must carry so an auto-created issue is always attributable to its producer.
export const KINDS = {
  "release-watch": {
    required: "source:auto-discovery",
    allowed: new Set([
      "source:auto-discovery",
      "claude-code-release",
      "codex-release",
      "gemini-release",
      "component:security",
      "component:performance",
      "component:vscode-extension",
      ...PRIORITY_LABELS,
      ...TYPE_LABELS,
    ]),
  },
  "continuous-improvement": {
    required: "continuous-improvement",
    allowed: new Set(["continuous-improvement", ...PRIORITY_LABELS, ...TYPE_LABELS]),
  },
};

const TOP_LEVEL_KEYS = ["schema", "kind", "proposals"];
const PROPOSAL_KEYS = ["title", "body", "labels"];

// C0 controls other than tab / newline / carriage return, plus DEL. A title
// additionally may not contain line breaks: it becomes a `--title` argument
// and a single line in the run record.
// Checked by code point rather than a regex character class: ESLint's
// no-control-regex forbids the latter, and the intent reads better anyway.
function hasControlChars(text, { allowLineBreaks }) {
  for (const char of text) {
    const code = char.codePointAt(0);
    if (code === 0x7f) return true;
    if (code >= 0x20) continue;
    if (allowLineBreaks && (code === 0x09 || code === 0x0a || code === 0x0d)) continue;
    return true;
  }
  return false;
}

function isPlainObject(value) {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function checkExactKeys(actual, expected, where, problems) {
  const keys = Object.keys(actual);
  for (const key of expected) {
    if (!keys.includes(key)) problems.push(`${where}: missing key "${key}"`);
  }
  for (const key of keys) {
    if (!expected.includes(key)) problems.push(`${where}: unexpected key "${key}"`);
  }
}

/**
 * Validate a parsed artifact. Returns the list of problems; empty means valid.
 * Pure so the test suite can exercise every rule without touching the disk.
 */
export function validateArtifact(doc, { kind, maxProposals = MAX_PROPOSALS_DEFAULT } = {}) {
  const problems = [];
  const spec = KINDS[kind];
  if (!spec) {
    problems.push(`unknown kind "${kind}" (expected one of ${Object.keys(KINDS).join(", ")})`);
    return problems;
  }
  if (!isPlainObject(doc)) {
    problems.push("top level: expected a JSON object");
    return problems;
  }
  checkExactKeys(doc, TOP_LEVEL_KEYS, "top level", problems);
  if (doc.schema !== SCHEMA_VERSION) {
    problems.push(`schema: expected ${SCHEMA_VERSION}, got ${JSON.stringify(doc.schema)}`);
  }
  if (doc.kind !== kind) {
    problems.push(`kind: expected "${kind}", got ${JSON.stringify(doc.kind)}`);
  }
  if (!Array.isArray(doc.proposals)) {
    problems.push("proposals: expected an array");
    return problems;
  }
  if (doc.proposals.length > maxProposals) {
    problems.push(`proposals: ${doc.proposals.length} entries exceeds the cap of ${maxProposals}`);
  }
  const seenTitles = new Set();
  doc.proposals.forEach((proposal, index) => {
    const where = `proposals[${index}]`;
    if (!isPlainObject(proposal)) {
      problems.push(`${where}: expected an object`);
      return;
    }
    checkExactKeys(proposal, PROPOSAL_KEYS, where, problems);

    const { title, body, labels } = proposal;
    if (typeof title !== "string") {
      problems.push(`${where}.title: expected a string`);
    } else {
      if (title.trim().length === 0) problems.push(`${where}.title: empty`);
      if (title.length > MAX_TITLE_CHARS) {
        problems.push(`${where}.title: ${title.length} chars exceeds ${MAX_TITLE_CHARS}`);
      }
      if (hasControlChars(title, { allowLineBreaks: false }))
        problems.push(`${where}.title: contains a control character`);
      const normalized = title.trim().toLowerCase();
      if (seenTitles.has(normalized))
        problems.push(`${where}.title: duplicates an earlier proposal`);
      seenTitles.add(normalized);
    }

    if (typeof body !== "string") {
      problems.push(`${where}.body: expected a string`);
    } else {
      if (body.trim().length === 0) problems.push(`${where}.body: empty`);
      if (body.length > MAX_BODY_CHARS) {
        problems.push(`${where}.body: ${body.length} chars exceeds ${MAX_BODY_CHARS}`);
      }
      if (hasControlChars(body, { allowLineBreaks: true }))
        problems.push(`${where}.body: contains a control character`);
    }

    if (!Array.isArray(labels)) {
      problems.push(`${where}.labels: expected an array`);
    } else {
      if (labels.length === 0) problems.push(`${where}.labels: empty`);
      if (labels.length > MAX_LABELS) {
        problems.push(`${where}.labels: ${labels.length} labels exceeds ${MAX_LABELS}`);
      }
      const seenLabels = new Set();
      for (const label of labels) {
        if (typeof label !== "string") {
          problems.push(`${where}.labels: non-string entry`);
          continue;
        }
        if (!spec.allowed.has(label)) {
          problems.push(`${where}.labels: "${label}" is not in the ${kind} allowlist`);
        }
        if (seenLabels.has(label)) problems.push(`${where}.labels: "${label}" repeated`);
        seenLabels.add(label);
      }
      if (!seenLabels.has(spec.required)) {
        problems.push(`${where}.labels: missing the required "${spec.required}" label`);
      }
    }
  });
  return problems;
}

function usage(message) {
  if (message) console.error(`validate-proposal-artifact: ${message}`);
  console.error(
    "usage: validate-proposal-artifact.mjs --kind <release-watch|continuous-improvement> [--max-proposals N] <file>"
  );
  return 2;
}

export function main(argv) {
  let kind = "";
  let maxProposals = MAX_PROPOSALS_DEFAULT;
  let file = "";
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === "--kind") {
      kind = argv[++i] ?? "";
    } else if (arg === "--max-proposals") {
      const raw = argv[++i] ?? "";
      maxProposals = Number.parseInt(raw, 10);
      if (!Number.isInteger(maxProposals) || maxProposals < 0) {
        return usage(`--max-proposals must be a non-negative integer, got "${raw}"`);
      }
    } else if (arg.startsWith("-")) {
      return usage(`unknown flag ${arg}`);
    } else if (file) {
      return usage("exactly one file expected");
    } else {
      file = arg;
    }
  }
  if (!kind || !file) return usage("--kind and <file> are required");

  let size;
  try {
    size = statSync(file).size;
  } catch (error) {
    console.error(`validate-proposal-artifact: cannot stat ${file}: ${error.message}`);
    return 2;
  }
  if (size > MAX_FILE_BYTES) {
    console.error(
      `validate-proposal-artifact: ${file} is ${size} bytes, over the ${MAX_FILE_BYTES} cap`
    );
    return 1;
  }

  let doc;
  try {
    doc = JSON.parse(readFileSync(file, "utf8"));
  } catch (error) {
    console.error(`validate-proposal-artifact: ${file} is not valid JSON: ${error.message}`);
    return 1;
  }

  const problems = validateArtifact(doc, { kind, maxProposals });
  if (problems.length > 0) {
    console.error(`validate-proposal-artifact: ${file} rejected (${problems.length} problem(s)):`);
    for (const problem of problems) console.error(`  - ${problem}`);
    return 1;
  }
  console.log(
    `validate-proposal-artifact: ${file} ok (${doc.proposals.length} proposal(s), kind ${kind})`
  );
  return 0;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exit(main(process.argv.slice(2)));
}
