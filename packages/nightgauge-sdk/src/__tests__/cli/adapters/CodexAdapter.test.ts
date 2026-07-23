/**
 * CodexAdapter unit tests.
 *
 * Covers capability declarations, ephemeral stage routing, and version
 * comparison logic.
 *
 * @see Issue #2587 — Codex adapter feature parity audit
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { EventEmitter } from "node:events";
import { spawn } from "node:child_process";
import { CodexAdapter, isEphemeralStage } from "../../../cli/adapters/CodexAdapter.js";

// Module-level mock — hoisted by Vitest so createCliQueryFn's internal spawn call is intercepted.
// ESM module-scope calls cannot be intercepted with vi.spyOn; mock the dependency directly.
// Pattern from codexSessionResume.test.ts.
vi.mock("node:child_process", () => ({ spawn: vi.fn() }));

const spawnMock = vi.mocked(spawn);

// ---------------------------------------------------------------------------
// Spawn mock helpers — pattern from codexSessionResume.test.ts
// ---------------------------------------------------------------------------

function threadStartedLine(threadId: string): string {
  return JSON.stringify({ type: "thread.started", thread_id: threadId });
}

function agentMessageLine(text: string): string {
  return JSON.stringify({
    type: "item.completed",
    item: { id: "item_1", type: "agent_message", text },
  });
}

function turnCompletedLine(): string {
  return JSON.stringify({ type: "turn.completed" });
}

function makeSuccessStdout(threadId?: string): string {
  const lines: string[] = [];
  if (threadId) lines.push(threadStartedLine(threadId));
  lines.push(agentMessageLine("Stage complete."));
  lines.push(turnCompletedLine());
  return lines.join("\n");
}

function makeChildProcess(stdout: string, exitCode = 0) {
  const child = new EventEmitter() as any;
  child.stdout = new EventEmitter();
  child.stderr = new EventEmitter();
  child.stdin = { write: vi.fn(), end: vi.fn() };
  process.nextTick(() => {
    child.stdout.emit("data", Buffer.from(stdout));
    child.emit("close", exitCode);
  });
  return child;
}

function mockSpawnReturning(stdout: string): void {
  spawnMock.mockImplementation(() => makeChildProcess(stdout) as any);
}

function lastSpawnArgs(): string[] {
  const calls = spawnMock.mock.calls;
  if (calls.length === 0) return [];
  return calls[calls.length - 1][1] as string[];
}

// ---------------------------------------------------------------------------
// isEphemeralStage()
// ---------------------------------------------------------------------------

describe("isEphemeralStage()", () => {
  afterEach(() => {
    delete process.env.NIGHTGAUGE_CODEX_EPHEMERAL;
    delete process.env.NIGHTGAUGE_CODEX_EPHEMERAL_STAGES;
  });

  it("returns false for undefined stage", () => {
    expect(isEphemeralStage(undefined)).toBe(false);
  });

  it("returns false for empty string", () => {
    expect(isEphemeralStage("")).toBe(false);
  });

  describe("default ephemeral stages", () => {
    it.each(["issue-pickup", "feature-validate", "pr-create", "pr-merge"])(
      "returns true for default ephemeral stage: %s",
      (stage) => {
        expect(isEphemeralStage(stage)).toBe(true);
      }
    );

    it.each(["feature-planning", "feature-dev"])(
      "returns false for non-ephemeral stage: %s",
      (stage) => {
        expect(isEphemeralStage(stage)).toBe(false);
      }
    );
  });

  describe("NIGHTGAUGE_CODEX_EPHEMERAL=true override", () => {
    it("makes all stages ephemeral when set to true", () => {
      process.env.NIGHTGAUGE_CODEX_EPHEMERAL = "true";
      expect(isEphemeralStage("feature-planning")).toBe(true);
      expect(isEphemeralStage("feature-dev")).toBe(true);
    });

    it("makes all stages ephemeral when set to 1", () => {
      process.env.NIGHTGAUGE_CODEX_EPHEMERAL = "1";
      expect(isEphemeralStage("feature-planning")).toBe(true);
    });

    it("does not override when set to false", () => {
      process.env.NIGHTGAUGE_CODEX_EPHEMERAL = "false";
      expect(isEphemeralStage("feature-planning")).toBe(false);
      expect(isEphemeralStage("issue-pickup")).toBe(true); // still uses default
    });
  });

  describe("NIGHTGAUGE_CODEX_EPHEMERAL_STAGES override", () => {
    it("uses the provided comma-separated list instead of defaults", () => {
      process.env.NIGHTGAUGE_CODEX_EPHEMERAL_STAGES = "feature-planning,feature-dev";
      expect(isEphemeralStage("feature-planning")).toBe(true);
      expect(isEphemeralStage("feature-dev")).toBe(true);
      // Default stages should NOT be ephemeral when overridden
      expect(isEphemeralStage("issue-pickup")).toBe(false);
    });

    it("trims whitespace from stage names", () => {
      process.env.NIGHTGAUGE_CODEX_EPHEMERAL_STAGES = " feature-planning , feature-dev ";
      expect(isEphemeralStage("feature-planning")).toBe(true);
    });

    it("returns false when stage not in the override list", () => {
      process.env.NIGHTGAUGE_CODEX_EPHEMERAL_STAGES = "pr-create";
      expect(isEphemeralStage("feature-dev")).toBe(false);
    });
  });
});

// ---------------------------------------------------------------------------
// CodexAdapter.getOrchestrationCapability()
// ---------------------------------------------------------------------------

describe("CodexAdapter.getOrchestrationCapability()", () => {
  const adapter = new CodexAdapter();

  it("declares sdk-fanout — Codex is a fan-out participant, not a native-workflow backend", () => {
    expect(adapter.getOrchestrationCapability()).toBe("sdk-fanout");
  });
});

// ---------------------------------------------------------------------------
// CodexAdapter identity and static properties
// ---------------------------------------------------------------------------

describe("CodexAdapter identity", () => {
  const adapter = new CodexAdapter();

  it("name is codex", () => {
    expect(adapter.name).toBe("codex");
  });

  it("displayName is Codex", () => {
    expect(adapter.displayName).toBe("Codex");
  });

  it("cliCommand is codex", () => {
    expect(adapter.cliCommand).toBe("codex");
  });

  it("requiresDirectApiKey returns false", () => {
    expect(adapter.requiresDirectApiKey()).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// CodexAdapter.getDefaultArgs()
// ---------------------------------------------------------------------------

describe("CodexAdapter.getDefaultArgs()", () => {
  const adapter = new CodexAdapter();

  it("includes exec, --dangerously-bypass-approvals-and-sandbox, --json (no deprecated --full-auto)", () => {
    const args = adapter.getDefaultArgs();
    expect(args).toContain("exec");
    expect(args).toContain("--dangerously-bypass-approvals-and-sandbox");
    expect(args).toContain("--json");
    expect(args).not.toContain("--full-auto");
    expect(args).not.toContain("--sandbox");
  });

  it("does not include --ephemeral by default", () => {
    // Ephemeral flag is injected at createQueryFunction() time, not in defaults
    expect(adapter.getDefaultArgs()).not.toContain("--ephemeral");
  });
});

// ---------------------------------------------------------------------------
// CodexAdapter.createQueryFunction() — ephemeral and model injection
// ---------------------------------------------------------------------------

describe("CodexAdapter.createQueryFunction()", () => {
  afterEach(() => {
    delete process.env.NIGHTGAUGE_CODEX_MODEL;
    delete process.env.NIGHTGAUGE_CODEX_EPHEMERAL;
    delete process.env.NIGHTGAUGE_CODEX_RESUME_ENABLED;
  });

  it("throws when --ephemeral and resume args conflict", async () => {
    // An ephemeral stage with RESUME_ENABLED would conflict
    process.env.NIGHTGAUGE_CODEX_CLI_ARGS = "--resume";
    const adapter = new CodexAdapter();
    await expect(adapter.createQueryFunction({ stage: "issue-pickup" })).rejects.toThrow(
      "--ephemeral and session resume cannot be used together"
    );
    delete process.env.NIGHTGAUGE_CODEX_CLI_ARGS;
  });
});

// ---------------------------------------------------------------------------
// CodexAdapter.createQueryFunction() — cross-stage session handoff
// ---------------------------------------------------------------------------

describe("CodexAdapter.createQueryFunction() — cross-stage session handoff", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("A1: stage 1 extracts session_id from thread.started event in JSONL output", async () => {
    vi.stubEnv("NIGHTGAUGE_CODEX_RESUME_ENABLED", "");
    mockSpawnReturning(makeSuccessStdout("stage1-thread-id"));

    const adapter = new CodexAdapter();
    const queryFn = await adapter.createQueryFunction({ stage: "feature-planning" });
    const messages: unknown[] = [];
    for await (const msg of queryFn({ prompt: "test", options: {} })) {
      messages.push(msg);
    }

    const resultMsg = messages.find((m) => (m as Record<string, unknown>).type === "result");
    expect(resultMsg).toBeDefined();
    expect((resultMsg as Record<string, unknown>).session_id).toBe("stage1-thread-id");
    expect(lastSpawnArgs()).not.toContain("resume");
  });

  it("A2: stage 2 uses resumeSessionId to build exec resume <threadId> args", async () => {
    vi.stubEnv("NIGHTGAUGE_CODEX_RESUME_ENABLED", "true");
    mockSpawnReturning(makeSuccessStdout());

    const adapter = new CodexAdapter();
    const queryFn = await adapter.createQueryFunction({ stage: "feature-dev" });
    for await (const _ of queryFn({
      prompt: "test",
      options: { resumeSessionId: "stage1-thread-id" },
    })) {
      /* consume */
    }

    const args = lastSpawnArgs();
    expect(args[0]).toBe("exec");
    expect(args[1]).toBe("resume");
    expect(args[2]).toBe("stage1-thread-id");
    expect(args[3]).toBe("-");
    expect(args).toContain("--dangerously-bypass-approvals-and-sandbox");
    expect(args).not.toContain("--sandbox");
  });

  it("A3: resume disabled — resumeSessionId ignored, standard exec args used", async () => {
    vi.stubEnv("NIGHTGAUGE_CODEX_RESUME_ENABLED", "");
    mockSpawnReturning(makeSuccessStdout());

    const adapter = new CodexAdapter();
    const queryFn = await adapter.createQueryFunction({ stage: "feature-dev" });
    for await (const _ of queryFn({
      prompt: "test",
      options: { resumeSessionId: "some-id" },
    })) {
      /* consume */
    }

    const STANDARD_ARGS = ["exec", "--dangerously-bypass-approvals-and-sandbox", "--json"];
    const args = lastSpawnArgs();
    expect(args.slice(0, STANDARD_ARGS.length)).toEqual(STANDARD_ARGS);
    expect(args).not.toContain("resume");
  });

  it("A4: resume enabled with no session ID falls back to exec resume --last", async () => {
    vi.stubEnv("NIGHTGAUGE_CODEX_RESUME_ENABLED", "true");
    mockSpawnReturning(makeSuccessStdout());

    const adapter = new CodexAdapter();
    const queryFn = await adapter.createQueryFunction({ stage: "feature-dev" });
    for await (const _ of queryFn({ prompt: "test", options: {} })) {
      /* consume */
    }

    const args = lastSpawnArgs();
    expect(args[1]).toBe("resume");
    expect(args[2]).toBe("--last");
  });

  it("A5: ephemeral stage uses standard exec path and injects --ephemeral (resume not triggered)", async () => {
    vi.stubEnv("NIGHTGAUGE_CODEX_RESUME_ENABLED", "");
    mockSpawnReturning(makeSuccessStdout());

    const adapter = new CodexAdapter();
    const queryFn = await adapter.createQueryFunction({ stage: "feature-validate" });
    for await (const _ of queryFn({ prompt: "test", options: {} })) {
      /* consume */
    }

    const args = lastSpawnArgs();
    expect(args).toContain("--ephemeral");
    expect(args).not.toContain("resume");
  });
});

