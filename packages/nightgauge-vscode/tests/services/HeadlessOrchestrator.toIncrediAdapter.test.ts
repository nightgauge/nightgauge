/**
 * HeadlessOrchestrator.toIncrediAdapter.test.ts
 *
 * Pins the VSCode `ExecutionAdapter` ↔ SDK `IncrediAdapter` mapping used by
 * the pipeline-start adapter auth pre-flight (Issue #3222).
 *
 * The risk this guards: a new ExecutionAdapter value gets added without a
 * matching `IncrediAdapter` mapping branch, and the pre-flight silently
 * probes the wrong adapter (or fails the build).
 */

import { describe, it, expect, vi } from "vitest";

// Minimal vscode mock — toIncrediAdapter does not touch vscode but the
// transitive imports do.
vi.mock("vscode", () => ({
  Uri: { file: (p: string) => ({ fsPath: p }) },
  workspace: { workspaceFolders: [] },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  EventEmitter: class {
    fire = vi.fn();
    event = vi.fn();
    dispose = vi.fn();
  },
  Disposable: { from: vi.fn() },
}));

vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn((stage: string) => stage),
}));

import { toIncrediAdapter } from "../../src/services/HeadlessOrchestrator";

describe("toIncrediAdapter", () => {
  it("maps claude → claude-headless even when ANTHROPIC_API_KEY is set", () => {
    expect(toIncrediAdapter("claude", { ANTHROPIC_API_KEY: "present" })).toBe("claude-headless");
  });

  it("maps claude → claude-headless when ANTHROPIC_API_KEY is unset", () => {
    expect(toIncrediAdapter("claude", {})).toBe("claude-headless");
  });

  it("maps codex → codex (identity)", () => {
    expect(toIncrediAdapter("codex", {})).toBe("codex");
  });

  it("maps gemini → gemini (identity)", () => {
    expect(toIncrediAdapter("gemini", {})).toBe("gemini");
  });

  it("maps gemini-sdk → gemini-sdk (identity)", () => {
    expect(toIncrediAdapter("gemini-sdk", {})).toBe("gemini-sdk");
  });

  it("maps lm-studio → lm-studio (identity)", () => {
    expect(toIncrediAdapter("lm-studio", {})).toBe("lm-studio");
  });

  it("maps ollama → ollama (identity)", () => {
    expect(toIncrediAdapter("ollama", {})).toBe("ollama");
  });

  it("maps copilot → copilot (identity)", () => {
    expect(toIncrediAdapter("copilot", {})).toBe("copilot");
  });
});
