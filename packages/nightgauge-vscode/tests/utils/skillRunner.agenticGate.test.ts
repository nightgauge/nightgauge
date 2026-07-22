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
import { describe, it, expect, vi } from "vitest";

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

import { validateAdapterPrerequisites } from "../../src/utils/skillRunner";
import type { ExecutionAdapter } from "../../src/utils/resolvers/modelResolver";

describe("validateAdapterPrerequisites — agentic truth-gate (#57)", () => {
  it("rejects chat-completion-only adapters with remediation", () => {
    for (const adapter of ["gemini-sdk", "ollama", "lm-studio"] as ExecutionAdapter[]) {
      const err = validateAdapterPrerequisites(adapter, "/test/workspace", "headless");
      expect(err, adapter).toMatch(/chat-completion-only/);
      expect(err, adapter).toMatch(/Switch Execution Adapter/);
    }
  });

  it("rejects chat-only adapters in interactive mode too (gate precedes mode checks)", () => {
    const err = validateAdapterPrerequisites("gemini-sdk", "/test/workspace", "interactive");
    expect(err).toMatch(/chat-completion-only/);
  });

  it("passes agentic adapters through to their normal prerequisite checks", () => {
    for (const adapter of ["claude", "codex", "gemini", "copilot"] as ExecutionAdapter[]) {
      const err = validateAdapterPrerequisites(adapter, "/test/workspace", "headless");
      expect(err === null || !err.includes("chat-completion-only"), adapter).toBe(true);
    }
  });
});
