/**
 * Orchestrator crashes are real terminal failures, not phantom backup writes
 * (#447).
 *
 * The crash input is the verbatim Go capture produced through startup crash
 * recovery for #397. The paired phantom fixture has the shape the existing
 * filters are meant to suppress, but no synthesized-record marker. These
 * tests pin the operator decision: positive identification by
 * terminal_failure_kind admits only the crash; the shape guard stays intact.
 */

import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { TelemetryStore } from "../../../src/services/TelemetryStore";
import { ExecutionHistoryReader } from "../../../src/utils/executionHistoryReader";
import { getProgressBarHtml } from "../../../src/views/dashboard/DashboardComponents";
import { DashboardState } from "../../../src/views/dashboard/DashboardState";
import { getHistoryHtml } from "../../../src/views/dashboard/tabs/PipelineTabHtml";
import { createMockMemento } from "../../mocks/memento";

const FIXTURE_ROOT = join(__dirname, "..", "..", "fixtures");
const CRASH_LINE = readFileSync(
  join(FIXTURE_ROOT, "undetermined-branch", "crash-record.jsonl"),
  "utf-8"
).trim();
const COMPLETED_LINE = readFileSync(
  join(FIXTURE_ROOT, "undetermined-branch", "completed-run-record.jsonl"),
  "utf-8"
).trim();
const PHANTOM_LINE = readFileSync(
  join(FIXTURE_ROOT, "orchestrator-crash", "phantom-record.jsonl"),
  "utf-8"
).trim();

