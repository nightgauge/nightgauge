# npm Audit Process

This document describes how to triage `npm audit` findings surfaced by CI.

## Overview

CI runs `node scripts/npm-audit-check.js` after `npm ci`. The script:

1. Checks `.npmauditrc.json` for expired allow-list entries (fails CI if any)
2. Runs `npm audit --audit-level=high --json`
3. Filters out high/critical advisories covered by the allow-list
4. Fails CI with actionable output if unallowed high/critical vulns remain

Moderate and low severity findings are visible in audit output but do not fail
the build.

---

## When CI Fails with "NPM AUDIT"

When CI reports unallowed high/critical vulnerabilities, follow this decision
tree:

```
Is a patched version of the vulnerable package available?
├── YES → Update the package
│         npm update <package>       # for direct dependency
│         npm install <package>@latest
│         Verify: npm audit --audit-level=high
│         Commit the updated package-lock.json
│
└── NO  → Is this a transitive dependency with no upstream fix?
          ├── YES → Add to allow-list with expiry (see below)
          └── NO  → Investigate further; escalate if critical (see Escalation)
```

---

## How to Read npm audit Output

Run locally:

```bash
npm audit
npm audit --audit-level=high   # show only high/critical
npm audit --json               # machine-readable output
```

Key fields in output:

| Field          | Meaning                                              |
| -------------- | ---------------------------------------------------- |
| `severity`     | `critical`, `high`, `moderate`, `low`, `info`        |
| `via`          | Direct or transitive source of the vulnerability     |
| `fixAvailable` | Whether `npm audit fix` can resolve it automatically |
| Advisory URL   | Links to GHSA-_ or CVE-_ advisory for full details   |

---

## How to Add an Allow-List Entry

When no fix is available and CI must pass, add the advisory to
`.npmauditrc.json`:

```json
{
  "allowlist": [
    {
      "id": "GHSA-xxxx-yyyy-zzzz",
      "reason": "No fix available upstream. Tracked in #NNN.",
      "expires": "2026-07-01",
      "addedBy": "your-github-username"
    }
  ]
}
```

**Field requirements:**

| Field     | Required | Description                                                        |
| --------- | -------- | ------------------------------------------------------------------ |
| `id`      | Yes      | Advisory ID from `npm audit` output (GHSA-\* or numeric source ID) |
| `reason`  | Yes      | Why this is accepted — must reference a tracking issue             |
| `expires` | Yes      | ISO date (`YYYY-MM-DD`). CI fails after this date, forcing review. |
| `addedBy` | Yes      | GitHub username of the person adding the exception                 |

**Expiry policy:**

- Set expiry to **90 days** from today for moderate-risk exceptions
- Set expiry to **30 days** for high/critical exceptions where fix is expected
- Do not set expiry beyond the date a fix is expected to be available
- Review and remove entries when the upstream package ships a fix

**Finding the advisory ID:**

The `id` field in the allow-list matches the `source` field from `npm audit
--json` output (a numeric GHSA source ID), not the package name. Run:

```bash
npm audit --json | jq '.vulnerabilities | to_entries[] | {pkg: .key, via: .value.via}'
```

Look for `"source": 123456` or `"url": "https://github.com/advisories/GHSA-..."` in
the `via` array. The numeric source ID is what the script checks.

---

## How to Remove or Update an Entry

**When a fix ships:**

1. Remove the entry from `.npmauditrc.json`
2. Update the package: `npm update <package>` or `npm install <package>@latest`
3. Verify: `node scripts/npm-audit-check.js`
4. Commit both changes together

**When an entry expires:**

CI will fail with:

```
ERROR: Expired allow-list entries found:
  GHSA-xxxx-yyyy-zzzz — expired 2026-07-01 (No fix available. Tracked in #NNN.)
Remove expired entries or extend their expiry dates in .npmauditrc.json
```

Options:

1. **If a fix is now available**: Remove the entry and update the package
2. **If still no fix**: Extend the expiry with updated reasoning and re-evaluate
3. **If the package is no longer in use**: Remove it from dependencies entirely

---

## Escalation — Critical Vulnerabilities

For `critical` severity findings:

1. Check if the vulnerability is exploitable in this codebase's usage context
2. If exploitable: treat as P0 — fix before merging any other work
3. If not exploitable in context (e.g., server-side-only package used in
   backend, no network exposure): document the reasoning explicitly in the
   allow-list entry and open a tracking issue
4. Tag `<your-security-team>` on the tracking issue for visibility

---

## Local Validation

Before pushing, verify your changes pass the audit check:

```bash
node scripts/npm-audit-check.js
```

Exit codes:

| Code | Meaning                                                                |
| ---- | ---------------------------------------------------------------------- |
| `0`  | Clean — no unallowed high/critical vulns                               |
| `1`  | Violations found or expired allow-list entry                           |
| `2`  | Config error (malformed `.npmauditrc.json` or npm audit parse failure) |