// ---------------------------------------------------------------------------
// CodexAdapter.createQueryFunction() — ephemeral flag injection
// ---------------------------------------------------------------------------

describe("CodexAdapter.createQueryFunction() — ephemeral flag injection", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("B1: ephemeral stage (issue-pickup) injects --ephemeral into spawn args", async () => {
    vi.stubEnv("NIGHTGAUGE_CODEX_EPHEMERAL", "");
    vi.stubEnv("NIGHTGAUGE_CODEX_RESUME_ENABLED", "");
    mockSpawnReturning(makeSuccessStdout());

    const adapter = new CodexAdapter();
    const queryFn = await adapter.createQueryFunction({ stage: "issue-pickup" });
    for await (const _ of queryFn({ prompt: "test", options: {} })) {
      /* consume */
    }

    expect(lastSpawnArgs()).toContain("--ephemeral");
  });

  it("B2: non-ephemeral stage (feature-dev) does not inject --ephemeral", async () => {
    vi.stubEnv("NIGHTGAUGE_CODEX_EPHEMERAL", "");
    vi.stubEnv("NIGHTGAUGE_CODEX_RESUME_ENABLED", "");
    mockSpawnReturning(makeSuccessStdout());

    const adapter = new CodexAdapter();
    const queryFn = await adapter.createQueryFunction({ stage: "feature-dev" });
    for await (const _ of queryFn({ prompt: "test", options: {} })) {
      /* consume */
    }

    expect(lastSpawnArgs()).not.toContain("--ephemeral");
  });
});

