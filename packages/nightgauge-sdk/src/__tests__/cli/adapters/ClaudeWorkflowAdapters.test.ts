/**
 * Adapter-level tests for the Claude native runWorkflow hook (#3910) on
 * ClaudeSdkAdapter + ClaudeHeadlessAdapter.
 *
 * Proves:
 *  (a) both adapters declare `native-workflow` and expose `runWorkflow`;
 *  (b) `validateAuth` runs the native-workflow preflight WITHOUT hard-failing on
 *      a stale/undetectable workflow version (records readiness instead), so the
 *      orchestration mode downgrades to sdk-fanout but ordinary execution works;
 *  (c) the headless adapter — which previously had NO version gate — now detects
 *      the CLI version via the injected runner;
 *  (d) `runWorkflow` throws the typed downgrade signal in this (research-preview)
 *      environment, emitting nothing — the engine owns the fallback tree;
 *  (e) cross-process resume is NOT delegated to a Claude session: a
 *      `resumeSessionId` passed to `runWorkflow` never produces a `--resume` /
 *      session-journal native call. The engine journal is authoritative.
 */

import { describe, it, expect, afterEach } from "vitest";
import { ClaudeSdkAdapter } from "../../../cli/adapters/ClaudeSdkAdapter.js";
import { ClaudeHeadlessAdapter } from "../../../cli/adapters/ClaudeHeadlessAdapter.js";
import { NativeWorkflowUnavailableError } from "../../../cli/adapters/ClaudeNativeWorkflow.js";
import type { PreflightCommandRunner } from "../../../cli/codexPreflight.js";
import {
  WORKFLOW_SCHEMA_VERSION,
  ArrayWorkflowEventSink,
  type WorkflowSpec,
} from "../../../cli/workflow/index.js";

function makeSpec(over: Partial<WorkflowSpec> = {}): WorkflowSpec {
  return {
    schemaVersion: WORKFLOW_SCHEMA_VERSION,
    runId: "run-adapter-1",
    ceiling: { maxConcurrent: 16, maxTotal: 1000 },
    phases: [{ name: "find", agents: [{ agentId: "a0", prompt: "p0", provider: "claude" }] }],
    ...over,
  };
}

/** A fake preflight runner that records calls and returns canned results. */
function makeRunner(
  responses: Record<string, { code: number; stdout?: string; stderr?: string }>
): { runner: PreflightCommandRunner; calls: string[][] } {
  const calls: string[][] = [];
  const runner: PreflightCommandRunner = async (command, args) => {
    calls.push([command, ...args]);
    const key = `${command} ${args.join(" ")}`;
    const r = responses[key] ?? responses[`${command} ${args[0]}`] ?? { code: 0 };
    return { code: r.code, stdout: r.stdout ?? "", stderr: r.stderr ?? "" };
  };
  return { runner, calls };
}

// ---------------------------------------------------------------------------
// Identity / capability
// ---------------------------------------------------------------------------

describe("Claude adapters declare native-workflow + expose runWorkflow", () => {
  it("ClaudeSdkAdapter", () => {
    const a = new ClaudeSdkAdapter();
    expect(a.getOrchestrationCapability()).toBe("native-workflow");
    expect(typeof a.runWorkflow).toBe("function");
  });
  it("ClaudeHeadlessAdapter", () => {
    const a = new ClaudeHeadlessAdapter();
    expect(a.getOrchestrationCapability()).toBe("native-workflow");
    expect(typeof a.runWorkflow).toBe("function");
  });
});

// ---------------------------------------------------------------------------
// ClaudeSdkAdapter.validateAuth — preflight without hard-failing
// ---------------------------------------------------------------------------

describe("ClaudeSdkAdapter.validateAuth() native-workflow preflight", () => {
  const prevKey = process.env.ANTHROPIC_API_KEY;
  afterEach(() => {
    if (prevKey === undefined) delete process.env.ANTHROPIC_API_KEY;
    else process.env.ANTHROPIC_API_KEY = prevKey;
  });

  it("passes auth and records readiness; a stale SDK version downgrades (does not throw)", async () => {
    process.env.ANTHROPIC_API_KEY = "sk-test";
    const adapter = new ClaudeSdkAdapter();
    // The pinned Agent SDK (0.3.x) is below the 2.1.154 floor → not ready, but
    // auth still passes (research-preview backend is never an auth gate).
    await expect(adapter.validateAuth()).resolves.toBe("passed");
    expect(adapter.nativeWorkflowReadiness).toBeDefined();
    expect(adapter.nativeWorkflowReadiness!.ready).toBe(false);
    expect(adapter.nativeWorkflowReadiness!.reason).toBe("version-below-floor");
  });

  it("still hard-fails auth when the API key is missing", async () => {
    delete process.env.ANTHROPIC_API_KEY;
    const adapter = new ClaudeSdkAdapter();
    await expect(adapter.validateAuth()).rejects.toThrow(/No Anthropic API key/);
  });
});

