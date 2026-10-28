import { describe, it, expect } from "vitest";
import {
  StageMatrixRowSchema,
  redactSecrets,
  renderMarkdownTable,
  runStageMatrix,
  supportedControlsFor,
  toJsonl,
  type StageMatrixOptions,
  type StageMatrixRecordReader,
} from "../../src/eval/stageMatrix.js";
import { PIPELINE_SKILLS } from "../../src/eval/schemas.js";

const NOW = () => new Date("2026-10-28T00:00:00Z");

function opts(over: Partial<StageMatrixOptions>): StageMatrixOptions {
  return {
    adapters: [],
    issueNumber: 7,
    repo: "nightgauge/fixture",
    timeoutMs: 1000,
    run: async () => {
      throw new Error("run must not be called");
    },
    readRecord: async () => undefined,
    probe: async () => {
      throw new Error("probe must not be called");
    },
    now: NOW,
    ...over,
  };
}

const completeRecord: StageMatrixRecordReader = async (_adapter, issue) => ({
  issue_number: issue,
  run_id: "run-1",
  branch: "feat/7-x",
  duration_ms: 5000,
  stages: Object.fromEntries(
    PIPELINE_SKILLS.map((s) => [
      s,
      { status: "complete", model_selection: { model: "gpt-5-codex", effort: "medium" } },
    ])
  ),
  tokens: { per_stage: { "feature-dev": { input: 100, output: 20 } } },
});

describe("runStageMatrix", () => {
  it.each(["ollama", "lm-studio"])(
    "records six agentic-gate rows for %s without probing",
    async (a) => {
      const rows = await runStageMatrix(opts({ adapters: [a] }));
      expect(rows).toHaveLength(6);
      expect(rows.map((r) => r.stage)).toEqual([...PIPELINE_SKILLS]);
      for (const r of rows) {
        expect(r).toMatchObject({ status: "skipped", skip_reason: "agentic-gate", adapter: a });
      }
    }
  );

  it("records six adapter-unavailable rows with the probe's reason when the CLI is missing", async () => {
    const rows = await runStageMatrix(
      opts({
        adapters: ["gemini"],
        probe: async () => ({ available: false, reason: "gemini not on PATH" }),
      })
    );
    expect(rows).toHaveLength(6);
    for (const r of rows) {
      expect(r).toMatchObject({
        status: "skipped",
        skip_reason: "adapter-unavailable",
        reason: "gemini not on PATH",
      });
    }
  });

  it("emits one pass row per stage from the run record, with identity, tokens and controls", async () => {
    const calls: string[] = [];
    const rows = await runStageMatrix(
      opts({
        adapters: ["codex"],
        probe: async () => ({ available: true }),
        run: async ({ adapter }) => {
          calls.push(adapter);
          return { code: 0, timedOut: false, stdout: "", stderr: "" };
        },
        readRecord: completeRecord,
      })
    );
    expect(calls).toEqual(["codex"]);
    expect(rows).toHaveLength(6);
    expect(rows.every((r) => r.status === "pass" && r.model_identity === "gpt-5-codex")).toBe(true);
    const dev = rows.find((r) => r.stage === "feature-dev")!;
    expect(dev.tokens_available).toBe(true);
    expect(dev.evidence).toMatchObject({ run_id: "run-1", input_tokens: 100, output_tokens: 20 });
    expect(rows.find((r) => r.stage === "pr-merge")!.tokens_available).toBe(false);
    expect(dev.supported_controls).toEqual({ model: true, effort: true, reasoning: false });
  });

  it("turns a failed run with no record into six fail rows, never a throw", async () => {
    const rows = await runStageMatrix(
      opts({
        adapters: ["codex"],
        probe: async () => ({ available: true }),
        run: async () => {
          throw new Error("spawn ENOENT");
        },
      })
    );
    expect(rows).toHaveLength(6);
    for (const r of rows) {
      expect(r.status).toBe("fail");
      expect(r.reason).toMatch(/spawn ENOENT/);
    }
  });

  it("redacts credentials from every field written to the report", async () => {
    const leaky =
      "auth failed sk-abcdef1234567890 ghp_ABCDEFGH12345678 xoxb-1234-5678-abcdefgh github_pat_ABCDEFGH_123";
    const rows = await runStageMatrix(
      opts({
        adapters: ["codex", "gemini"],
        probe: async (a) =>
          a === "gemini" ? { available: false, reason: leaky } : { available: true },
        run: async () => ({ code: 1, timedOut: false, stdout: "", stderr: leaky }),
      })
    );
    const jsonl = toJsonl(rows);
    expect(jsonl.match(/sk-|ghp_|xoxb-|github_pat_/g)).toBeNull();
    expect(jsonl).toContain("[REDACTED]");
  });
});

describe("helpers", () => {
  it("redactSecrets leaves ordinary text alone", () => {
    expect(redactSecrets("stage feature-dev failed")).toBe("stage feature-dev failed");
    expect(redactSecrets("Authorization: Bearer abc.def.ghi123")).toBe("Authorization: [REDACTED]");
  });

  it("supportedControlsFor reflects the real knobs", () => {
    expect(supportedControlsFor("claude")).toEqual({ model: true, effort: true, reasoning: true });
    expect(supportedControlsFor("gemini")).toEqual({
      model: true,
      effort: false,
      reasoning: false,
    });
    expect(supportedControlsFor("copilot")).toEqual({
      model: true,
      effort: false,
      reasoning: false,
    });
    expect(supportedControlsFor("ollama")).toEqual({
      model: false,
      effort: false,
      reasoning: false,
    });
  });

  it("toJsonl emits schema-valid lines and the table has one row per adapter", async () => {
    const rows = await runStageMatrix(opts({ adapters: ["ollama"] }));
    const lines = toJsonl(rows).trim().split("\n");
    expect(lines).toHaveLength(6);
    for (const l of lines) StageMatrixRowSchema.parse(JSON.parse(l));
    const table = renderMarkdownTable(rows);
    expect(table).toContain("| ollama | skipped: agentic-gate |");
    expect(table.trim().split("\n")).toHaveLength(3);
  });
});
