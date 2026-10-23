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
import { mkFakeCloneLayout } from "../helpers/cloneLayout";

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
  /** The new file: the checkout's own, in CHECKOUT (ADR-024 § 7). */
  let newFile: string;

  beforeEach(() => {
    workspaceRoot = fs.mkdtempSync(path.join(os.tmpdir(), "perf-migrate-"));
    newFile = path.join(mkFakeCloneLayout(workspaceRoot).checkout, NEW_FILE);
  });

  afterEach(() => {
    fs.rmSync(workspaceRoot, { recursive: true, force: true });
  });

  it("active:true → maximum and renames the legacy file", () => {
    writeLegacy(workspaceRoot, true);

    const result = migrateSuperchargeToPerformanceMode(workspaceRoot);

    expect(result.migrated).toBe(true);
    expect(result.mode).toBe("maximum");
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
    expect(fs.readFileSync(newFile, "utf-8")).toContain("mode: elevated");
    expect(fs.existsSync(path.join(workspaceRoot, ".nightgauge", LEGACY_BACKUP))).toBe(true);
  });

  it("is a no-op when the new file already exists", () => {
    fs.writeFileSync(newFile, "mode: efficiency\n", "utf-8");
    writeLegacy(workspaceRoot, true);

    const result = migrateSuperchargeToPerformanceMode(workspaceRoot);

    expect(result.migrated).toBe(false);
    // Legacy file untouched
    expect(fs.existsSync(path.join(workspaceRoot, ".nightgauge", LEGACY_FILE))).toBe(true);
    expect(fs.readFileSync(newFile, "utf-8")).toContain("mode: efficiency");
  });

  it("is a no-op when no legacy file is present (first-time install)", () => {
    const result = migrateSuperchargeToPerformanceMode(workspaceRoot);
    expect(result.migrated).toBe(false);
    expect(fs.existsSync(newFile)).toBe(false);
  });

  it("is idempotent across multiple calls", () => {
    writeLegacy(workspaceRoot, true);

    const first = migrateSuperchargeToPerformanceMode(workspaceRoot);
    const second = migrateSuperchargeToPerformanceMode(workspaceRoot);

    expect(first.migrated).toBe(true);
    expect(second.migrated).toBe(false);
  });

  it("is a no-op outside a git checkout and never writes into the working tree", () => {
    const plain = fs.mkdtempSync(path.join(os.tmpdir(), "perf-migrate-plain-"));
    try {
      writeLegacy(plain, true);
      expect(migrateSuperchargeToPerformanceMode(plain).migrated).toBe(false);
      expect(fs.existsSync(path.join(plain, ".nightgauge", NEW_FILE))).toBe(false);
      expect(fs.existsSync(path.join(plain, ".nightgauge", LEGACY_FILE))).toBe(true);
    } finally {
      fs.rmSync(plain, { recursive: true, force: true });
    }
  });
});
