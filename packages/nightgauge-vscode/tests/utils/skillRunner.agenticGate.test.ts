/**
 * skillRunner.agenticGate.test.ts (#57)
 *
 * The agentic truth-gate in `validateAdapterPrerequisites`: chat-completion-
 * only adapters (gemini-sdk, ollama, lm-studio) are rejected for pipeline
 * dispatch with remediation, while every agentic adapter passes the gate
 * (under VITEST the downstream commandExists checks short-circuit to true).
 * The gate covers primary dispatch, the fallback walker, and auto-router
 * enumeration — all funnel through this function.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/test/workspace" } }],
  },
  window: {
    terminals: [],
    createTerminal: vi.fn(() => ({ show: vi.fn(), sendText: vi.fn() })),
    showWarningMessage: vi.fn().mockResolvedValue(undefined),
  },
  extensions: { getExtension: vi.fn(() => null) },
}));

import { isAgenticAdapter } from "@nightgauge/sdk";
import { agenticPipelineAdapters, validateAdapterPrerequisites } from "../../src/utils/skillRunner";
import type { ExecutionAdapter } from "../../src/utils/resolvers/modelResolver";
import { AdapterEnumSchema } from "../../src/config/schema";
import { openCodeGateMessage } from "../../src/utils/openCodeExperimentalGate";

describe("validateAdapterPrerequisites — agentic truth-gate (#57)", () => {
  it("rejects chat-completion-only adapters with remediation", () => {
    for (const adapter of ["gemini-sdk", "ollama", "lm-studio"] as ExecutionAdapter[]) {
      const err = validateAdapterPrerequisites(adapter, "/test/workspace", "headless");
      expect(err, adapter).toMatch(/chat-completion-only/);
      expect(err, adapter).toMatch(/Switch Execution Adapter/);
    }
  });

  it("names the agentic adapters from the SDK registry, not a hand-typed list (#1657)", () => {
    const agentic = AdapterEnumSchema.options.filter((a) => isAgenticAdapter(a));
    expect(agentic).toContain("opencode");
    expect(agentic).not.toContain("lm-studio");
    expect(agenticPipelineAdapters()).toEqual(agentic);
    const err = validateAdapterPrerequisites("ollama", "/test/workspace", "headless");
    expect(err).toContain(`(${agentic.join(", ")})`);
  });

  it("rejects chat-only adapters in interactive mode too (gate precedes mode checks)", () => {
    const err = validateAdapterPrerequisites("gemini-sdk", "/test/workspace", "interactive");
    expect(err).toMatch(/chat-completion-only/);
  });

  it("passes agentic adapters through to their normal prerequisite checks", () => {
    for (const adapter of ["claude", "codex", "gemini", "copilot", "grok"] as ExecutionAdapter[]) {
      const err = validateAdapterPrerequisites(adapter, "/test/workspace", "headless");
      expect(err === null || !err.includes("chat-completion-only"), adapter).toBe(true);
    }
  });
});

/**
 * #1657: the `opencode` prerequisites. `commandExists` short-circuits to true
 * under VITEST, so these cases turn that off and put exactly the fixture's
 * executables on PATH. The Go binary is resolvable only through PATH here:
 * the vscode mock has no configuration and no extension, so BinaryResolver's
 * filesystem tiers find nothing.
 */
describe("validateAdapterPrerequisites — opencode (#1657)", () => {
  const savedEnv = process.env;
  let root: string;

  function fixture(executables: string[]): { workspace: string } {
    const bin = path.join(root, "bin");
    fs.mkdirSync(bin, { recursive: true });
    for (const name of executables) {
      fs.writeFileSync(path.join(bin, name), "#!/bin/sh\n", { mode: 0o755 });
    }
    const workspace = path.join(root, "ws");
    const cli = path.join(workspace, "packages", "nightgauge-sdk", "dist", "cli");
    fs.mkdirSync(cli, { recursive: true });
    fs.writeFileSync(path.join(cli, "index.js"), "");
    process.env = {
      ...savedEnv,
      VITEST: "false",
      PATH: bin,
      NIGHTGAUGE_EXPERIMENTAL_OPENCODE: "1",
    };
    delete process.env.NIGHTGAUGE_GO_BINARY_PATH;
    return { workspace };
  }

  const ALL = ["opencode", "nightgauge", "node", "git", "gh"];

  beforeEach(() => {
    root = fs.mkdtempSync(path.join(os.tmpdir(), "ng-opencode-prereq-"));
  });

  afterEach(() => {
    process.env = savedEnv;
    fs.rmSync(root, { recursive: true, force: true });
  });

  it("passes with the gate on, opencode on PATH and the Go binary resolvable", () => {
    const { workspace } = fixture(ALL);
    expect(validateAdapterPrerequisites("opencode", workspace, "headless")).toBeNull();
  });

  it("refuses with the enable-gate remediation when the gate is off", () => {
    const { workspace } = fixture(ALL);
    delete process.env.NIGHTGAUGE_EXPERIMENTAL_OPENCODE;
    expect(validateAdapterPrerequisites("opencode", workspace, "headless")).toBe(
      openCodeGateMessage()
    );
    // Only the exact value 1 opens it (ADR-022 § The enable gate).
    process.env.NIGHTGAUGE_EXPERIMENTAL_OPENCODE = "true";
    expect(validateAdapterPrerequisites("opencode", workspace, "headless")).toBe(
      openCodeGateMessage()
    );
  });

  it("refuses when the opencode CLI is not on PATH", () => {
    const { workspace } = fixture(ALL.filter((n) => n !== "opencode"));
    const err = validateAdapterPrerequisites("opencode", workspace, "headless");
    expect(err).toMatch(/`opencode` CLI is not available in PATH/);
  });

  it("refuses when the Nightgauge Go binary is not resolvable", () => {
    const { workspace } = fixture(ALL.filter((n) => n !== "nightgauge"));
    const err = validateAdapterPrerequisites("opencode", workspace, "headless");
    expect(err).toMatch(/nightgauge opencode config/);
    expect(err).toMatch(/NIGHTGAUGE_GO_BINARY_PATH/);
  });

  it("interactive mode is headless-only, as ADR-022 records no interactive decision", () => {
    const { workspace } = fixture(ALL);
    const err = validateAdapterPrerequisites("opencode", workspace, "interactive");
    expect(err).toMatch(/^OpenCode adapter supports headless execution only\./);
    expect(err).toMatch(/Nightgauge: Run Stage/);
  });
});
