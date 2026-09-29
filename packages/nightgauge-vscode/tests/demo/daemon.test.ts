/**
 * Demo daemon (#2105, ADR-026): every inventoried method answers with the
 * shape its typed caller expects, the handshake announces demo mode, and the
 * daemon runs with networking and process spawning stubbed to throw.
 */

import { spawn } from "node:child_process";
import * as fs from "node:fs";
import { createRequire } from "node:module";
import * as os from "node:os";
import * as path from "node:path";
import { describe, expect, it } from "vitest";
import type {
  AnalyticsHealthResult,
  AnalyticsRunsResult,
  AnalyticsTrendsResult,
  AttentionListResult,
  AttentionSweepResult,
  AutonomousStatusResult,
  BoardChangedResult,
  BoardItem,
  ConfigGetProjectResult,
  ConfigTierAuditResult,
  CostAnalyticsResult,
  ForgeListResult,
  GitCleanupMergedBranchesResult,
  HealthResponse,
  IpcQueueState,
  KnowledgeMetricsResult,
  KnowledgeSearchResult,
  PipelineMaxConcurrentResult,
  PlatformStatus,
  PullRequestDetail,
  RateLimitInfo,
  RetentionConfig,
  RunningPipelinesResult,
  StatusCounts,
  StatusOK,
  UsageSummaryResult,
  WorkspaceRepoListResult,
  WorkspaceSetRootResult,
} from "../../src/services/IpcClientBase";
import type { IpcInventory } from "../../demo/ipc-inventory";

const packageRoot = path.resolve(__dirname, "..", "..");
const ENTRY = path.join(packageRoot, "demo", "ipc-stub.cjs");
const SEED = path.join(packageRoot, "demo", "daemon", "seed.json");
const inventory = JSON.parse(
  fs.readFileSync(path.join(packageRoot, "demo", "ipc-inventory.json"), "utf8")
) as IpcInventory;

interface Daemon {
  handle(request: { id: number; method: string; params?: unknown }): void;
  emit(event: string, data?: unknown): void;
  ready(): void;
  methods: string[];
}
const load = createRequire(__filename);
const { createDaemon, PROTOCOL_VERSION } = load("../../demo/daemon/daemon.cjs") as {
  createDaemon(opts: {
    state: unknown;
    send: (m: unknown) => void;
    log?: (m: string) => void;
  }): Daemon;
  PROTOCOL_VERSION: number;
};
const { createState, moveBoardItem } = load("../../demo/daemon/state.cjs") as {
  createState(raw: unknown): { board: BoardItem[] };
  moveBoardItem(state: unknown, n: number, status: string): BoardItem | null;
};

function freshDaemon() {
  const sent: Array<Record<string, unknown>> = [];
  const logged: string[] = [];
  const state = createState(JSON.parse(fs.readFileSync(SEED, "utf8")).state);
  const daemon = createDaemon({
    state,
    send: (m) => sent.push(m as Record<string, unknown>),
    log: (m) => logged.push(m),
  });
  let id = 0;
  const call = (method: string, params?: unknown) => {
    daemon.handle({ id: ++id, method, params });
    return sent[sent.length - 1];
  };
  return { daemon, state, sent, logged, call };
}

// ---- type guards: one per result type the typed client expects ----------

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === "object" && v !== null && !Array.isArray(v);
const str = (v: unknown) => typeof v === "string";
const num = (v: unknown) => typeof v === "number" && Number.isFinite(v);
const bool = (v: unknown) => typeof v === "boolean";
const arrOf = (v: unknown, g: (x: unknown) => boolean) => Array.isArray(v) && v.every(g);
const has = (v: unknown, fields: Record<string, (x: unknown) => boolean>): v is Obj =>
  isObj(v) && Object.entries(fields).every(([k, g]) => g(v[k]));

const isBoardItem = (v: unknown): v is BoardItem =>
  has(v, {
    id: str,
    number: num,
    title: str,
    state: str,
    status: str,
    priority: str,
    size: str,
    labels: (x) => arrOf(x, str),
    assignees: (x) => arrOf(x, str),
    repo: str,
    url: str,
    isEpic: bool,
  });

