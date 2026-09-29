/**
 * The demo daemon's request handlers and event emitter (#2105, ADR-026).
 *
 * `createDaemon({ state, send, log })` returns:
 *   - `handle(request)`: answers one IPC request from state. Unknown methods
 *     get the protocol's empty result (`null`) and are logged, never fatal
 *     (decision 7). `pipeline.abort` and `pipeline.runStage` are refused: the
 *     demo starts and stops nothing (decision 1).
 *   - `emit(event, data)`: sends an event to the extension. The scenario
 *     player (#2106) drives the UI through this. It refuses
 *     `pipeline.runStage`, which would make the extension spawn an agent.
 *   - `ready()`: the `ipc.ready` handshake with `demo: true` (decision 4).
 *
 * Pure: no I/O of its own. The entry point (`../ipc-stub.cjs`) owns stdio.
 */
"use strict";

const { BOARD_STATUSES } = require("./state.cjs");

/** Must equal IPC_PROTOCOL_VERSION; a mismatch disconnects the extension. */
const PROTOCOL_VERSION = 2;

/** Methods the daemon never honours, and events it never emits. */
const REFUSED_METHODS = new Set(["pipeline.abort", "pipeline.runStage"]);
const REFUSED_EVENTS = new Set(["pipeline.runStage", "pipeline.abort"]);

const OK = { status: "ok" };

function statusKey(status) {
  return {
    Ready: "ready",
    "In Progress": "inProgress",
    "In Review": "inReview",
    Backlog: "backlog",
  }[status];
}

