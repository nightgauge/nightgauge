/**
 * migratePerformanceMode.test.ts (Issue #3009)
 *
 * Asserts the one-time migration helper:
 *   - active=true  → maximum
 *   - active=false → elevated
 *   - no-op when the new file already exists
 *   - no-op when no legacy file is present
 *   - the legacy file is renamed to `supercharge.yaml.migrated`
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as path from "node:path";
import * as os from "node:os";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "__PRIMARY__" } }],
  },
}));

import { migrateSuperchargeToPerformanceMode } from "../../src/utils/migratePerformanceMode";

const NEW_FILE = "performance-mode.yaml";
const LEGACY_FILE = "supercharge.yaml";
const LEGACY_BACKUP = "supercharge.yaml.migrated";

function writeLegacy(root: string, active: boolean): void {
  const dir = path.join(root, ".nightgauge");
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, LEGACY_FILE), `active: ${active}\n`, "utf-8");
}

describe("migrateSuperchargeToPerformanceMode", () => {
  let workspaceRoot: string;

  beforeEach(() => {
    workspaceRoot = fs.mkdtempSync(path.join(os.tmpdir(), "perf-migrate-"));
  });

  afterEach(() => {
    fs.rmSync(workspaceRoot, { recursive: true, force: true });
  });

  it("active:true → maximum and renames the legacy file", () => {
    writeLegacy(workspaceRoot, true);

    const result = migrateSuperchargeToPerformanceMode(workspaceRoot);

    expect(result.migrated).toBe(true);
    expect(result.mode).toBe("maximum");
    const newFile = path.join(workspaceRoot, ".nightgauge", NEW_FILE);
    expect(fs.existsSync(newFile)).toBe(true);
    expect(fs.readFileSync(newFile, "utf-8")).toContain("mode: maximum");
    expect(fs.existsSync(path.join(workspaceRoot, ".nightgauge", LEGACY_FILE))).toBe(false);
    expect(fs.existsSync(path.join(workspaceRoot, ".nightgauge", LEGACY_BACKUP))).toBe(true);
  });

  it("active:false → elevated and renames the legacy file", () => {
    writeLegacy(workspaceRoot, false);

    const result = migrateSuperchargeToPerformanceMode(workspaceRoot);

    expect(result.migrated).toBe(true);
    expect(result.mode).toBe("elevated");
    const newFile = path.join(workspaceRoot, ".nightgauge", NEW_FILE);
    expect(fs.readFileSync(newFile, "utf-8")).toContain("mode: elevated");
    expect(fs.existsSync(path.join(workspaceRoot, ".nightgauge", LEGACY_BACKUP))).toBe(true);
  });

  it("is a no-op when the new file already exists", () => {
    fs.mkdirSync(path.join(workspaceRoot, ".nightgauge"), { recursive: true });
    fs.writeFileSync(
      path.join(workspaceRoot, ".nightgauge", NEW_FILE),
      "mode: efficiency\n",
      "utf-8"
    );
    writeLegacy(workspaceRoot, true);

    const result = migrateSuperchargeToPerformanceMode(workspaceRoot);

    expect(result.migrated).toBe(false);
    // Legacy file untouched
    expect(fs.existsSync(path.join(workspaceRoot, ".nightgauge", LEGACY_FILE))).toBe(true);
    expect(fs.readFileSync(path.join(workspaceRoot, ".nightgauge", NEW_FILE), "utf-8")).toContain(
      "mode: efficiency"
    );
  });

  it("is a no-op when no legacy file is present (first-time install)", () => {
    const result = migrateSuperchargeToPerformanceMode(workspaceRoot);
    expect(result.migrated).toBe(false);
    expect(fs.existsSync(path.join(workspaceRoot, ".nightgauge", NEW_FILE))).toBe(false);
  });

  it("is idempotent across multiple calls", () => {
    writeLegacy(workspaceRoot, true);

    const first = migrateSuperchargeToPerformanceMode(workspaceRoot);
    const second = migrateSuperchargeToPerformanceMode(workspaceRoot);

    expect(first.migrated).toBe(true);
    expect(second.migrated).toBe(false);
  });
});
