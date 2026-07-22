/**
 * Tests for PipelineOrchestrator.selectExecutor — the ONE selection point (#3913).
 *
 * Proves the single decision point routes a stage to the multi-agent
 * `WorkflowExecutor` fan-out vs. the single-agent `StageExecutor` path based on
 * the stage SKILL's `orchestration:` frontmatter + the resolved orchestration
 * config:
 *  - orchestration disabled (the default) → single-agent,
 *  - no orchestration-capable adapter/bindings → single-agent (AC4 downgrade),
 *  - a frontmatter-bearing stage with an adapter + bindings → workflow, with a
 *    WorkflowSpec compiled from the frontmatter and the budget cap folded in,
 *  - a stage with no orchestration block → single-agent,
 *  - pr-create / pr-merge are ALWAYS single-agent, even with a block present,
 *  - and the spec is clamped to the config caps so a synthetic huge fan-out
 *    cannot exceed the run budget / hard ceiling.
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as path from "node:path";
import * as fs from "node:fs/promises";
import * as os from "node:os";
import {
  PipelineOrchestrator,
  type PipelineConfig,
} from "../../orchestrator/PipelineOrchestrator.js";
import { clampSpecCeiling, type JournalFs } from "../../orchestrator/WorkflowExecutor.js";
import {
  zeroUsage,
  type WorkflowExecutorBindings,
  type WorkflowAgentUsage,
} from "../../cli/workflow/index.js";
import { createSuccessQueryFn } from "../integration/helpers/query-mocks.js";
import type { ICliAdapter, IncrediAdapter } from "../../cli/adapters/ICliAdapter.js";

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

const usage = (): WorkflowAgentUsage => ({ ...zeroUsage(true), inputTokens: 10, outputTokens: 5 });

class FakeFs implements JournalFs {
  files = new Map<string, string>();
  async mkdir(): Promise<void> {}
  async appendFile(file: string, data: string): Promise<void> {
    this.files.set(file, (this.files.get(file) ?? "") + data);
  }
  async readFile(file: string): Promise<string | null> {
    return this.files.get(file) ?? null;
  }
}

function fakeAdapter(capability: "native-workflow" | "sdk-fanout" = "sdk-fanout"): ICliAdapter {
  return {
    name: "claude-sdk" as IncrediAdapter,
    displayName: "Fake",
    cliCommand: "fake",
    agentic: true,
    async validateAuth() {
      return "passed";
    },
    async createQueryFunction() {
      return (async function* () {})() as never;
    },
    getDefaultArgs() {
      return [];
    },
    getOrchestrationCapability() {
      return capability;
    },
    requiresDirectApiKey() {
      return false;
    },
  };
}

function passingBindings(): WorkflowExecutorBindings {
  return {
    async runAgent() {
      return { usage: usage(), terminalKind: "success" as const };
    },
    async runJudge() {
      return { verdict: "pass" as const, usage: usage() };
    },
  };
}

/** A SKILL.md with an orchestration fan-out block. */
const FANOUT_SKILL = `---
name: stage
orchestration:
  mode: fanout
  phase: review
  ceiling: fanout
  units:
    - id: a
      role: reviewer
      promptRef: ref.md
    - id: b
      role: reviewer
      promptRef: ref.md
---

# Body
`;

/** A SKILL.md with NO orchestration block. */
const PLAIN_SKILL = `---
name: stage
allowed-tools: Read Write
---

# Body
`;

const SKILL_DIRS: Record<string, string> = {
  "issue-pickup": "nightgauge-issue-pickup",
  "feature-planning": "nightgauge-feature-planning",
  "feature-dev": "nightgauge-feature-dev",
  "feature-validate": "nightgauge-feature-validate",
  "pr-create": "nightgauge-pr-create",
  "pr-merge": "nightgauge-pr-merge",
};

/**
 * Build a temp skills workspace. `fanoutStages` get the fan-out block; the rest
 * get a plain block.
 */
async function makeWorkspace(fanoutStages: string[]): Promise<{
  dir: string;
  cleanup: () => Promise<void>;
}> {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "select-executor-"));
  for (const [stage, skillDir] of Object.entries(SKILL_DIRS)) {
    const d = path.join(dir, "skills", skillDir);
    await fs.mkdir(d, { recursive: true });
    const content = fanoutStages.includes(stage) ? FANOUT_SKILL : PLAIN_SKILL;
    await fs.writeFile(path.join(d, "SKILL.md"), content, "utf-8");
  }
  await fs.mkdir(path.join(dir, ".nightgauge", "pipeline"), { recursive: true });
  return { dir, cleanup: () => fs.rm(dir, { recursive: true, force: true }) };
}

