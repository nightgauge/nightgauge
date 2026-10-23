/**
 * HeadlessOrchestrator.toNightgaugeAdapter.test.ts
 *
 * Pins the VSCode `ExecutionAdapter` ↔ SDK `NightgaugeAdapter` mapping used by
 * the pipeline-start adapter auth pre-flight (Issue #3222).
 *
 * The risk this guards: a new ExecutionAdapter value gets added without a
 * matching `NightgaugeAdapter` mapping branch, and the pre-flight silently
 * probes the wrong adapter (or fails the build).
 */

import { describe, it, expect, vi } from "vitest";

// Minimal vscode mock — toNightgaugeAdapter does not touch vscode but the
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

import { toNightgaugeAdapter } from "../../src/services/HeadlessOrchestrator";

describe("toNightgaugeAdapter", () => {
  it("maps claude → claude-headless even when ANTHROPIC_API_KEY is set", () => {
    expect(toNightgaugeAdapter("claude", { ANTHROPIC_API_KEY: "present" })).toBe("claude-headless");
  });

  it("maps claude → claude-headless when ANTHROPIC_API_KEY is unset", () => {
    expect(toNightgaugeAdapter("claude", {})).toBe("claude-headless");
  });

  it("maps codex → codex (identity)", () => {
    expect(toNightgaugeAdapter("codex", {})).toBe("codex");
  });

  it("maps gemini → gemini (identity)", () => {
    expect(toNightgaugeAdapter("gemini", {})).toBe("gemini");
  });

  it("maps gemini-sdk → gemini-sdk (identity)", () => {
    expect(toNightgaugeAdapter("gemini-sdk", {})).toBe("gemini-sdk");
  });

  it("maps openai-compatible → openai-compatible (identity)", () => {
    expect(toNightgaugeAdapter("openai-compatible", {})).toBe("openai-compatible");
  });

  it("maps copilot → copilot (identity)", () => {
    expect(toNightgaugeAdapter("copilot", {})).toBe("copilot");
    expect(toNightgaugeAdapter("grok", {})).toBe("grok");
  });

  it("maps opencode → opencode (identity, #1623)", () => {
    expect(toNightgaugeAdapter("opencode", {})).toBe("opencode");
  });
});
