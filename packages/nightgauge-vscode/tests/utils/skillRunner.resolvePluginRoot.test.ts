/**
 * skillRunner.resolvePluginRoot.test.ts
 *
 * Pins the CLAUDE_PLUGIN_ROOT resolution contract that keeps the pipeline's
 * deterministic safety hooks (the push-to-main guard, workflow gate, etc.)
 * loading for EVERY repo — not just the nightgauge source repo.
 *
 * Root cause this guards against (acmeapp incident): the old logic hardcoded
 * `workspaceFolders[0]/claude-plugins/nightgauge`. When the primary repo
 * isn't nightgauge that path doesn't exist, CLAUDE_PLUGIN_ROOT pointed at
 * nothing, every hook silently failed to resolve, and a pr-merge agent pushed
 * straight to main unguarded.
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import * as fs from "fs";
import * as vscode from "vscode";
import { resolvePluginRoot } from "../../src/utils/skillRunner";

vi.mock("vscode", () => ({
  workspace: { workspaceFolders: [] as Array<{ uri: { fsPath: string } }> },
  window: { terminals: [], createTerminal: vi.fn(), showWarningMessage: vi.fn() },
  extensions: { getExtension: vi.fn(() => null) },
}));

vi.mock("fs", () => ({
  existsSync: vi.fn(),
  readFileSync: vi.fn(),
}));

vi.mock("child_process", () => ({
  spawn: vi.fn(),
  execFile: vi.fn(),
}));

const HOOKS = "hooks/hooks.json";

function setWorkspaceFolders(paths: string[]): void {
  (vscode.workspace as { workspaceFolders: Array<{ uri: { fsPath: string } }> }).workspaceFolders =
    paths.map((p) => ({ uri: { fsPath: p } }));
}

describe("resolvePluginRoot", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    setWorkspaceFolders([]);
    vi.mocked(vscode.extensions.getExtension).mockReturnValue(undefined as never);
  });

  it("prefers a live workspace claude-plugins copy when present", () => {
    setWorkspaceFolders(["/repos/nightgauge"]);
    vi.mocked(fs.existsSync).mockImplementation((p) =>
      String(p).startsWith("/repos/nightgauge/claude-plugins/nightgauge/" + HOOKS)
    );

    expect(resolvePluginRoot()).toBe("/repos/nightgauge/claude-plugins/nightgauge");
  });

  it("finds the live copy in a NON-first workspace folder (multi-root)", () => {
    setWorkspaceFolders(["/repos/acmeapp", "/repos/nightgauge"]);
    vi.mocked(fs.existsSync).mockImplementation((p) =>
      String(p).includes("/repos/nightgauge/claude-plugins/nightgauge/" + HOOKS)
    );

    expect(resolvePluginRoot()).toBe("/repos/nightgauge/claude-plugins/nightgauge");
  });

  it("falls back to the bundled extension hooks when the workspace has none (the acmeapp case)", () => {
    setWorkspaceFolders(["/repos/acmeapp"]);
    vi.mocked(vscode.extensions.getExtension).mockReturnValue({
      extensionPath: "/ext/nightgauge-vscode",
    } as never);
    // No workspace claude-plugins; only the bundled dist copy exists.
    vi.mocked(fs.existsSync).mockImplementation((p) =>
      String(p).startsWith("/ext/nightgauge-vscode/dist/claude-plugins/nightgauge/" + HOOKS)
    );

    expect(resolvePluginRoot()).toBe("/ext/nightgauge-vscode/dist/claude-plugins/nightgauge");
  });

  it("returns undefined when neither a live nor a bundled copy exists", () => {
    setWorkspaceFolders(["/repos/acmeapp"]);
    vi.mocked(vscode.extensions.getExtension).mockReturnValue({
      extensionPath: "/ext/nightgauge-vscode",
    } as never);
    vi.mocked(fs.existsSync).mockReturnValue(false);

    expect(resolvePluginRoot()).toBeUndefined();
  });

  it("does not return a workspace path that lacks hooks/hooks.json", () => {
    // Directory exists but hooks.json doesn't — must not be treated as valid.
    setWorkspaceFolders(["/repos/acmeapp"]);
    vi.mocked(fs.existsSync).mockImplementation((p) => String(p).endsWith("/claude-plugins")); // not the hooks.json
    expect(resolvePluginRoot()).toBeUndefined();
  });
});
