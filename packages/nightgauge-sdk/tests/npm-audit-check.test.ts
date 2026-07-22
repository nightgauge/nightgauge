/**
 * Unit tests for scripts/npm-audit-check.js
 *
 * Strategy: spawn the script as a subprocess with a mock npm binary injected
 * via PATH and a temporary .npmauditrc.json. This tests actual exit codes and
 * stderr output without requiring internal module mocking.
 */

import { spawnSync, SpawnSyncReturns } from "child_process";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";

const SCRIPT = path.resolve(__dirname, "../../../scripts/npm-audit-check.js");
const REPO_ROOT = path.resolve(__dirname, "../../..");

/** Audit JSON with no vulnerabilities */
const CLEAN_AUDIT = JSON.stringify({
  vulnerabilities: {},
  metadata: { vulnerabilities: { total: 0 } },
});

/** Audit JSON with one high vulnerability (source ID 9999) */
const HIGH_VULN_AUDIT = JSON.stringify({
  vulnerabilities: {
    "some-pkg": {
      severity: "high",
      via: [
        {
          source: 9999,
          url: "https://github.com/advisories/GHSA-test-test-test",
          title: "Test High Vulnerability",
          severity: "high",
          range: ">=1.0.0",
        },
      ],
    },
  },
});

/** Audit JSON with one critical vulnerability not in any allow-list */
const CRITICAL_VULN_AUDIT = JSON.stringify({
  vulnerabilities: {
    "critical-pkg": {
      severity: "critical",
      via: [
        {
          source: 8888,
          url: "https://github.com/advisories/GHSA-crit-crit-crit",
          title: "Critical Vulnerability",
          severity: "critical",
          range: ">=2.0.0",
        },
      ],
    },
  },
});

/** Audit JSON with a moderate vulnerability only (should not fail) */
const MODERATE_ONLY_AUDIT = JSON.stringify({
  vulnerabilities: {
    "moderate-pkg": {
      severity: "moderate",
      via: [
        {
          source: 7777,
          url: "https://github.com/advisories/GHSA-mod-mod-mod",
          title: "Moderate Vulnerability",
          severity: "moderate",
          range: ">=1.0.0",
        },
      ],
    },
  },
});

/**
 * Create a temp directory with:
 * - A mock `npm` executable that outputs the given audit JSON
 * - An optional .npmauditrc.json
 *
 * Returns { tmpDir, env } for subprocess invocation.
 */
function setupTestEnv(
  auditJson: string,
  allowlistContent?: object
): { tmpDir: string; env: NodeJS.ProcessEnv } {
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "npm-audit-test-"));

  // Write mock npm binary
  const mockNpm = path.join(tmpDir, "npm");
  const escapedJson = auditJson.replace(/'/g, "'\\''");
  fs.writeFileSync(mockNpm, `#!/bin/sh\necho '${escapedJson}'\n`);
  fs.chmodSync(mockNpm, 0o755);

  // Write allowlist if provided
  if (allowlistContent !== undefined) {
    fs.writeFileSync(
      path.join(tmpDir, ".npmauditrc.json"),
      JSON.stringify(allowlistContent, null, 2)
    );
  }

  const env = {
    ...process.env,
    PATH: `${tmpDir}:${process.env.PATH}`,
  };

  return { tmpDir, env };
}

function runScript(env: NodeJS.ProcessEnv, allowlistPath?: string): SpawnSyncReturns<Buffer> {
  // Build args to override the allowlist path via an env var if needed
  // The script resolves ALLOWLIST_FILE relative to __dirname, so we patch it
  // by symlinking .npmauditrc.json into the tmpDir and overriding cwd, or by
  // using a wrapper. Instead, we just copy the allowlist into the cwd used.
  return spawnSync("node", [SCRIPT], {
    cwd: REPO_ROOT,
    env,
    timeout: 15000,
  });
}

/**
 * Run the script with a specific allowlist file content by temporarily
 * replacing the root .npmauditrc.json. Restores original after run.
 */