// ---------------------------------------------------------------------------
// CodexAdapter.createQueryFunction() — model routing
// ---------------------------------------------------------------------------

describe("CodexAdapter.createQueryFunction() — model routing", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    delete process.env.NIGHTGAUGE_CODEX_MODEL;
    delete process.env.NIGHTGAUGE_CODEX_REASONING_EFFORT;
  });

  afterEach(() => {
    vi.unstubAllEnvs();
    delete process.env.NIGHTGAUGE_CODEX_MODEL;
    delete process.env.NIGHTGAUGE_CODEX_REASONING_EFFORT;
  });

  it("C1: injects --model <value> when NIGHTGAUGE_CODEX_MODEL is set", async () => {
    // Use a valid Codex model id — resolveAndValidateModel (#4021) now rejects
    // unknown ids, so a stale placeholder would (correctly) fail validation.
    vi.stubEnv("NIGHTGAUGE_CODEX_MODEL", "gpt-5.4-mini");
    vi.stubEnv("NIGHTGAUGE_CODEX_RESUME_ENABLED", "");
    mockSpawnReturning(makeSuccessStdout());

    const adapter = new CodexAdapter();
    const queryFn = await adapter.createQueryFunction({ stage: "feature-dev" });
    for await (const _ of queryFn({ prompt: "test", options: {} })) {
      /* consume */
    }

    const args = lastSpawnArgs();
    const modelIdx = args.indexOf("--model");
    expect(modelIdx).toBeGreaterThan(-1);
    expect(args[modelIdx + 1]).toBe("gpt-5.4-mini");
  });

  it("C3: throws CONFIG_INVALID when NIGHTGAUGE_CODEX_MODEL is invalid (#4021)", async () => {
    vi.stubEnv("NIGHTGAUGE_CODEX_MODEL", "o4-mini");
    vi.stubEnv("NIGHTGAUGE_CODEX_RESUME_ENABLED", "");
    mockSpawnReturning(makeSuccessStdout());

    const adapter = new CodexAdapter();
    await expect(adapter.createQueryFunction({ stage: "feature-dev" })).rejects.toThrow(
      /not valid for the Codex adapter/
    );
  });

  it("passes the configured reasoning effort to Codex", async () => {
    vi.stubEnv("NIGHTGAUGE_CODEX_MODEL", "gpt-5.6-sol");
    vi.stubEnv("NIGHTGAUGE_CODEX_REASONING_EFFORT", "max");
    vi.stubEnv("NIGHTGAUGE_CODEX_RESUME_ENABLED", "");
    mockSpawnReturning(makeSuccessStdout());

    const queryFn = await new CodexAdapter().createQueryFunction({ stage: "feature-dev" });
    for await (const _ of queryFn({ prompt: "test", options: {} })) {
      /* consume */
    }

    const args = lastSpawnArgs();
    const configIdx = args.indexOf("-c");
    expect(args[configIdx + 1]).toBe("model_reasoning_effort=max");
  });

  it("rejects an invalid reasoning effort before spawning Codex", async () => {
    vi.stubEnv("NIGHTGAUGE_CODEX_REASONING_EFFORT", "ultra");
    await expect(new CodexAdapter().createQueryFunction({ stage: "feature-dev" })).rejects.toThrow(
      /Invalid NIGHTGAUGE_CODEX_REASONING_EFFORT/
    );
  });

  it("C2: does not inject --model when NIGHTGAUGE_CODEX_MODEL is unset", async () => {
    vi.stubEnv("NIGHTGAUGE_CODEX_RESUME_ENABLED", "");
    mockSpawnReturning(makeSuccessStdout());

    const adapter = new CodexAdapter();
    const queryFn = await adapter.createQueryFunction({ stage: "feature-dev" });
    for await (const _ of queryFn({ prompt: "test", options: {} })) {
      /* consume */
    }

    expect(lastSpawnArgs()).not.toContain("--model");
  });
});