const guards: Record<string, (v: unknown) => boolean> = {
  "attention.list": (v): v is AttentionListResult =>
    has(v, {
      requests: (x) =>
        arrOf(x, (r) =>
          has(r, {
            schema_version: num,
            id: str,
            idempotency_key: str,
            kind: (k) =>
              ["unblock", "approve", "choose", "provide_input", "handoff", "resume"].includes(
                k as string
              ),
            severity: (k) => ["fyi", "blocking_run", "blocking_fleet"].includes(k as string),
            title: str,
            body: str,
            context: (c) => has(c, { repo: str }),
            producer: str,
            options: (o) => arrOf(o, (e) => has(e, { id: str, label: str, verb: str })),
            created_at: str,
            expires_at: str,
            default_action: str,
            lifecycle: (l) => has(l, { state: str }),
          })
        ),
    }),
  "attention.sweep": (v): v is AttentionSweepResult =>
    has(v, {
      repos: (x) => arrOf(x, (r) => has(r, { repo: str })),
      created: num,
      updated: num,
      autoResolved: num,
    }),
  "audit.getRetentionConfig": (v): v is RetentionConfig => has(v, { retentionDays: num }),
  "autonomous.status": (v): v is AutonomousStatusResult =>
    has(v, {
      status: str,
      startedAt: str,
      lastScanAt: str,
      running: (x) =>
        arrOf(x, (r) => has(r, { repo: str, number: num, title: str, startedAt: str })),
      completed: (x) =>
        arrOf(x, (r) => has(r, { repo: str, number: num, title: str, completedAt: str })),
      failed: (x) => arrOf(x, (r) => has(r, { repo: str, number: num, title: str, failedAt: str })),
      remaining: num,
      tokensSpent: num,
      tokensCeiling: num,
      cyclesRun: num,
      boardRecoveryInFlight: num,
    }),
  "config.getProjectConfig": (v): v is ConfigGetProjectResult =>
    has(v, {
      owner: str,
      projectNumber: num,
      sanitizationMode: (x) => ["warn", "block", "disabled"].includes(x as string),
    }),
  "config.tierAudit": (v): v is ConfigTierAuditResult =>
    has(v, { entries: Array.isArray, hasDrift: bool }),
  "forge.list": (v): v is ForgeListResult =>
    has(v, {
      forges: (x) =>
        arrOf(x, (f) => has(f, { id: str, kind: str, base_url: str, auth_method: str })),
    }),
  "git.cleanupMergedBranches": (v): v is GitCleanupMergedBranchesResult =>
    has(v, { deleted: (x) => arrOf(x, str), count: num }),
  "git.root": (v): v is { root: string } => has(v, { root: str }),
  "github.rateLimit": (v): v is RateLimitInfo =>
    has(v, { remaining: num, limit: num, resetAt: num }),
  "knowledge.metrics": (v): v is KnowledgeMetricsResult =>
    has(v, {
      window_days: num,
      stale_days: num,
      status: (x) => ["enabled", "empty", "disabled"].includes(x as string),
      generated_at: str,
      totals: (t) => has(t, { writes: num, reads: num, recalls: num, events_in_range: num }),
      per_stage: (x) => arrOf(x, (s) => has(s, { stage: str, reads: num, writes: num })),
      top_recalled: (x) => arrOf(x, (s) => has(s, { path: str, hits: num })),
      untouched_entries: Array.isArray,
      graduation_history: Array.isArray,
      trust_distribution: isObj,
      expired_entries: Array.isArray,
      deprecated_entries: Array.isArray,
    }),
  "knowledge.search": (v): v is KnowledgeSearchResult =>
    has(v, {
      hits: (x) =>
        arrOf(x, (h) =>
          has(h, {
            rank: num,
            score: num,
            path: str,
            kind: str,
            snippet: str,
            stale: bool,
            lifecycle_multiplier: num,
          })
        ),
      total_hits: num,
    }),
  "pipeline.getMaxConcurrent": (v): v is PipelineMaxConcurrentResult =>
    has(v, { maxConcurrent: num, persisted: bool }),
  "pipeline.runningSummary": (v): v is RunningPipelinesResult =>
    has(v, {
      count: num,
      reloadSafe: bool,
      runs: (x) =>
        arrOf(x, (r) =>
          has(r, { runId: str, repo: str, issueNumber: num, stale: bool, source: str })
        ),
    }),
  "platform.healthCheck": (v): v is HealthResponse =>
    has(v, { status: str, version: str, uptime_seconds: num, dependencies: isObj }),
  "platform.setSessionToken": (v): v is StatusOK => has(v, { status: str }),
  "platform.status": (v): v is PlatformStatus => has(v, { mode: str }),
  "platform.getUsageSummary": (v): v is UsageSummaryResult =>
    has(v, {
      totalRuns: num,
      successRatePct: num,
      totalCostUsd: num,
      totalTokens: num,
      period: str,
    }),
  "platform.getCostAnalytics": (v): v is CostAnalyticsResult =>
    has(v, {
      totalInputTokens: num,
      totalOutputTokens: num,
      totalTokens: num,
      totalCostUsd: str,
      breakdown: (b) =>
        has(b, {
          byModel: (x) => arrOf(x, (m) => has(m, { modelId: str, costUsd: str, tokens: num })),
          byProject: (x) => arrOf(x, (m) => has(m, { costUsd: str })),
          byDay: (x) => arrOf(x, (m) => has(m, { date: str, costUsd: str })),
        }),
    }),
  "platform.getAnalyticsRuns": (v): v is AnalyticsRunsResult =>
    has(v, {
      has_more: bool,
      entries: (x) =>
        arrOf(x, (e) =>
          has(e, {
            issue_number: num,
            title: str,
            branch: str,
            outcome: str,
            duration_ms: num,
            total_cost_usd: str,
            started_at: str,
          })
        ),
    }),
  "platform.getAnalyticsTrends": (v): v is AnalyticsTrendsResult =>
    has(v, {
      entries: (x) =>
        arrOf(x, (e) => has(e, { date: str, successRate: num, totalRuns: num, totalTokens: num })),
      granularity: str,
      dateFrom: str,
      dateTo: str,
      repos: (x) => arrOf(x, str),
      targetSuccessRate: num,
    }),
  "platform.getAnalyticsHealth": (v): v is AnalyticsHealthResult =>
    has(v, {
      overall_score: num,
      dimensions: (x) => arrOf(x, (d) => has(d, { name: str, score: num, label: str })),
      generated_at: str,
      period_days: num,
      total_runs: num,
    }),
  "pr.list": (v): v is PullRequestDetail[] =>
    arrOf(v, (p) =>
      has(p, {
        nodeId: str,
        number: num,
        title: str,
        state: str,
        headRef: str,
        baseRef: str,
        repo: str,
        url: str,
        isDraft: bool,
      })
    ),
  "queue.list": (v): v is IpcQueueState =>
    has(v, {
      schema_version: str,
      status: str,
      updated_at: str,
      items: (x) =>
        arrOf(x, (i) =>
          has(i, {
            repo: str,
            issueNumber: num,
            title: str,
            priority: num,
            status: str,
            addedAt: str,
            position: num,
          })
        ),
    }),
  "board.list": (v): v is BoardItem[] => arrOf(v, isBoardItem),
  "board.listOpen": (v): v is BoardItem[] => arrOf(v, isBoardItem),
  "board.counts": (v): v is StatusCounts =>
    has(v, { ready: num, inProgress: num, inReview: num, backlog: num }),
  "board.changed": (v): v is BoardChangedResult =>
    has(v, {
      changed: bool,
      repos: (x) => arrOf(x, (r) => has(r, { repo: str, changed: bool })),
      probed: num,
      unprobeable: num,
    }),
  "workspace.repoList": (v): v is WorkspaceRepoListResult =>
    has(v, {
      manifestPath: str,
      configured: (x) =>
        arrOf(x, (r) =>
          has(r, {
            name: str,
            path: str,
            role: str,
            projectNumber: num,
            resolvedProject: num,
            projectTitle: str,
            exists: bool,
          })
        ),
      candidates: Array.isArray,
      unmanaged: bool,
    }),
  "workspace.setRoot": (v): v is WorkspaceSetRootResult => has(v, { ok: bool }),
};

