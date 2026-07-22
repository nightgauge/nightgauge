/**
 * isSuperchargeModeActive.test.ts
 *
 * Issue #3009 replaced the binary supercharge toggle with the
 * `performance_mode` selector. `isSuperchargeModeActive` is now a thin
 * deprecation wrapper that returns true iff the active mode is `maximum`.
 *
 * These tests confirm the wrapper preserves the original primary-workspace
 * fallback semantics (status-bar item writes to the primary workspace, but
 * pipeline stages may run under a different workspace root — concurrent
 * worktrees, multi-repo workspaces, #1621).
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as path from "node:path";
import * as os from "node:os";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "__PRIMARY_WORKSPACE__" } }],
  },
}));

import * as vscode from "vscode";
import { isSuperchargeModeActive } from "../../src/utils/resolvers/monitoringResolver";

function writePerformanceMode(dir: string, mode: "efficiency" | "elevated" | "maximum"): void {
  const filePath = path.join(dir, ".nightgauge", "performance-mode.yaml");
  fs.mkdirSync(path.dirname(filePath), { recursive: true });
  fs.writeFileSync(filePath, `mode: ${mode}\n`, "utf-8");
}

describe("isSuperchargeModeActive — primary-workspace-first lookup (deprecated wrapper)", () => {
  let primaryRoot: string;
  let stageRoot: string;
  const originalEnv = process.env;

  beforeEach(() => {
    primaryRoot = fs.mkdtempSync(path.join(os.tmpdir(), "primary-"));
    stageRoot = fs.mkdtempSync(path.join(os.tmpdir(), "stage-"));
    vi.mocked(vscode.workspace).workspaceFolders = [
      { uri: { fsPath: primaryRoot } } as vscode.WorkspaceFolder,
    ];
    process.env = { ...originalEnv };
    delete process.env.NIGHTGAUGE_SUPERCHARGE;
    delete process.env.NIGHTGAUGE_PERFORMANCE_MODE;
  });

  afterEach(() => {
    fs.rmSync(primaryRoot, { recursive: true, force: true });
    fs.rmSync(stageRoot, { recursive: true, force: true });
    process.env = originalEnv;
  });

  it("returns true when the primary workspace is set to Maximum, even if stage root has no file", () => {
    writePerformanceMode(primaryRoot, "maximum");
    expect(isSuperchargeModeActive(stageRoot)).toBe(true);
  });

  it("returns false for Elevated mode in the primary workspace", () => {
    writePerformanceMode(primaryRoot, "elevated");
    expect(isSuperchargeModeActive(stageRoot)).toBe(false);
  });

  it("returns false for Efficiency mode in the primary workspace", () => {
    writePerformanceMode(primaryRoot, "efficiency");
    expect(isSuperchargeModeActive(stageRoot)).toBe(false);
  });

  it("honors the legacy env var override regardless of state files", () => {
    writePerformanceMode(primaryRoot, "elevated");
    process.env.NIGHTGAUGE_PERFORMANCE_MODE = "maximum";
    expect(isSuperchargeModeActive(stageRoot)).toBe(true);
  });

  it("falls back to the passed-in workspaceRoot when there is no primary workspace", () => {
    vi.mocked(vscode.workspace).workspaceFolders = undefined;
    writePerformanceMode(stageRoot, "maximum");
    expect(isSuperchargeModeActive(stageRoot)).toBe(true);
  });
});
