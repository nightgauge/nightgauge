/**
 * Dashboard.healthSnapshotAttribution.test.ts
 *
 * Issue #1231 — one health file held two repositories' populations.
 *
 * `recordHealthSnapshotForRun` once scored a run against the DASHBOARD's
 * history and filed it under the DASHBOARD's path, for a run that may have
 * executed in a sibling repository. A score read off that file describes no
 * repository at all.
 *
 * Since #1898 the store is the one health series, `health/trends.jsonl`,
 * appended by the SDK engine over the run's own repo. These tests write real
 * files under temp roots and assert WHERE the entry landed and WHAT corpus it
 * was computed from, because the defect was never in the scoring maths — it
 * was the root the writer was handed.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { createMockMemento } from "../../mocks/memento";
import type * as vscode from "vscode";

interface MockEventHandler {
  stateChanged: ((state: any) => void)[];
  stageStart: ((data: any) => void)[];
  stageComplete: ((data: any) => void)[];
  stageError: ((data: any) => void)[];
  phaseStart: ((data: any) => void)[];
  phaseComplete: ((data: any) => void)[];
  tokenUsageUpdated: ((data: any) => void)[];
  toolCallRecorded: ((data: any) => void)[];
  backtrackTriggered: ((data: any) => void)[];
  backtrackBlocked: ((data: any) => void)[];
  modelEscalated: ((data: any) => void)[];
  historyRecorded: ((data: any) => void)[];
}

let mockEventHandlers: MockEventHandler;
let mockDisposables: { dispose: () => void }[];

/** repo slug -> absolute root, populated per test before the Dashboard is built. */
const repoRoots = new Map<string, string>();

vi.mock("../../../src/views/dashboard/DashboardHtml", () => ({
  getDashboardHtml: vi.fn().mockReturnValue("<html></html>"),
  getPipelineProgressSectionHtml: vi.fn().mockReturnValue(""),
  getSummaryCardsSectionHtml: vi.fn().mockReturnValue(""),
  getAnalyticsSectionHtml: vi.fn().mockReturnValue(""),
}));