describe("demo daemon results", () => {
  it("has a type guard for every inventoried method", () => {
    const missing = inventory.methods.map((m) => m.method).filter((m) => !(m in guards));
    expect(missing).toEqual([]);
  });

  it("answers every method it serves", () => {
    const { daemon } = freshDaemon();
    expect(daemon.methods.sort()).toEqual(Object.keys(guards).sort());
  });

  for (const method of Object.keys(guards)) {
    it(`${method} matches its typed result`, () => {
      const { call } = freshDaemon();
      const sample = inventory.methods.find((m) => m.method === method)?.sampleParams;
      const response = call(method, sample ?? undefined);
      expect(response.error).toBeUndefined();
      expect(response.result).not.toBeNull();
      expect(guards[method](response.result), JSON.stringify(response.result)).toBe(true);
    });
  }

  it("serves the seeded board by status", () => {
    const { call } = freshDaemon();
    expect(call("board.counts").result).toEqual({
      ready: 3,
      inProgress: 2,
      inReview: 1,
      backlog: 4,
    });
    const ready = call("board.list", { status: "Ready" }).result as BoardItem[];
    expect(ready.map((i) => i.number)).toEqual([131, 133, 135]);
    const running = call("pipeline.runningSummary").result as RunningPipelinesResult;
    expect(running.runs.map((r) => r.issueNumber)).toEqual([112]);
  });

  it("names the default repository by its short name, as the real daemon does", () => {
    // The extension joins it to the owner; a full name shows no board rows.
    const { call } = freshDaemon();
    expect((call("config.getProjectConfig").result as ConfigGetProjectResult).defaultRepo).toBe(
      "harbor-api"
    );
  });

  it("matches board statuses case-insensitively, as the extension spells them", () => {
    const { call } = freshDaemon();
    const inProgress = call("board.list", { status: "In progress" }).result as BoardItem[];
    expect(inProgress.map((i) => i.number)).toEqual([112, 115]);
  });

  it("reflects state mutations in later answers", () => {
    const { state, call } = freshDaemon();
    expect(moveBoardItem(state, 131, "In Progress")?.status).toBe("In Progress");
    expect((call("board.counts").result as StatusCounts).ready).toBe(2);
    expect(() => moveBoardItem(state, 131, "Nowhere")).toThrow(/unknown board status/);
  });

  it("answers unknown methods empty and logs them", () => {
    const { call, logged } = freshDaemon();
    expect(call("nope.method").result).toBeNull();
    expect(logged.join("\n")).toContain("nope.method");
  });

  it("refuses pipeline.abort and pipeline.runStage", () => {
    const { call } = freshDaemon();
    for (const method of ["pipeline.abort", "pipeline.runStage"]) {
      const response = call(method, { executionId: "x" });
      expect(response.result).toBeUndefined();
      expect(response.error).toMatchObject({ message: expect.stringContaining("demo mode") });
    }
  });

  it("emits events for the player but never pipeline.runStage", () => {
    const { daemon, sent } = freshDaemon();
    daemon.emit("queue.changed", { count: 1 });
    expect(sent.at(-1)).toEqual({ event: "queue.changed", data: { count: 1 } });
    expect(() => daemon.emit("pipeline.runStage", {})).toThrow(/never emits/);
    expect(() => daemon.emit("pipeline.abort", {})).toThrow(/never emits/);
  });

  it("announces demo mode with the extension's protocol version", () => {
    const { daemon, sent } = freshDaemon();
    daemon.ready();
    expect(sent.at(-1)).toEqual({
      event: "ipc.ready",
      data: { protocolVersion: PROTOCOL_VERSION, demo: true },
    });
  });

  it("rejects a malformed seed", () => {
    expect(() => createState({ owner: "o", board: { Somewhere: [] } })).toThrow(/unknown board/);
    expect(() => createState({})).toThrow(/owner is required/);
  });

  it("seeds only fictional names", () => {
    const text = fs.readFileSync(SEED, "utf8");
    expect(text).not.toMatch(/nightgauge|edibu|github\.com/i);
  });
});

