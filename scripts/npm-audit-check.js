#!/usr/bin/env node
/**
 * npm-audit-check.js
 * Runs `npm audit --json`, filters allow-listed advisories, enforces expiry,
 * and exits non-zero if any unallowed high/critical vulnerabilities remain.
 *
 * Usage: node scripts/npm-audit-check.js
 * Exit codes: 0 = clean, 1 = violations found, 2 = config error
 */

"use strict";

const { execSync } = require("child_process");
const fs = require("fs");
const path = require("path");

const ALLOWLIST_FILE = path.resolve(__dirname, "../.npmauditrc.json");
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

// Run npm audit
let auditOutput;
try {
  auditOutput = execSync("npm audit --audit-level=high --json", {
    encoding: "utf8",
    stdio: ["pipe", "pipe", "pipe"],
  });
} catch (err) {
  // npm audit exits non-zero when vulnerabilities are found — capture output
  auditOutput = err.stdout || "{}";
}

let auditData;
try {
  auditData = JSON.parse(auditOutput);
} catch {
  console.error("ERROR: Failed to parse npm audit output as JSON");
  console.error(auditOutput.slice(0, 500));
  process.exit(2);
}

// Extract high/critical vulnerabilities not in allow-list
const vulnerabilities = auditData.vulnerabilities || {};
const violations = [];

for (const [pkg, vuln] of Object.entries(vulnerabilities)) {
  if (!["high", "critical"].includes(vuln.severity)) continue;
  const viaIds = (vuln.via || [])
    .filter((v) => typeof v === "object")
    .map((v) => v.source || v.url || "unknown");

  const unallowed = viaIds.filter((id) => !allowedIds.has(String(id)));
  if (unallowed.length > 0 || viaIds.length === 0) {
    violations.push({ pkg, severity: vuln.severity, via: viaIds });
  }
}

if (violations.length > 0) {
  console.error(
    `\nNPM AUDIT: ${violations.length} unallowed high/critical vulnerability(ies) found:\n`
  );
  violations.forEach(({ pkg, severity, via }) => {
    console.error(`  [${severity.toUpperCase()}] ${pkg}`);
    if (via.length > 0) console.error(`    Advisory IDs: ${via.join(", ")}`);
  });
  console.error("\nTo resolve:");
  console.error("  1. Run `npm audit` locally to see full details");
  console.error("  2. Update the vulnerable package if a fix is available");
  console.error(
    "  3. If no fix exists, add to .npmauditrc.json with expiry date and tracking issue"
  );
  console.error("  4. See docs/NPM_AUDIT_PROCESS.md for the full triage process");
  process.exit(1);
}

const totalHigh = Object.values(vulnerabilities).filter(
  (v) => v.severity === "high" || v.severity === "critical"
).length;

if (totalHigh > 0) {
  console.log(`npm audit: ${totalHigh} high/critical vuln(s) found — all covered by allow-list. ✓`);
} else {
  console.log("npm audit: No high/critical vulnerabilities found. ✓");
}