function runWithAllowlist(auditJson: string, allowlist: object): SpawnSyncReturns<Buffer> {
  const allowlistPath = path.join(REPO_ROOT, ".npmauditrc.json");
  const originalContent = fs.readFileSync(allowlistPath, "utf8");

  try {
    fs.writeFileSync(allowlistPath, JSON.stringify({ allowlist }, null, 2));
    const { tmpDir, env } = setupTestEnv(auditJson);
    const result = runScript(env);
    fs.rmSync(tmpDir, { recursive: true, force: true });
    return result;
  } finally {
    fs.writeFileSync(allowlistPath, originalContent);
  }
}

describe("npm-audit-check.js", () => {
  describe("exit 0 — clean cases", () => {
    it("exits 0 when npm audit reports no vulnerabilities", () => {
      const result = runWithAllowlist(CLEAN_AUDIT, []);
      expect(result.status).toBe(0);
      expect(result.stdout.toString()).toContain("No high/critical vulnerabilities found");
    });

    it("exits 0 when a moderate-only vulnerability is present (not gated)", () => {
      const result = runWithAllowlist(MODERATE_ONLY_AUDIT, []);
      expect(result.status).toBe(0);
    });

    it("exits 0 when a high vulnerability is covered by the allow-list", () => {
      const allowlist = [
        {
          id: "9999",
          reason: "Test entry. Tracked in #9999.",
          expires: "2099-12-31",
          addedBy: "test",
        },
      ];
      const result = runWithAllowlist(HIGH_VULN_AUDIT, allowlist);
      expect(result.status).toBe(0);
      expect(result.stdout.toString()).toContain("covered by allow-list");
    });
  });

  describe("exit 1 — violation cases", () => {
    it("exits 1 when an unallowed high vulnerability is found", () => {
      const result = runWithAllowlist(HIGH_VULN_AUDIT, []);
      expect(result.status).toBe(1);
      expect(result.stderr.toString()).toContain("NPM AUDIT");
      expect(result.stderr.toString()).toContain("[HIGH]");
      expect(result.stderr.toString()).toContain("some-pkg");
    });

    it("exits 1 when an unallowed critical vulnerability is found", () => {
      const result = runWithAllowlist(CRITICAL_VULN_AUDIT, []);
      expect(result.status).toBe(1);
      expect(result.stderr.toString()).toContain("[CRITICAL]");
      expect(result.stderr.toString()).toContain("critical-pkg");
    });

    it("exits 1 when an allow-list entry is expired", () => {
      const allowlist = [
        {
          id: "9999",
          reason: "Expired test entry.",
          expires: "2020-01-01", // past date
          addedBy: "test",
        },
      ];
      const result = runWithAllowlist(HIGH_VULN_AUDIT, allowlist);
      expect(result.status).toBe(1);
      expect(result.stderr.toString()).toContain("Expired allow-list entries found");
      expect(result.stderr.toString()).toContain("2020-01-01");
    });

    it("includes remediation instructions on violation", () => {
      const result = runWithAllowlist(HIGH_VULN_AUDIT, []);
      const stderr = result.stderr.toString();
      expect(stderr).toContain("npm audit");
      expect(stderr).toContain(".npmauditrc.json");
      expect(stderr).toContain("NPM_AUDIT_PROCESS.md");
    });
  });

  describe("exit 2 — config error cases", () => {
    it("exits 2 when .npmauditrc.json contains invalid JSON", () => {
      const allowlistPath = path.join(REPO_ROOT, ".npmauditrc.json");
      const originalContent = fs.readFileSync(allowlistPath, "utf8");

      try {
        fs.writeFileSync(allowlistPath, "{ invalid json !!!");
        const { tmpDir, env } = setupTestEnv(CLEAN_AUDIT);
        const result = runScript(env);
        fs.rmSync(tmpDir, { recursive: true, force: true });
        expect(result.status).toBe(2);
        expect(result.stderr.toString()).toContain("Failed to parse");
      } finally {
        fs.writeFileSync(allowlistPath, originalContent);
      }
    });
  });
});