/** Orchestrator wired with the temp workspace + (optionally) a fan-out backend. */
function makeOrchestrator(
  workspaceDir: string,
  over: Partial<PipelineConfig> = {}
): PipelineOrchestrator {
  return new PipelineOrchestrator(createSuccessQueryFn(), {
    cwd: workspaceDir,
    skillsPath: path.join(workspaceDir, "skills"),
    contextPath: path.join(workspaceDir, ".nightgauge", "pipeline"),
    ...over,
  });
}

// A backend wiring that makes fan-out actually selectable.
function withBackend(): Partial<PipelineConfig> {
  return {
    workflowAdapter: fakeAdapter(),
    workflowBindings: passingBindings(),
    workflowJournalFs: new FakeFs(),
  };
}

describe("PipelineOrchestrator.selectExecutor", () => {
  let ws: { dir: string; cleanup: () => Promise<void> };

  afterEach(async () => {
    if (ws) await ws.cleanup();
  });

  describe("gating: orchestration OFF by default", () => {
    beforeEach(async () => {
      ws = await makeWorkspace(["feature-dev"]);
    });

    it("disabled (default) → single-agent even with a fan-out block + backend present", async () => {
      const orch = makeOrchestrator(ws.dir, withBackend());
      const sel = await orch.selectExecutor("feature-dev", 42);
      expect(sel.kind).toBe("single-agent");
    });

    it("disabled via env kill-switch → single-agent even when config enables it", async () => {
      const orch = makeOrchestrator(ws.dir, {
        ...withBackend(),
        orchestration: { disabled: false },
      });
      // Force the kill-switch through the resolved config: re-create with env set.
      const prev = process.env.CLAUDE_CODE_DISABLE_WORKFLOWS;
      process.env.CLAUDE_CODE_DISABLE_WORKFLOWS = "true";
      try {
        const killed = makeOrchestrator(ws.dir, {
          ...withBackend(),
          orchestration: { disabled: false },
        });
        const sel = await killed.selectExecutor("feature-dev", 42);
        expect(sel.kind).toBe("single-agent");
      } finally {
        if (prev === undefined) delete process.env.CLAUDE_CODE_DISABLE_WORKFLOWS;
        else process.env.CLAUDE_CODE_DISABLE_WORKFLOWS = prev;
      }
      // Sanity: the non-killed orchestrator is unaffected here (env was scoped).
      void orch;
    });
  });

  describe("gating: orchestration ON", () => {
    beforeEach(async () => {
      ws = await makeWorkspace(["feature-dev", "pr-create", "pr-merge"]);
    });

    it("a fan-out stage + adapter + bindings → WorkflowExecutor with a compiled spec", async () => {
      const orch = makeOrchestrator(ws.dir, {
        ...withBackend(),
        orchestration: { disabled: false },
      });
      const sel = await orch.selectExecutor("feature-dev", 42);
      expect(sel.kind).toBe("workflow");
      if (sel.kind !== "workflow") throw new Error("expected workflow");
      expect(sel.spec.stage).toBe("feature-dev");
      expect(sel.spec.phases[0].agents.map((a) => a.agentId)).toEqual(["a", "b"]);
      expect(sel.executor).toBeDefined();
    });

    it("a fan-out stage with NO adapter/bindings → single-agent (AC4 graceful downgrade)", async () => {
      const orch = makeOrchestrator(ws.dir, { orchestration: { disabled: false } });
      const sel = await orch.selectExecutor("feature-dev", 42);
      expect(sel.kind).toBe("single-agent");
    });

    it("a stage with no orchestration block → single-agent", async () => {
      const orch = makeOrchestrator(ws.dir, {
        ...withBackend(),
        orchestration: { disabled: false },
      });
      // feature-validate got the PLAIN skill in this workspace.
      const sel = await orch.selectExecutor("feature-validate", 42);
      expect(sel.kind).toBe("single-agent");
    });

    it("pr-create is ALWAYS single-agent even with a fan-out block present", async () => {
      const orch = makeOrchestrator(ws.dir, {
        ...withBackend(),
        orchestration: { disabled: false },
      });
      const sel = await orch.selectExecutor("pr-create", 42);
      expect(sel.kind).toBe("single-agent");
    });

    it("pr-merge is ALWAYS single-agent even with a fan-out block present", async () => {
      const orch = makeOrchestrator(ws.dir, {
        ...withBackend(),
        orchestration: { disabled: false },
      });
      const sel = await orch.selectExecutor("pr-merge", 42);
      expect(sel.kind).toBe("single-agent");
    });

    it("folds the run budget cap (max_usd) onto the compiled spec", async () => {
      const orch = makeOrchestrator(ws.dir, {
        ...withBackend(),
        orchestration: { disabled: false, max_usd: 7.5 },
      });
      const sel = await orch.selectExecutor("feature-dev", 42);
      if (sel.kind !== "workflow") throw new Error("expected workflow");
      expect(sel.spec.budgetUsd).toBe(7.5);
    });

    it("honors prefer_native_offload[stage] on the compiled spec", async () => {
      const orch = makeOrchestrator(ws.dir, {
        ...withBackend(),
        orchestration: { disabled: false, prefer_native_offload: { "feature-dev": true } },
      });
      const sel = await orch.selectExecutor("feature-dev", 42);
      if (sel.kind !== "workflow") throw new Error("expected workflow");
      expect(sel.spec.preferNativeOffload).toBe(true);
    });
  });

  describe("runStage wires the selection point end-to-end", () => {
    it("runStage drives the fan-out backend (not the single-agent StageExecutor) for a workflow stage", async () => {
      ws = await makeWorkspace(["feature-dev"]);
      let agentRuns = 0;
      const bindings: WorkflowExecutorBindings = {
        async runAgent() {
          agentRuns++;
          return { usage: usage(), terminalKind: "success" as const };
        },
        async runJudge() {
          return { verdict: "pass" as const, usage: usage() };
        },
      };
      const orch = makeOrchestrator(ws.dir, {
        workflowAdapter: fakeAdapter(),
        workflowBindings: bindings,
        workflowJournalFs: new FakeFs(),
        orchestration: { disabled: false },
      });

      const result = await orch.runStage("feature-dev", 42);
      expect(result.success).toBe(true);
      // The fan-out ran the two units; the single-agent path would run zero agents.
      expect(agentRuns).toBe(2);
    });

    it("runStage keeps the single-agent path (zero fan-out) when orchestration is off", async () => {
      ws = await makeWorkspace(["feature-dev"]);
      let agentRuns = 0;
      const bindings: WorkflowExecutorBindings = {
        async runAgent() {
          agentRuns++;
          return { usage: usage(), terminalKind: "success" as const };
        },
        async runJudge() {
          return { verdict: "pass" as const, usage: usage() };
        },
      };
      const orch = makeOrchestrator(ws.dir, {
        workflowAdapter: fakeAdapter(),
        workflowBindings: bindings,
        workflowJournalFs: new FakeFs(),
        // disabled defaults to true → single-agent.
      });

      const result = await orch.runStage("feature-dev", 42);
      expect(result.success).toBe(true);
      expect(agentRuns).toBe(0);
    });
  });

  describe("budget + ceiling clamp", () => {
    it("a synthetic huge fan-out is clamped to the config caps, never exceeding them", async () => {
      // The selection point compiles the spec; the executor clamps the ceiling.
      // Prove a synthetic 1000-agent spec cannot exceed a tight config cap.
      ws = await makeWorkspace(["feature-dev"]);
      const orch = makeOrchestrator(ws.dir, {
        ...withBackend(),
        orchestration: { disabled: false, max_agents: 4, max_concurrency: 2 },
      });
      const sel = await orch.selectExecutor("feature-dev", 42);
      if (sel.kind !== "workflow") throw new Error("expected workflow");
      const synthetic = {
        ...sel.spec,
        ceiling: { maxConcurrent: 16, maxTotal: 1000 },
      };
      const clamped = clampSpecCeiling(synthetic, {
        max_agents: 4,
        max_concurrency: 2,
      });
      expect(clamped.ceiling.maxTotal).toBe(4);
      expect(clamped.ceiling.maxConcurrent).toBe(2);
    });
  });
});