describe("demo daemon with networking and child_process stubbed to throw", () => {
  it("answers every method and never touches a forbidden module", async () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "demo-daemon-test-"));
    const preload = path.join(dir, "forbid.cjs");
    fs.writeFileSync(
      preload,
      `"use strict";
const Module = require("node:module");
const forbidden = /^(node:)?(net|http|https|http2|tls|dgram|child_process)$/;
const orig = Module._load;
Module._load = function (request, ...rest) {
  if (forbidden.test(request)) throw new Error("forbidden module " + request);
  return orig.call(this, request, ...rest);
};
globalThis.fetch = () => { throw new Error("forbidden fetch"); };
`
    );
    const proc = spawn(
      process.execPath,
      ["--require", preload, ENTRY, "serve", "--workspace", dir],
      {
        env: { PATH: process.env.PATH, NIGHTGAUGE_DEMO_IPC_LOG: path.join(dir, "log.jsonl") },
      }
    );
    let stdout = "";
    let stderr = "";
    proc.stdout.on("data", (c: Buffer) => (stdout += c.toString()));
    proc.stderr.on("data", (c: Buffer) => (stderr += c.toString()));
    const methods = Object.keys(guards);
    methods.forEach((method, i) =>
      proc.stdin.write(JSON.stringify({ id: i + 1, method, params: {} }) + "\n")
    );
    proc.stdin.end();
    const code = await new Promise((resolve) => proc.on("exit", resolve));
    fs.rmSync(dir, { recursive: true, force: true });

    expect(code, stderr).toBe(0);
    expect(stderr).not.toMatch(/forbidden/);
    const lines = stdout
      .trim()
      .split("\n")
      .map((l) => JSON.parse(l) as Obj);
    expect(lines[0]).toEqual({
      event: "ipc.ready",
      data: { protocolVersion: PROTOCOL_VERSION, demo: true },
    });
    const responses = lines.slice(1);
    expect(responses).toHaveLength(methods.length);
    for (const response of responses) {
      const method = methods[(response.id as number) - 1];
      expect(guards[method](response.result), method).toBe(true);
    }
  });
});
