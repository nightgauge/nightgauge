#!/usr/bin/env node
/**
 * npm-audit-check.js
 * Runs `npm audit --json` twice, filters allow-listed advisories, enforces
 * expiry, and exits non-zero if an unallowed advisory remains: moderate or
 * worse in a runtime dependency (`--omit=dev`), high or worse anywhere.
 *
 * Usage: node scripts/npm-audit-check.js
 * Exit codes: 0 = clean, 1 = violations found, 2 = config error
 */

"use strict";

const { execFileSync } = require("child_process");
const fs = require("fs");
const path = require("path");

// NPM_AUDIT_CHECK_ALLOWLIST, NPM_AUDIT_CHECK_INPUT (the whole tree) and
// NPM_AUDIT_CHECK_RUNTIME_INPUT (`--omit=dev`) substitute fixtures for the
// allow-list and the `npm audit --json` outputs (scripts/test-npm-audit-check.sh).
const ALLOWLIST_FILE =
  process.env.NPM_AUDIT_CHECK_ALLOWLIST || path.resolve(__dirname, "../.npmauditrc.json");
const TODAY = new Date().toISOString().slice(0, 10);

// Load allow-list
let allowlist = [];
if (fs.existsSync(ALLOWLIST_FILE)) {
  try {
    const config = JSON.parse(fs.readFileSync(ALLOWLIST_FILE, "utf8"));
    allowlist = config.allowlist || [];
  } catch (err) {
    console.error(`ERROR: Failed to parse ${ALLOWLIST_FILE}: ${err.message}`);
    process.exit(2);
  }
}

// Check for expired entries (fail fast — stale exceptions are a security risk)
const expired = allowlist.filter((entry) => entry.expires && entry.expires < TODAY);
if (expired.length > 0) {
  console.error("ERROR: Expired allow-list entries found:");
  expired.forEach((e) => {
    console.error(`  ${e.id} — expired ${e.expires} (${e.reason})`);
  });
  console.error("Remove expired entries or extend their expiry dates in .npmauditrc.json");
  process.exit(1);
}

const allowedIds = new Set(allowlist.map((e) => e.id));

// Two audits, two thresholds (ADR-023 Decision 8): what ships to users
// (`--omit=dev`) gates at moderate, and the whole tree, dev tooling included,
// gates at high. A waiver is a dated allow-list entry, never a lower bar.
const TIERS = [
  {
    label: "runtime (--omit=dev)",
    args: ["--omit=dev"],
    input: process.env.NPM_AUDIT_CHECK_RUNTIME_INPUT,
    severities: ["moderate", "high", "critical"],
  },
  {
    label: "all dependencies",
    args: [],
    input: process.env.NPM_AUDIT_CHECK_INPUT,
    severities: ["high", "critical"],
  },
];
// Fixture mode: a suite that substitutes one input gets an empty report for
// the other, so it never reaches the live advisory database.
const FIXTURE_MODE = Boolean(
  process.env.NPM_AUDIT_CHECK_INPUT || process.env.NPM_AUDIT_CHECK_RUNTIME_INPUT
);

function readAudit(tier) {
  let output;
  if (tier.input) {
    output = fs.readFileSync(tier.input, "utf8");
  } else if (FIXTURE_MODE) {
    output = '{"vulnerabilities":{}}';
  } else {
    try {
      output = execFileSync("npm", ["audit", ...tier.args, "--json"], {
        encoding: "utf8",
        stdio: ["pipe", "pipe", "pipe"],
      });
    } catch (err) {
      // npm audit exits non-zero when vulnerabilities are found — capture output
      output = err.stdout || "{}";
    }
  }
  try {
    return JSON.parse(output);
  } catch {
    console.error(`ERROR: Failed to parse npm audit output (${tier.label}) as JSON`);
    console.error(output.slice(0, 500));
    process.exit(2);
  }
}

// A `via` entry is either an advisory object or the name of another vulnerable
// package (a dependent of a vulnerable package is flagged too). Resolve names to
// the advisories they carry, so allow-listing an advisory also covers every
// package that is flagged only because it depends on the vulnerable one.
function advisoryIds(vulnerabilities, pkg, seen = new Set()) {
  if (seen.has(pkg)) return [];
  seen.add(pkg);
  return (vulnerabilities[pkg]?.via || []).flatMap((v) =>
    typeof v === "object" ? [v.source || v.url || "unknown"] : advisoryIds(vulnerabilities, v, seen)
  );
}

const violations = [];
const covered = [];
for (const tier of TIERS) {
  const vulnerabilities = readAudit(tier).vulnerabilities || {};
  for (const [pkg, vuln] of Object.entries(vulnerabilities)) {
    if (!tier.severities.includes(vuln.severity)) continue;
    const viaIds = [...new Set(advisoryIds(vulnerabilities, pkg))];
    const unallowed = viaIds.filter((id) => !allowedIds.has(String(id)));
    if (unallowed.length > 0 || viaIds.length === 0) {
      violations.push({ tier: tier.label, pkg, severity: vuln.severity, via: viaIds });
    } else {
      covered.push(pkg);
    }
  }
}

if (violations.length > 0) {
  console.error(`\nNPM AUDIT: ${violations.length} unallowed vulnerability(ies) found:\n`);
  violations.forEach(({ tier, pkg, severity, via }) => {
    console.error(`  [${severity.toUpperCase()}] ${pkg} — ${tier}`);
    if (via.length > 0) console.error(`    Advisory IDs: ${via.join(", ")}`);
  });
  console.error("\nThresholds: runtime dependencies fail at moderate, dev dependencies at high.");
  console.error("To resolve:");
  console.error("  1. Run `npm audit` locally to see full details");
  console.error("  2. Update the vulnerable package if a fix is available");
  console.error(
    "  3. If no fix exists, add to .npmauditrc.json with expiry date and tracking issue"
  );
  console.error("  4. See docs/NPM_AUDIT_PROCESS.md for the full triage process");
  process.exit(1);
}

if (covered.length > 0) {
  console.log(`npm audit: ${covered.length} gated vuln(s) found — all covered by allow-list. ✓`);
} else {
  console.log("npm audit: no moderate+ runtime and no high+ dev vulnerabilities found. ✓");
}