describe("orchestrator-crash history records (#447)", () => {
  let root: string;

  beforeEach(() => {
    ExecutionHistoryReader.clearCache();
    root = mkdtempSync(join(tmpdir(), "ng-orchestrator-crash-"));
    mkdirSync(join(root, ".nightgauge", "pipeline", "history"), { recursive: true });
  });

  afterEach(() => {
    ExecutionHistoryReader.clearCache();
    rmSync(root, { recursive: true, force: true });
  });

  function writeBothPopulations(): void {
    writeFileSync(
      join(root, ".nightgauge", "pipeline", "history", "2026-08-11.jsonl"),
      `${CRASH_LINE}\n${PHANTOM_LINE}\n${unmarkedCrashLine()}\n`
    );
  }

  function unmarkedCrashLine(): string {
    const unmarkedCrash = JSON.parse(CRASH_LINE) as Record<string, unknown>;
    delete unmarkedCrash.terminal_failure_kind;
    return JSON.stringify(unmarkedCrash);
  }

  function differentlyMarkedCrashLine(): string {
    const otherFailure = JSON.parse(CRASH_LINE) as Record<string, unknown>;
    otherFailure.terminal_failure_kind = "subagent_crash";
    return JSON.stringify(otherFailure);
  }

  function writeLegacyIndexWithoutCrashIdentity(): void {
    const entry = (issueNumber: number, title: string) => ({
      issue_number: issueNumber,
      title,
      outcome: "complete",
      cost_usd: 0,
      total_input_tokens: 0,
      total_output_tokens: 0,
      total_cache_read_tokens: 0,
      total_cache_creation_tokens: 0,
      duration_ms: 0,
      stage_count: 0,
      started_at: "2026-08-11T00:39:37Z",
      recorded_at: "2026-08-11T00:39:37Z",
      branch: "",
    });
    writeFileSync(
      join(root, ".nightgauge", "pipeline", "history", "index.json"),
      JSON.stringify({
        schema_version: "1",
        // Keep the index newer than the JSONL so only the schema-version gate
        // can trigger the required rebuild.
        updated_at: "2099-01-01T00:00:00Z",
        total_runs: 2,
        entries: [entry(397, "Crash mid-stage"), entry(446, "Phantom backup write")],
      })
    );
  }

  it("reader keeps the positively identified crash and filters the true phantom", async () => {
    writeBothPopulations();

    const records = await ExecutionHistoryReader.readAll(root);

    expect(records).toHaveLength(1);
    expect(records[0]).toMatchObject({
      issue_number: 397,
      outcome: "failed",
      terminal_failure_kind: "orchestrator_crash",
    });
  });

  it.each([
    [
      "production reader",
      async () => {
        const records = await ExecutionHistoryReader.readAll(root);
        return records.length;
      },
    ],
    [
      "legacy dashboard reader",
      async () => {
        const state = new DashboardState(createMockMemento(), root);
        return state.backfillFromPipelineArtifacts();
      },
    ],
  ])("%s filters the captured crash shape when its marker is absent", async (_name, read) => {
    writeFileSync(
      join(root, ".nightgauge", "pipeline", "history", "2026-08-11.jsonl"),
      `${unmarkedCrashLine()}\n`
    );

    expect(await read()).toBe(0);
  });

  it.each([
    [
      "production reader",
      async () => {
        const records = await ExecutionHistoryReader.readAll(root);
        return records.length;
      },
    ],
    [
      "legacy dashboard reader",
      async () => {
        const state = new DashboardState(createMockMemento(), root);
        return state.backfillFromPipelineArtifacts();
      },
    ],
  ])("%s does not exempt a different terminal-failure marker", async (_name, read) => {
    writeFileSync(
      join(root, ".nightgauge", "pipeline", "history", "2026-08-11.jsonl"),
      `${differentlyMarkedCrashLine()}\n`
    );

    expect(await read()).toBe(0);
  });

  it.each([
    [
      "production TelemetryStore path",
      (workspaceRoot: string) => {
        writeLegacyIndexWithoutCrashIdentity();
        return new TelemetryStore(workspaceRoot);
      },
    ],
    ["legacy direct-import path", () => undefined],
  ])("dashboard %s imports and distinctly labels only the crash", async (_name, makeStore) => {
    writeBothPopulations();
    const memento = createMockMemento();
    const store = makeStore(root);
    const state = new DashboardState(memento, root, store);

    const imported = await state.backfillFromPipelineArtifacts();

    expect(imported).toBe(1);
    expect(state.getHistory()).toHaveLength(1);
    const crash = state.getHistory()[0];
    expect(crash).toMatchObject({
      issueNumber: 397,
      status: "failed",
      terminalFailureKind: "orchestrator_crash",
    });
    expect(crash.stages.find((stage) => stage.stage === "feature-dev")?.status).toBe("failed");

    const html = getProgressBarHtml(crash);
    expect(html).toContain("Orchestrator crash");
    expect(html).not.toContain(">Failed</span>");

    const historyHtml = getHistoryHtml([crash]);
    expect(historyHtml).toContain("Orchestrator crash");

    const restored = new DashboardState(memento, root, store);
    expect(restored.getHistory()[0]?.terminalFailureKind).toBe("orchestrator_crash");
  });

  it("labels an ordinary failed history run as failed, never as an orchestrator crash", () => {
    const ordinaryFailure = {
      issueNumber: 448,
      title: "Ordinary stage failure",
      branch: "fix/448",
      startedAt: new Date("2026-08-11T00:39:37Z"),
      completedAt: new Date("2026-08-11T00:40:37Z"),
      status: "failed" as const,
      stages: [],
      usage: {
        inputTokens: 0,
        outputTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        costUsd: 0,
        durationMs: 60_000,
        stageCount: 0,
      },
      toolCalls: [],
    };

    const html = getHistoryHtml([ordinaryFailure]);
    expect(html).toContain(">Failed</span>");
    expect(html).not.toContain("Orchestrator crash");
  });

  it("hydrates the crash record by run identity when the same issue has a later retry", async () => {
    const retry = JSON.parse(COMPLETED_LINE) as Record<string, unknown>;
    retry.issue_number = 397;
    retry.title = "Successful retry of crashed issue";
    retry.started_at = "2026-08-11T02:00:00Z";
    retry.completed_at = "2026-08-11T02:10:00Z";
    retry.recorded_at = "2026-08-11T02:10:00Z";
    writeFileSync(
      join(root, ".nightgauge", "pipeline", "history", "2026-08-11.jsonl"),
      `${CRASH_LINE}\n${JSON.stringify(retry)}\n`
    );

    const state = new DashboardState(createMockMemento(), root, new TelemetryStore(root));
    expect(await state.backfillFromPipelineArtifacts()).toBe(2);

    const crash = state
      .getHistory()
      .find((run) => run.terminalFailureKind === "orchestrator_crash");
    expect(crash?.stages.find((stage) => stage.stage === "feature-dev")?.status).toBe("failed");
    expect(crash?.stages.find((stage) => stage.stage === "issue-pickup")?.status).toBe("pending");

    const successfulRetry = state
      .getHistory()
      .find((run) => run.title === "Successful retry of crashed issue");
    expect(successfulRetry?.stages.find((stage) => stage.stage === "feature-dev")?.status).toBe(
      "complete"
    );
  });
});