// ---------------------------------------------------------------------------
// ClaudeHeadlessAdapter.validateAuth — version gate via injected runner
// ---------------------------------------------------------------------------

describe("ClaudeHeadlessAdapter.validateAuth() native-workflow version gate", () => {
  it("detects a stale CLI version and records a version-below-floor downgrade (auth still passes)", async () => {
    const { runner, calls } = makeRunner({
      "claude --version": { code: 0, stdout: "2.1.100 (Claude Code)" },
      "claude auth status": { code: 0, stdout: "Logged in" },
    });
    const adapter = new ClaudeHeadlessAdapter();
    await expect(adapter.validateAuth({ runner, cwd: "/tmp" })).resolves.toBe("passed");
    expect(adapter.nativeWorkflowReadiness!.ready).toBe(false);
    expect(adapter.nativeWorkflowReadiness!.reason).toBe("version-below-floor");
    expect(adapter.nativeWorkflowReadiness!.detectedVersion).toBe("2.1.100");
    // The version gate actually probed `claude --version`.
    expect(calls.some((c) => c[0] === "claude" && c[1] === "--version")).toBe(true);
  });

  it("marks ready when the CLI meets the floor", async () => {
    const { runner } = makeRunner({
      "claude --version": { code: 0, stdout: "2.1.200 (Claude Code)" },
      "claude auth status": { code: 0, stdout: "Logged in" },
    });
    const adapter = new ClaudeHeadlessAdapter();
    await adapter.validateAuth({ runner, cwd: "/tmp" });
    expect(adapter.nativeWorkflowReadiness!.ready).toBe(true);
    expect(adapter.nativeWorkflowReadiness!.detectedVersion).toBe("2.1.200");
  });

  it("without a runner, skips preflight and passes (SDK direct usage)", async () => {
    const adapter = new ClaudeHeadlessAdapter();
    await expect(adapter.validateAuth()).resolves.toBe("passed");
  });
});

// ---------------------------------------------------------------------------
// runWorkflow downgrade signal (research-preview environment)
// ---------------------------------------------------------------------------

describe("Claude adapters runWorkflow() downgrade", () => {
  it("ClaudeSdkAdapter.runWorkflow throws the typed downgrade and emits nothing", async () => {
    const adapter = new ClaudeSdkAdapter();
    const sink = new ArrayWorkflowEventSink();
    const err = await adapter.runWorkflow(makeSpec(), sink).catch((e) => e);
    expect(err).toBeInstanceOf(NativeWorkflowUnavailableError);
    expect(sink.getEvents()).toHaveLength(0);
  });

  it("ClaudeHeadlessAdapter.runWorkflow throws the typed downgrade and emits nothing", async () => {
    const adapter = new ClaudeHeadlessAdapter();
    const sink = new ArrayWorkflowEventSink();
    // Point the version probe at a binary that does not exist so detection fails
    // → version-undetectable downgrade (deterministic, no real `claude` needed).
    const prev = process.env.NIGHTGAUGE_CLAUDE_CLI_COMMAND;
    process.env.NIGHTGAUGE_CLAUDE_CLI_COMMAND = "claude-nonexistent-binary-3910";
    try {
      const err = await adapter.runWorkflow(makeSpec(), sink).catch((e) => e);
      expect(err).toBeInstanceOf(NativeWorkflowUnavailableError);
      expect(sink.getEvents()).toHaveLength(0);
    } finally {
      if (prev === undefined) delete process.env.NIGHTGAUGE_CLAUDE_CLI_COMMAND;
      else process.env.NIGHTGAUGE_CLAUDE_CLI_COMMAND = prev;
    }
  });

  it("never delegates cross-process resume to a Claude session — resumeSessionId yields no session journal call", async () => {
    // The engine journal (#3908) is authoritative. Passing a resumeSessionId
    // must NOT cause runWorkflow to resume a same-session Claude journal: in this
    // environment it downgrades cleanly with no native call at all.
    const adapter = new ClaudeSdkAdapter();
    const sink = new ArrayWorkflowEventSink();
    const err = await adapter
      .runWorkflow(makeSpec(), sink, { resumeSessionId: "claude-session-xyz" })
      .catch((e) => e);
    expect(err).toBeInstanceOf(NativeWorkflowUnavailableError);
    // No tree emitted → the run is owned by the engine + SdkFanoutRunner, never a
    // Claude session resume.
    expect(sink.getEvents()).toHaveLength(0);
  });
});