// Mock PipelineStateService
const mockPipelineStateService = {
  onStateChanged: vi.fn((handler: (state: any) => void) => {
    mockEventHandlers.stateChanged.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
  onStageStart: vi.fn((handler: (data: any) => void) => {
    mockEventHandlers.stageStart.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
  onStageComplete: vi.fn((handler: (data: any) => void) => {
    mockEventHandlers.stageComplete.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
  onStageError: vi.fn((handler: (data: any) => void) => {
    mockEventHandlers.stageError.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
  onPhaseStart: vi.fn((handler: (data: any) => void) => {
    mockEventHandlers.phaseStart.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
  onPhaseComplete: vi.fn((handler: (data: any) => void) => {
    mockEventHandlers.phaseComplete.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
  onTokenUsageUpdated: vi.fn((handler: (data: any) => void) => {
    mockEventHandlers.tokenUsageUpdated.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
  onToolCallRecorded: vi.fn((handler: (data: any) => void) => {
    mockEventHandlers.toolCallRecorded.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
  onBacktrackTriggered: vi.fn((handler: (data: any) => void) => {
    mockEventHandlers.backtrackTriggered.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
  onBacktrackBlocked: vi.fn((handler: (data: any) => void) => {
    mockEventHandlers.backtrackBlocked.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
  onModelEscalated: vi.fn((handler: (data: any) => void) => {
    mockEventHandlers.modelEscalated.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
  onHistoryRecorded: vi.fn((handler: (data: any) => void) => {
    mockEventHandlers.historyRecorded.push(handler);
    const disposable = { dispose: vi.fn() };
    mockDisposables.push(disposable);
    return disposable;
  }),
  getState: vi.fn().mockResolvedValue(null),
  getInstance: vi.fn(),
  resetInstance: vi.fn(),
};

// Mock PipelineStateService module
vi.mock("../../../src/services/PipelineStateService", () => ({
  PipelineStateService: {
    getInstance: vi.fn(() => mockPipelineStateService),
    resetInstance: vi.fn(),
  },
}));

// Mock WorkspaceManager
const mockWorkspaceManager = {
  onRepositoryChanged: vi.fn(() => ({ dispose: vi.fn() })),
  onWorkspaceChanged: vi.fn(() => ({ dispose: vi.fn() })),
  getInstance: vi.fn(),
  isMultiWorkspace: vi.fn().mockReturnValue(false),
};

vi.mock("../../../src/services/WorkspaceManager", () => ({
  WorkspaceManager: {
    getInstance: vi.fn(() => ({
      ...mockWorkspaceManager,
      // The real resolver maps "owner/repo" to an absolute root; the test
      // supplies that mapping so attribution can be observed on disk.
      findRepositoryByGitHub: (slug: string) => {
        const p = repoRoots.get(slug);
        return p ? { name: slug.split("/")[1], path: p } : undefined;
      },
    })),
  },
}));

// Mock SanitizationLogService
vi.mock("../../../src/services/SanitizationLogService", () => ({
  SanitizationLogService: vi.fn(function () {
    return {
      onEventsChanged: vi.fn(() => ({ dispose: vi.fn() })),
      initialize: vi.fn().mockResolvedValue(undefined),
      getFilteredEvents: vi.fn().mockReturnValue([]),
      getAggregates: vi.fn().mockReturnValue({}),
      getTimeSeriesData: vi.fn().mockReturnValue([]),
      getEvents: vi.fn().mockReturnValue([]),
      dispose: vi.fn(),
    };
  }),
}));

// Mock ProjectBoardService
vi.mock("../../../src/services/ProjectBoardService", () => ({
  ProjectBoardService: vi.fn(function () {
    return {
      getIssuesByStatus: vi.fn().mockResolvedValue([]),
      getProjects: vi.fn().mockResolvedValue([]),
      getSelectedProject: vi.fn().mockReturnValue(null),
      setSelectedProject: vi.fn(),
    };
  }),
}));

// Mock ProjectIterationService
vi.mock("../../../src/services/ProjectIterationService", () => ({
  ProjectIterationService: {
    getInstance: vi.fn(() => ({
      getIterations: vi.fn().mockResolvedValue([]),
    })),
  },
}));

// Mock vscode module
vi.mock("vscode", () => ({
  EventEmitter: class EventEmitter {
    private listeners: ((data: any) => void)[] = [];

    get event() {
      return (listener: (data: any) => void) => {
        this.listeners.push(listener);
        return { dispose: () => {} };
      };
    }

    fire(data: any) {
      this.listeners.forEach((l) => l(data));
    }

    dispose = vi.fn();
  },
  Uri: {
    joinPath: vi.fn((uri: any, ...pathSegments: string[]) => ({
      fsPath: `/mock/path/${pathSegments.join("/")}`,
    })),
    file: vi.fn((path: string) => ({ fsPath: path })),
  },
  ViewColumn: {
    One: 1,
  },
  window: {
    createWebviewPanel: vi.fn(() => ({
      webview: {
        html: "",
        onDidReceiveMessage: vi.fn(() => ({ dispose: vi.fn() })),
        postMessage: vi.fn(),
      },
      reveal: vi.fn(),
      onDidDispose: vi.fn(() => ({ dispose: vi.fn() })),
      dispose: vi.fn(),
      visible: true,
    })),
    showWarningMessage: vi.fn(),
    showInformationMessage: vi.fn(),
    showSaveDialog: vi.fn(),
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  workspace: {
    getConfiguration: vi.fn(() => ({
      get: vi.fn().mockReturnValue(undefined),
    })),
    fs: {
      writeFile: vi.fn().mockResolvedValue(undefined),
    },
    createFileSystemWatcher: vi.fn(() => ({
      onDidChange: vi.fn(() => ({ dispose: vi.fn() })),
      onDidCreate: vi.fn(() => ({ dispose: vi.fn() })),
      onDidDelete: vi.fn(() => ({ dispose: vi.fn() })),
      dispose: vi.fn(),
    })),
  },
  RelativePattern: vi.fn(),
}));

import { HealthTrendsWriter, type HealthTrendEntry } from "@nightgauge/sdk";
import { Dashboard } from "../../../src/views/dashboard/Dashboard";
import { initGitRepo, mkFakeCloneLayout } from "../../helpers/cloneLayout";

/** `hoursAgo` hours before now, ISO — inside the series' 30-day window. */
function hoursAgo(hours: number, minutes = 0): string {
  return new Date(Date.now() - hours * 3_600_000 + minutes * 60_000).toISOString();
}

/**
 * Seed a repo root with completed runs.
 *
 * Field set mirrors `tests/fixtures/history/go-writer-runs.jsonl` — the Go
 * writer's real output. A record missing `recorded_at`, `run_id` or `branch` is
 * dropped on read, which silently produces an empty corpus and a test that
 * passes for the wrong reason.
 */
function seedRepo(root: string, costs: number[]): void {
  // Each repo is its own clone, with its own per-clone pipeline directory. A
  // real git repository, because the SDK resolves the series' path from git
  // (a fake layout seeded through the extension's helper is not visible to
  // the SDK entry point under vitest's module graph).
  initGitRepo(root);
  const histDir = path.join(mkFakeCloneLayout(root).pipeline, "history");
  fs.mkdirSync(histDir, { recursive: true });
  const lines = costs.map((c, i) => {
    // Oldest first, one run an hour, ending an hour ago.
    const startH = costs.length - i + 1;
    return JSON.stringify({
      schema_version: "2",
      record_type: "run",
      run_id: `01a05300-0000-7000-8000-0000000000${String(i).padStart(2, "0")}`,
      issue_number: 100 + i,
      title: `seed ${i}`,
      repo: "seed/repo",
      branch: `feat/seed-${i}`,
      base_branch: "main",
      outcome: "complete",
      size: "M",
      type: "feature",
      execution_mode: "headless",
      started_at: hoursAgo(startH),
      completed_at: hoursAgo(startH, 30),
      recorded_at: hoursAgo(startH, 31),
      total_duration_ms: 1_800_000,
      files: [],
      stages: {
        "feature-dev": { status: "complete", duration_ms: 1000, attempts: 1 },
      },
      tokens: {
        total_input: 1000,
        total_output: 100,
        total_cache_read: 900,
        total_cache_creation: 10,
        estimated_cost_usd: c,
        per_stage: {
          "feature-dev": {
            input: 1000,
            output: 100,
            cache_read: 900,
            cache_creation: 10,
            cost_usd: c,
          },
        },
      },
    });
  });
  const day = new Date().toISOString().slice(0, 10);
  fs.writeFileSync(path.join(histDir, `${day}.jsonl`), lines.join("\n") + "\n");
}

function readSnapshots(root: string): Promise<HealthTrendEntry[]> {
  return HealthTrendsWriter.read(root);
}

describe("health snapshots are attributed to the repo that ran (#1231)", () => {
  let dashboard: Dashboard;
  let workspaceState: vscode.Memento;
  let tmp: string;
  let runnerRoot: string;
  let siblingRoot: string;
  const mockExtensionUri = { fsPath: "/mock/extension" } as vscode.Uri;

  beforeEach(() => {
    vi.clearAllMocks();
    mockEventHandlers = {
      stateChanged: [],
      stageStart: [],
      stageComplete: [],
      stageError: [],
      phaseStart: [],
      phaseComplete: [],
      tokenUsageUpdated: [],
      toolCallRecorded: [],
      backtrackTriggered: [],
      backtrackBlocked: [],
      modelEscalated: [],
      historyRecorded: [],
    };
    mockDisposables = [];

    tmp = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), "ng-health-attr-")));
    runnerRoot = path.join(tmp, "runner");
    siblingRoot = path.join(tmp, "sibling");
    // Cheap, flat corpus in the runner; an expensive recent spike in the
    // sibling. The two therefore score DIFFERENTLY, which is what makes a
    // misattributed snapshot detectable rather than coincidentally identical.
    seedRepo(runnerRoot, [5, 5, 5, 5, 5, 5, 5, 5, 5, 5]);
    // Oldest first: the sibling's costs SPIKE in its most recent five runs, so
    // its costTrend collapses while the runner's stays flat.
    seedRepo(siblingRoot, [7, 6, 7, 3.5, 3, 9, 10, 10, 64, 70]);

    repoRoots.clear();
    repoRoots.set("acme/sibling", siblingRoot);

    workspaceState = createMockMemento();
    dashboard = new Dashboard(mockExtensionUri, workspaceState, runnerRoot);
  });

  afterEach(() => {
    dashboard.dispose();
    fs.rmSync(tmp, { recursive: true, force: true });
  });

  it("writes a cross-repo run's entry under THAT repo, not the runner's", async () => {
    await dashboard.recordHealthSnapshotForRun(349, 10.85, "acme/sibling", "run-x");

    const sibling = await readSnapshots(siblingRoot);
    const runner = await readSnapshots(runnerRoot);

    expect(
      sibling.length,
      "the run executed in acme/sibling, so its entry belongs in that repo"
    ).toBe(1);
    expect(
      runner.length,
      "writing it under the runner root is the #1231 defect: the file then holds " +
        "two repos' populations and the score describes neither"
    ).toBe(0);
    expect(sibling[0].issue_number).toBe(349);
    expect(sibling[0].run_id).toBe("run-x");
    expect(sibling[0].schema_version).toBe("2");
  });

  it("scores the cross-repo run against THAT repo's history", async () => {
    await dashboard.recordHealthSnapshotForRun(349, 10.85, "acme/sibling", "run-x");
    await dashboard.recordHealthSnapshotForRun(11, 1.5, undefined, "run-local");

    const [sib] = await readSnapshots(siblingRoot);
    const [run] = await readSnapshots(runnerRoot);
    // Both corpora are ten runs, so both are scored — and the sibling's ~10x
    // recent cost spike must make its cost-health differ from the runner's
    // flat corpus. Scoring both against one corpus would make them equal.
    expect(sib.overall_score).not.toBeNull();
    expect(run.overall_score).not.toBeNull();
    expect(sib.dimensions["cost-health"]).toBeDefined();
    expect(sib.dimensions["cost-health"]).not.toBe(run.dimensions["cost-health"]);
  });

  it("records exactly one entry per run, across repeated completions", async () => {
    // One completed run produced six byte-identical records in the real file.
    for (let i = 0; i < 6; i++) {
      await dashboard.recordHealthSnapshotForRun(349, 10.85, "acme/sibling", "run-x");
    }
    expect((await readSnapshots(siblingRoot)).length).toBe(1);
  });

  it("still records a genuine re-run of the same issue", async () => {
    // The dedupe key carries the run identity, so a second attempt at one issue
    // is a new key. Keying on the issue alone would silently drop it.
    await dashboard.recordHealthSnapshotForRun(349, 10.85, "acme/sibling", "run-1");
    await dashboard.recordHealthSnapshotForRun(349, 12.0, "acme/sibling", "run-2");

    expect((await readSnapshots(siblingRoot)).length).toBe(2);
  });

  it("records a repo with no history of its own as null — under that repo only", async () => {
    const emptyRoot = path.join(tmp, "empty");
    initGitRepo(emptyRoot);
    mkFakeCloneLayout(emptyRoot);
    repoRoots.set("acme/empty", emptyRoot);

    await dashboard.recordHealthSnapshotForRun(1, 1, "acme/empty", "run-e");

    // #1898: a run with no data is recorded as null, not omitted and not
    // coerced to a number — and still never filed under the runner's root.
    const [entry, ...rest] = await readSnapshots(emptyRoot);
    expect(rest).toHaveLength(0);
    expect(entry.overall_score).toBeNull();
    expect(entry.status).toBe("no-data");
    expect((await readSnapshots(runnerRoot)).length).toBe(0);
  });
});
