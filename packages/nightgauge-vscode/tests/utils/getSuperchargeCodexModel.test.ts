/**
 * getSuperchargeCodexModel.test.ts
 *
 * Verifies the resolution order for the supercharge Codex model:
 *   env var → config override → dynamic Codex catalog → undefined (fallback).
 *
 * The dynamic-discovery path reads `~/.codex/models_cache.json` via
 * CodexModelCatalogService so the pipeline picks whichever top-tier model
 * the user's plan entitles them to — no code edits when OpenAI ships a
 * successor.
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

const mockListModels = vi.fn<() => string[]>(() => []);

vi.mock("../../src/services/CodexModelCatalogService", () => ({
  CodexModelCatalogService: class {
    listModels(): string[] {
      return mockListModels();
    }
  },
}));

import * as vscode from "vscode";
import { getSuperchargeCodexModel } from "../../src/utils/resolvers/monitoringResolver";

describe("getSuperchargeCodexModel — resolution order", () => {
  let workspaceRoot: string;
  const originalEnv = process.env;

  beforeEach(() => {
    workspaceRoot = fs.mkdtempSync(path.join(os.tmpdir(), "sc-codex-"));
    vi.mocked(vscode.workspace).workspaceFolders = [
      { uri: { fsPath: workspaceRoot } } as vscode.WorkspaceFolder,
    ];
    process.env = { ...originalEnv };
    delete process.env.NIGHTGAUGE_SUPERCHARGE_CODEX_MODEL;
    mockListModels.mockReset();
    mockListModels.mockReturnValue([]);
  });

  afterEach(() => {
    fs.rmSync(workspaceRoot, { recursive: true, force: true });
    process.env = originalEnv;
  });

  it("returns env var override when set, ignoring both config and catalog", () => {
    process.env.NIGHTGAUGE_SUPERCHARGE_CODEX_MODEL = "gpt-99-experimental";
    mockListModels.mockReturnValue(["gpt-5.5", "gpt-5.4"]);

    expect(getSuperchargeCodexModel(workspaceRoot)).toBe("gpt-99-experimental");
    expect(mockListModels).not.toHaveBeenCalled();
  });

  it("returns pipeline.supercharge.codex_model from config when no env var", () => {
    fs.mkdirSync(path.join(workspaceRoot, ".nightgauge"), { recursive: true });
    fs.writeFileSync(
      path.join(workspaceRoot, ".nightgauge", "config.yaml"),
      "pipeline:\n  supercharge:\n    codex_model: gpt-my-preferred\n",
      "utf-8"
    );
    mockListModels.mockReturnValue(["gpt-5.5"]);

    expect(getSuperchargeCodexModel(workspaceRoot)).toBe("gpt-my-preferred");
    expect(mockListModels).not.toHaveBeenCalled();
  });

  it("falls back to the top-priority model from the Codex catalog when no override is set", () => {
    mockListModels.mockReturnValue(["gpt-5.5", "gpt-5.4", "gpt-5.4-mini"]);

    expect(getSuperchargeCodexModel(workspaceRoot)).toBe("gpt-5.5");
  });

  it("returns undefined when env, config, and catalog are all empty (caller uses static default)", () => {
    mockListModels.mockReturnValue([]);

    expect(getSuperchargeCodexModel(workspaceRoot)).toBeUndefined();
  });
});