function buildHandlers(state) {
  const repoPath = (name) => `/demo/${name}`;
  const openItems = () => state.board.filter((i) => i.status !== "Done");
  const counts = () => {
    const out = { ready: 0, inProgress: 0, inReview: 0, backlog: 0 };
    for (const item of state.board) {
      const key = statusKey(item.status);
      if (key) out[key] += 1;
    }
    return out;
  };
  const totalCost = () => state.history.reduce((sum, h) => sum + Number(h.costUsd || 0), 0);

  return {
    "attention.list": (p) => ({
      requests: state.attention
        .filter((a) => !p || !p.repo || a.repo === p.repo)
        .filter((a) => (p && p.includeTerminal) || !a.resolved)
        .map((a) => ({
          schema_version: 1,
          id: a.id,
          idempotency_key: `demo-${a.id}`,
          kind: a.kind,
          severity: a.severity,
          title: a.title,
          body: a.body || "",
          context: { repo: a.repo, issue: a.issue },
          producer: "demo",
          options: [
            { id: "approve", label: "Approve", verb: "approve", style: "primary" },
            { id: "reject", label: "Reject", verb: "reject", style: "danger" },
          ],
          created_at: state.now,
          expires_at: "2026-12-31T00:00:00.000Z",
          default_action: "reject",
          lifecycle: a.resolved
            ? {
                state: "resolved",
                resolved: { actor: "demo", at: state.now, option_id: a.resolved },
              }
            : { state: "open" },
        })),
    }),
    "audit.getRetentionConfig": () => ({ retentionDays: 90, updatedAt: state.now }),
    "autonomous.status": () => ({
      status: "running",
      startedAt: state.now,
      lastScanAt: state.now,
      running: state.activeRuns.map((r) => ({
        repo: r.repo,
        number: r.issueNumber,
        title: r.title,
        startedAt: r.startedAt,
      })),
      completed: state.history
        .filter((h) => h.outcome === "success")
        .map((h) => ({
          repo: h.repo,
          number: h.number,
          title: h.title,
          completedAt: h.completedAt,
        })),
      failed: state.history
        .filter((h) => h.outcome !== "success")
        .map((h) => ({
          repo: h.repo,
          number: h.number,
          title: h.title,
          failedAt: h.completedAt,
          reason: h.reason,
        })),
      remaining: state.queue.length,
      tokensSpent: 1840000,
      tokensCeiling: 10000000,
      cyclesRun: 12,
      boardRecoveryInFlight: 0,
    }),
    "config.getProjectConfig": () => ({
      owner: state.owner,
      projectNumber: state.projectNumber,
      sanitizationMode: "warn",
      projects: [{ name: "Demo board", number: state.projectNumber, default: true }],
      defaultRepo: state.repositories[0]
        ? `${state.owner}/${state.repositories[0].name}`
        : undefined,
      ownerType: "org",
    }),
    "config.tierAudit": () => ({ entries: [], hasDrift: false }),
    "forge.list": () => ({
      forges: [
        { id: "github", kind: "github", base_url: "https://example.invalid", auth_method: "demo" },
      ],
    }),
    "git.cleanupMergedBranches": () => ({ deleted: [], count: 0 }),
    "git.root": (p) => ({ root: (p && p.workDir) || repoPath("workspace") }),
    "knowledge.metrics": (p) => ({
      window_days: (p && p.windowDays) || 7,
      stale_days: (p && p.staleDays) || 30,
      status: state.knowledge.length ? "enabled" : "empty",
      generated_at: state.now,
      hit_rate: 0.62,
      totals: {
        writes: 14,
        reads: 41,
        recalls: 29,
        recall_hits: 18,
        graduations: 2,
        scaffolds: 5,
        prunes: 0,
        indexes: 3,
        validates: 3,
        stats: 1,
        events_in_range: 116,
      },
      per_stage: [
        { stage: "issue-pickup", reads: 12, writes: 2, recalls: 9, recall_hits: 6 },
        { stage: "feature-implement", reads: 29, writes: 12, recalls: 20, recall_hits: 12 },
      ],
      top_recalled: state.knowledge.map((k) => ({ path: k.path, hits: k.hits || 0 })),
      untouched_entries: [],
      graduation_history: [],
      trust_distribution: { verified: 2 },
      expired_entries: [],
      deprecated_entries: [],
    }),
    "knowledge.search": (p) => {
      const query = String((p && p.query) || "").toLowerCase();
      const hits = state.knowledge
        .filter((k) => !query || `${k.path} ${k.snippet}`.toLowerCase().includes(query))
        .map((k, i) => ({
          rank: i + 1,
          score: 1 - i * 0.1,
          path: k.path,
          kind: k.kind,
          snippet: k.snippet,
          stale: false,
          lifecycle_multiplier: 1,
        }));
      return { hits, total_hits: hits.length };
    },
    "pipeline.getMaxConcurrent": () => ({ maxConcurrent: 2, persisted: true }),
    "pipeline.runningSummary": () => ({
      count: state.activeRuns.length,
      runs: state.activeRuns.map((r) => ({
        runId: r.runId,
        repo: r.repo,
        issueNumber: r.issueNumber,
        title: r.title,
        stage: r.stage,
        startedAt: r.startedAt,
        lastProgressAt: state.now,
        stale: false,
        source: "demo",
      })),
      reloadSafe: false,
      autonomousStatus: "running",
    }),
    "platform.healthCheck": () => ({
      status: "ok",
      version: "demo",
      uptime_seconds: 3600,
      dependencies: {},
    }),
    "platform.setSessionToken": () => OK,
    "platform.status": () => ({ mode: "demo", tier: "team", message: "Demo data" }),
    "platform.getUsageSummary": () => ({
      totalRuns: state.history.length,
      successRatePct: Math.round(
        (100 * state.history.filter((h) => h.outcome === "success").length) /
          Math.max(1, state.history.length)
      ),
      totalCostUsd: Number(totalCost().toFixed(2)),
      totalTokens: state.platform.costByModel.reduce((s, m) => s + m.tokens, 0),
      period: "30d",
    }),
    "platform.getCostAnalytics": () => {
      const byModel = state.platform.costByModel;
      const tokens = byModel.reduce((s, m) => s + m.tokens, 0);
      const cost = byModel.reduce((s, m) => s + Number(m.costUsd), 0);
      return {
        totalInputTokens: Math.round(tokens * 0.8),
        totalOutputTokens: tokens - Math.round(tokens * 0.8),
        totalTokens: tokens,
        totalCostUsd: cost.toFixed(2),
        breakdown: {
          byModel,
          byProject: state.repositories.map((r) => ({
            projectId: `${state.owner}/${r.name}`,
            costUsd: (cost / Math.max(1, state.repositories.length)).toFixed(2),
          })),
          byDay: state.platform.trends.map((t) => ({
            date: t.date,
            costUsd: ((cost * t.totalTokens) / Math.max(1, tokens)).toFixed(2),
          })),
        },
      };
    },
    "platform.getAnalyticsRuns": () => ({
      entries: state.history.map((h) => ({
        issue_number: h.number,
        title: h.title,
        branch: `feat/${h.number}-demo`,
        outcome: h.outcome,
        duration_ms: h.durationMs || 0,
        total_cost_usd: h.costUsd || "0.00",
        started_at: h.completedAt,
      })),
      has_more: false,
    }),
    "platform.getAnalyticsTrends": () => {
      const trends = state.platform.trends;
      return {
        entries: trends,
        granularity: "day",
        dateFrom: trends.length ? trends[0].date : state.now.slice(0, 10),
        dateTo: trends.length ? trends[trends.length - 1].date : state.now.slice(0, 10),
        repos: state.repositories.map((r) => `${state.owner}/${r.name}`),
        targetSuccessRate: 0.8,
      };
    },
    "platform.getAnalyticsHealth": () => ({
      overall_score: 82,
      dimensions: [{ name: "reliability", score: 82, label: "Good", findings: [] }],
      generated_at: state.now,
      period_days: 30,
      total_runs: state.history.length,
    }),
    "pr.list": (p) =>
      state.board
        .filter((i) => i.status === "In Review")
        .filter((i) => !p || !p.repo || i.repo.endsWith(`/${p.repo}`))
        .map((i) => ({
          nodeId: `demo-pr-${i.number}`,
          number: i.number + 1000,
          title: i.title,
          state: "OPEN",
          headRef: `feat/${i.number}-demo`,
          baseRef: "main",
          repo: i.repo,
          url: `${i.url.replace("/issues/", "/pull/")}`,
          reviewStatus: "REVIEW_REQUIRED",
          checkStatus: "SUCCESS",
          isDraft: false,
          createdAt: state.now,
        })),
    "queue.list": () => ({
      schema_version: "1",
      status: state.queue.length ? "active" : "idle",
      items: state.queue.map((q, i) => ({
        repo: q.repo,
        issueNumber: q.issueNumber,
        title: q.title,
        priority: q.priority || i + 1,
        status: "pending",
        addedAt: state.now,
        position: i + 1,
      })),
      updated_at: state.now,
    }),
    "board.list": (p) => state.board.filter((i) => !p || !p.status || i.status === p.status),
    "board.listOpen": () => openItems(),
    "board.counts": () => counts(),
    "board.changed": (p) => ({
      changed: false,
      since: p && p.since,
      repos: state.repositories.map((r) => ({ repo: `${state.owner}/${r.name}`, changed: false })),
      probed: state.repositories.length,
      unprobeable: 0,
    }),
    "workspace.repoList": () => ({
      manifestPath: repoPath("workspace/.nightgauge/workspace.yaml"),
      configured: state.repositories.map((r) => ({
        name: r.name,
        path: repoPath(r.name),
        role: r.role,
        projectNumber: state.projectNumber,
        resolvedProject: state.projectNumber,
        projectTitle: "Demo board",
        exists: true,
        routingRefs: null,
      })),
      candidates: [],
      unmanaged: false,
    }),
    "workspace.setRoot": () => ({ ok: true }),
  };
}

