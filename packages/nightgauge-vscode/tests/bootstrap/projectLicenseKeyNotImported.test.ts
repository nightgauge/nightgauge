/**
 * projectLicenseKeyNotImported.test.ts
 *
 * Regression guard for #2023: extension startup used to copy a
 * `platform.license_key` found in the workspace's committed
 * `.nightgauge/config.yaml` into SecretStorage, so a cloned repository could
 * replace the operator's license key. The fix deletes that import; startup now
 * only warns.
 *
 * The migration runs inline in the VSCode-API-dependent `initializeServices()`
 * bootstrap, which no unit harness stands up, and the fix is a deletion, so
 * this is a resurrection pin against the block's own source text — the form
 * shared with the other tests in this directory.
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";

const SERVICES_PATH = path.resolve(__dirname, "../../src/bootstrap/services.ts");

function projectConfigBlock(): string {
  const src = readFileSync(SERVICES_PATH, "utf-8");
  const start = src.indexOf("// 1. A key in the project config is never imported");
  const end = src.indexOf("// 2. Seed SecretStorage from the machine config");
  expect(start, "project-config license block not found").toBeGreaterThan(-1);
  expect(end).toBeGreaterThan(start);
  return src.slice(start, end);
}

describe("project config license key (#2023)", () => {
  it("is never written to SecretStorage or cached", () => {
    const block = projectConfigBlock();
    expect(block).not.toMatch(/setSecret\s*\(/);
    expect(block).not.toMatch(/cachedLicenseKey\s*=/);
  });

  it("does not rewrite the committed file, and tells the user", () => {
    const block = projectConfigBlock();
    expect(block).not.toMatch(/writeFileSync\s*\(/);
    expect(block).toMatch(/showWarningMessage\s*\(/);
  });
});
