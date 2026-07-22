import { describe, it, expect, beforeEach, vi } from "vitest";
import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import {
  TraceRecorder,
  TRACE_SCHEMA_VERSION,
  TRACE_PRODUCER_SDK,
} from "../events/traceRecorder.js";
import type { TraceEvent } from "../events/traceRecorder.js";

const RUN_ID = "01890a5d-ac96-774b-bcce-b302099a8057";

async function tmpPipelineDir(): Promise<string> {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "trace-recorder-"));
  return path.join(dir, ".nightgauge", "pipeline");
}

async function readEvents(pipelineDir: string, runId: string): Promise<TraceEvent[]> {
  const file = path.join(pipelineDir, "trace", `${runId}.jsonl`);
  const content = await fs.readFile(file, "utf-8");
  return content
    .split("\n")
    .filter((l) => l.length > 0)
    .map((l) => JSON.parse(l) as TraceEvent);
}

describe("TraceRecorder", () => {
  let pipelineDir: string;

  beforeEach(async () => {
    pipelineDir = await tmpPipelineDir();
  });

  it("writes the ADR 013 envelope with monotonic seq and sdk producer", async () => {
    const recorder = TraceRecorder.open({
      pipelineDir,
      runId: RUN_ID,
      repo: "nightgauge/nightgauge",
      issue: 180,
    });
    recorder.emit("stage_start", { stage: "feature-dev", payload: { model: "sonnet" } });
    recorder.emit("stage_exit", { stage: "feature-dev", payload: { success: true } });
    await recorder.flush();

    const events = await readEvents(pipelineDir, RUN_ID);
    expect(events).toHaveLength(2);
    for (const [i, ev] of events.entries()) {
      expect(ev.schema_version).toBe(TRACE_SCHEMA_VERSION);
      expect(ev.run_id).toBe(RUN_ID);
      expect(ev.producer).toBe(TRACE_PRODUCER_SDK);
      expect(ev.repo).toBe("nightgauge/nightgauge");
      expect(ev.issue).toBe(180);
      expect(ev.seq).toBe(i + 1);
      expect(() => new Date(ev.ts)).not.toThrow();
    }
    expect(events[0].kind).toBe("stage_start");
    expect(events[1].payload).toEqual({ success: true });
  });

  it("resolves run_id from run-state.json when not given explicitly", async () => {
    await fs.mkdir(pipelineDir, { recursive: true });
    await fs.writeFile(
      path.join(pipelineDir, "run-state.json"),
      JSON.stringify({
        schema_version: "1.0",
        issue_number: 180,
        state: "running",
        run_id: RUN_ID,
        attempt_number: 1,
        completed_stages: [],
        branch: "feat/180",
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
        attempts: [],
      })
    );

    const recorder = TraceRecorder.open({ pipelineDir, issue: 180 });
    recorder.emit("stage_start", { stage: "feature-dev" });
    await recorder.flush();

    expect(recorder.getRunId()).toBe(RUN_ID);
    const events = await readEvents(pipelineDir, RUN_ID);
    expect(events).toHaveLength(1);
  });

  it("resolves run_id from runtime-{issue}.json on the interactive path (#228)", async () => {
    // The interactive HeadlessOrchestrator path never writes run_id into
    // run-state.json; the Go notify handler instead persists the platform-facing
    // RunID to runtime-{issue}.json. The recorder must resolve it from there so
    // the trace shares the platform's run_id.
    await fs.mkdir(pipelineDir, { recursive: true });
    await fs.writeFile(
      path.join(pipelineDir, "runtime-244.json"),
      JSON.stringify({ repo: "nightgauge/acmeapp", issueNumber: 244, runId: RUN_ID })
    );

    const recorder = TraceRecorder.open({ pipelineDir, issue: 244 });
    recorder.emit("stage_start", { stage: "feature-dev" });
    await recorder.flush();

    expect(recorder.isEnabled()).toBe(true);
    expect(recorder.getRunId()).toBe(RUN_ID);
    const events = await readEvents(pipelineDir, RUN_ID);
    expect(events).toHaveLength(1);
    expect(events[0].issue).toBe(244);
  });

  it("prefers run-state.json run_id over runtime-{issue}.json when both exist", async () => {
    const runtimeOnlyId = "runtime11-2222-3333-4444-555566667777";
    await fs.mkdir(pipelineDir, { recursive: true });
    await fs.writeFile(
      path.join(pipelineDir, "run-state.json"),
      JSON.stringify({
        schema_version: "1.0",
        issue_number: 244,
        state: "running",
        run_id: RUN_ID,
        attempt_number: 1,
        completed_stages: [],
        branch: "feat/244",
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
        attempts: [],
      })
    );
    await fs.writeFile(
      path.join(pipelineDir, "runtime-244.json"),
      JSON.stringify({ issueNumber: 244, runId: runtimeOnlyId })
    );

    const recorder = TraceRecorder.open({ pipelineDir, issue: 244 });
    recorder.emit("stage_start", { stage: "feature-dev" });
    await recorder.flush();

    expect(recorder.getRunId()).toBe(RUN_ID);
  });

  it("is a no-op that logs one debug line when no run-state exists and no run id was given", async () => {
    const debugSpy = vi.spyOn(console, "debug").mockImplementation(() => {});
    try {
      const recorder = TraceRecorder.open({ pipelineDir, issue: 180 });
      recorder.emit("stage_start", { stage: "feature-dev" });
      recorder.emit("stage_exit", { stage: "feature-dev" });
      await recorder.flush();

      expect(recorder.isEnabled()).toBe(false);
      await expect(fs.readdir(path.join(pipelineDir, "trace"))).rejects.toThrow();
      // #228: the disable path is observable (one-time debug line), not silent.
      expect(debugSpy).toHaveBeenCalledTimes(1);
      expect(debugSpy.mock.calls[0][0]).toContain("[traceRecorder] disabled");
    } finally {
      debugSpy.mockRestore();
    }
  });

  it("rejects unsafe run ids instead of writing outside the trace dir", async () => {
    const recorder = TraceRecorder.open({ pipelineDir, runId: "../../etc/passwd" });
    recorder.emit("stage_start", {});
    await recorder.flush();
    expect(recorder.isEnabled()).toBe(false);
  });

  it("resumes seq past existing lines so producers/restarts stay monotonic", async () => {
    // Simulate the Go writer having already appended two events.
    const traceDir = path.join(pipelineDir, "trace");
    await fs.mkdir(traceDir, { recursive: true });
    await fs.writeFile(
      path.join(traceDir, `${RUN_ID}.jsonl`),
      `${JSON.stringify({ schema_version: 1, run_id: RUN_ID, seq: 1, ts: "2026-07-17T10:00:00Z", kind: "stage_start", producer: "go" })}\n` +
        `${JSON.stringify({ schema_version: 1, run_id: RUN_ID, seq: 2, ts: "2026-07-17T10:00:01Z", kind: "stage_exit", producer: "go" })}\n`
    );

    const recorder = TraceRecorder.open({ pipelineDir, runId: RUN_ID });
    recorder.emit("phase_transition", { stage: "feature-dev", phase: "implementation" });
    await recorder.flush();

    const events = await readEvents(pipelineDir, RUN_ID);
    expect(events).toHaveLength(3);
    const sdkEvent = events[2];
    expect(sdkEvent.producer).toBe(TRACE_PRODUCER_SDK);
    expect(sdkEvent.seq).toBeGreaterThan(2);
  });

  it("records phase transitions with per-phase durations", async () => {
    const recorder = TraceRecorder.open({ pipelineDir, runId: RUN_ID, issue: 180 });
    recorder.phaseTransition("feature-dev", { name: "context-loading", index: 1, total: 18 });
    recorder.phaseTransition("feature-dev", { name: "implementation", index: 5, total: 18 });
    // Re-announcement of the current phase is not a transition.
    recorder.phaseTransition("feature-dev", { name: "implementation", index: 5, total: 18 });
    await recorder.flush();

    const events = await readEvents(pipelineDir, RUN_ID);
    expect(events).toHaveLength(2);
    expect(events[0].phase).toBe("context-loading");
    expect(events[0].payload).toMatchObject({ index: 1, total: 18 });
    expect(events[0].payload).not.toHaveProperty("prev_phase");
    expect(events[1].phase).toBe("implementation");
    expect(events[1].payload).toMatchObject({ prev_phase: "context-loading" });
    expect(events[1].payload?.["prev_phase_duration_ms"]).toBeGreaterThanOrEqual(0);
  });

  it("records backtracks with rationale and evidence intact", async () => {
    const recorder = TraceRecorder.open({ pipelineDir, runId: RUN_ID, issue: 180 });
    recorder.backtrack({
      fromStage: "feature-validate",
      targetStage: "feature-planning",
      signalType: "PLAN_REVISION_NEEDED",
      rationale: "acceptance criteria 3 is unimplementable as planned",
      evidence: ["validate log line 42", "AC-3 mismatch"],
      trigger: "feedback",
    });
    await recorder.flush();

    const events = await readEvents(pipelineDir, RUN_ID);
    expect(events[0].kind).toBe("backtrack");
    expect(events[0].payload).toEqual({
      from_stage: "feature-validate",
      target_stage: "feature-planning",
      signal_type: "PLAN_REVISION_NEEDED",
      rationale: "acceptance criteria 3 is unimplementable as planned",
      evidence: ["validate log line 42", "AC-3 mismatch"],
      trigger: "feedback",
    });
  });

  it("is fail-open: an unwritable trace path never throws", async () => {
    // pipelineDir points at a FILE, so mkdir of trace/ fails.
    const parent = await fs.mkdtemp(path.join(os.tmpdir(), "trace-recorder-bad-"));
    const filePath = path.join(parent, "not-a-dir");
    await fs.writeFile(filePath, "x");

    const recorder = TraceRecorder.open({ pipelineDir: filePath, runId: RUN_ID });
    recorder.emit("stage_start", { stage: "feature-dev" });
    await expect(recorder.flush()).resolves.toBeUndefined();
    expect(recorder.isEnabled()).toBe(false);
  });
});