function createDaemon({ state, send, log }) {
  const handlers = buildHandlers(state);
  const warn = log || (() => {});

  function handle(request) {
    if (!request || typeof request.method !== "string" || request.id === undefined) return;
    const { id, method, params } = request;
    if (REFUSED_METHODS.has(method)) {
      warn(`refused ${method}: the demo starts and stops no pipeline`);
      send({ id, error: { code: -32601, message: `${method} is not available in demo mode` } });
      return;
    }
    const handler = handlers[method];
    if (!handler) {
      warn(`unknown method ${method}: answered empty`);
      send({ id, result: null });
      return;
    }
    let result;
    try {
      result = handler(params || undefined);
    } catch (err) {
      send({ id, error: { code: -32603, message: `demo: ${err.message}` } });
      return;
    }
    send({ id, result: result === undefined ? null : result });
  }

  function emit(event, data) {
    if (REFUSED_EVENTS.has(event)) {
      throw new Error(`demo daemon never emits ${event}`);
    }
    send({ event, data: data === undefined ? {} : data });
  }

  function ready() {
    send({ event: "ipc.ready", data: { protocolVersion: PROTOCOL_VERSION, demo: true } });
  }

  return { handle, emit, ready, methods: Object.keys(handlers), state };
}

module.exports = { BOARD_STATUSES, PROTOCOL_VERSION, REFUSED_METHODS, createDaemon };
