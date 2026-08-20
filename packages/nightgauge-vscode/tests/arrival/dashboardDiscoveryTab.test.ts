/**
 * dashboardDiscoveryTab.test.ts — arrival: Discovery.
 *
 * arrival:discovery
 *
 * The Discovery tab is the one surface in epic #741 whose emptiness was never
 * a reader bug. `DiscoveryActivityService` parsed the right files at the right
 * paths; the two GitHub Actions workflows that were supposed to WRITE those
 * files had simply never been built (#753), and `docs/SCHEDULED_DISCOVERY.md`
 * described them in the present tense anyway. Every existing test passed while
 * the tab could not possibly show anything.
 *
 * So this file does not stub a producer — it runs the real one. The records
 * come from `scripts/discovery-run-record.py`, the same script both workflows
 * invoke, and the transport test below pushes them to a `discovery-state`
 * branch with `scripts/discovery-state-publish.sh` and pulls them back with
 * `scripts/discovery-state-sync.sh`, exactly as a runner and a developer's
 * checkout do. A field renamed on either side of that pipe fails here.
 *
 * What is NOT covered, deliberately: the model-driven half. Relevance scoring
 * and issue authoring happen inside the release-watch skill, and the arrays it
 * fills in (`issues_created`, `issues_backlogged`) are written directly below
 * to stand in for that step. Everything mechanical around it is real.
 *
 * @see docs/SCHEDULED_DISCOVERY.md
 * @see tests/arrival/dashboardHarness.ts
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { execFileSync } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { createMockMemento } from "../mocks/memento";

vi.mock("vscode", async () => (await import("./dashboardHarness")).vscodeMockModule());
vi.mock("../../src/services/IpcClient", async () =>
  (await import("./dashboardHarness")).ipcClientMockModule()
);
vi.mock("../../src/platform/TokenStorage", async () =>
  (await import("./dashboardHarness")).tokenStorageMockModule()
);
vi.mock("../../src/services/PipelineStateService", async () =>
  (await import("./dashboardHarness")).pipelineStateServiceMockModule()
);
vi.mock("../../src/services/WorkspaceManager", async () =>
  (await import("./dashboardHarness")).workspaceManagerMockModule()
);
vi.mock(
  "../../src/services/SanitizationLogService",
  async () => await (await import("./dashboardHarness")).sanitizationLogServiceMockModule()
);
vi.mock("../../src/services/ProjectBoardService", async () =>
  (await import("./dashboardHarness")).projectBoardServiceMockModule()
);
vi.mock("../../src/services/ProjectIterationService", async () =>
  (await import("./dashboardHarness")).projectIterationServiceMockModule()
);
vi.mock(
  "../../src/services/ConfigBridge",
  async () => await (await import("./dashboardHarness")).configBridgeMockModule()
);

import { Dashboard } from "../../src/views/dashboard/Dashboard";
import { resetHarness, renderDashboardHtml, renderedText, tabPanelHtml } from "./dashboardHarness";

const repoRoot = path.resolve(__dirname, "../../../..");
const runRecord = path.join(repoRoot, "scripts/discovery-run-record.py");
const statePublish = path.join(repoRoot, "scripts/discovery-state-publish.sh");
const stateSync = path.join(repoRoot, "scripts/discovery-state-sync.sh");

const NEW_VERSION = "2.1.80";
const SINCE_VERSION = "2.1.74";
const CREATED_TITLE = "Adopt the new streaming tool-result shape";
const BACKLOG_TITLE = "Status line supports a custom template";

let dashboard: Dashboard | undefined;
const tempRoots: string[] = [];

function makeWorkspace(prefix: string): string {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), prefix));
  tempRoots.push(root);
  fs.mkdirSync(path.join(root, ".nightgauge"), { recursive: true });
  fs.writeFileSync(
    path.join(root, ".nightgauge", "config.yaml"),
    "github:\n  owner: nightgauge\n  repo: nightgauge\n",
    "utf-8"
  );
  return root;
}

/** Run the producer the workflows run, in the workspace the tab will read. */
function producer(args: string[], cwd: string): void {
  execFileSync("python3", [runRecord, ...args], { cwd, stdio: "pipe" });
}

/**
 * Bracket one release-watch run exactly as release-watchdog.yml does: open a
 * `running` record, let the skill's contribution land in it, then close it.
 */
function recordReleaseWatchRun(root: string): void {
  producer(
    [
      "open",
      "--kind",
      "release-watch",
      "--workspace",
      root,
      "--provider",
      "claude-code",
      "--source",
      "anthropics/claude-code",
      "--triggered-by",
      "schedule",
      "--since-version",
      SINCE_VERSION,
      "--new-version",
      NEW_VERSION,
    ],
    root
  );

  // Stand-in for the skill's in-place enrichment between open and close. The
  // `close` verb must preserve it — a producer that rewrote the record from
  // scratch would erase every issue the run had just filed, and the tab would
  // report a run that created nothing.
  const logPath = path.join(root, ".nightgauge", "release-watch", "creation-log-claude-code.json");
  const log = JSON.parse(fs.readFileSync(logPath, "utf-8"));
  log.issues_created = [
    {
      number: 771,
      title: CREATED_TITLE,
      url: "https://github.com/nightgauge/nightgauge/issues/771",
      score: 88,
    },
  ];
  log.issues_backlogged = [{ title: BACKLOG_TITLE, score: 41, reason: "below score_threshold" }];
  log.issues_deduped = ["Adopt the new streaming tool-result shape"];
  fs.writeFileSync(logPath, JSON.stringify(log, null, 2), "utf-8");

  producer(
    [
      "close",
      "--kind",
      "release-watch",
      "--workspace",
      root,
      "--provider",
      "claude-code",
      "--status",
      "completed",
    ],
    root
  );

  // backlog.json is the skill's own output, not the run record's — the service
  // reads it separately to populate the pending-backlog table.
  fs.writeFileSync(
    path.join(root, ".nightgauge", "release-watch", "backlog.json"),
    JSON.stringify([{ title: BACKLOG_TITLE, score: 41, reason: "below score_threshold" }], null, 2),
    "utf-8"
  );
}

function recordImprovementRun(root: string): void {
  producer(
    [
      "open",
      "--kind",
      "continuous-improvement",
      "--workspace",
      root,
      "--triggered-by",
      "schedule",
      "--mode",
      "dogfood",
      "--create-issues",
    ],
    root
  );
  producer(
    ["close", "--kind", "continuous-improvement", "--workspace", root, "--status", "completed"],
    root
  );
}

async function newDashboard(root: string): Promise<Dashboard> {
  const d = new Dashboard({ fsPath: "/mock/extension" } as never, createMockMemento(), root);
  d.show();
  await d.refreshDiscoveryActivityData();
  return d;
}

function discoveryText(d: Dashboard): string {
  return renderedText(tabPanelHtml(renderDashboardHtml(d), "discovery"));
}

beforeEach(() => {
  vi.clearAllMocks();
  resetHarness();
});

afterEach(async () => {
  await new Promise((resolve) => setImmediate(resolve));
  dashboard?.dispose();
  dashboard = undefined;
  while (tempRoots.length > 0) {
    fs.rmSync(tempRoots.pop() as string, { recursive: true, force: true });
  }
});

// ---------------------------------------------------------------------------
// The records the scheduled workflows write
// ---------------------------------------------------------------------------

describe("arrival: Discovery tab (.nightgauge/release-watch + improvement-runs)", () => {
  it("reaches a populated state from records the real producer wrote", async () => {
    const root = makeWorkspace("ng-arrival-discovery-");
    recordReleaseWatchRun(root);
    recordImprovementRun(root);

    dashboard = await newDashboard(root);
    const text = discoveryText(dashboard);

    // Release-watch half: the version came off the producer's record, and the
    // issue title came through the service's per-provider aggregation.
    expect(text).toContain(NEW_VERSION);
    expect(text).toContain(SINCE_VERSION);
    expect(text).toContain(CREATED_TITLE);
    expect(text).toContain("#771");
    expect(text).toContain("1 created");

    // Continuous-improvement half — a separate file, separate section.
    expect(text).toContain("Continuous Improvement");
    expect(text).toContain("dogfood");

    // Backlog table.
    expect(text).toContain(BACKLOG_TITLE);

    // The summary cards are computed, not copied: an issue created today is
    // inside the 7-day window, so a zero here means the aggregation never saw
    // the record even though the section above rendered.
    expect(text).toContain("Issues Created (7d)");
    expect(text).not.toContain("No discovery activity yet");
  });

  it("an empty workspace renders the empty state, not a stale one", async () => {
    const root = makeWorkspace("ng-arrival-discovery-empty-");

    dashboard = await newDashboard(root);
    const text = discoveryText(dashboard);

    expect(text).toContain("No discovery activity yet");
    expect(text).not.toContain(CREATED_TITLE);
    expect(text).not.toContain(NEW_VERSION);
  });

  it("a run that died mid-flight is shown as running, not as never having run", async () => {
    const root = makeWorkspace("ng-arrival-discovery-running-");
    // `open` with no `close` — the workflow was cancelled after filing issues.
    producer(
      [
        "open",
        "--kind",
        "release-watch",
        "--workspace",
        root,
        "--triggered-by",
        "workflow_dispatch",
        "--since-version",
        SINCE_VERSION,
        "--new-version",
        NEW_VERSION,
      ],
      root
    );

    dashboard = await newDashboard(root);
    const text = discoveryText(dashboard);

    expect(text).not.toContain("No discovery activity yet");
    expect(text).toContain(NEW_VERSION);
    expect(text).toContain("workflow_dispatch");
  });
});

// ---------------------------------------------------------------------------
// The transport: runner → discovery-state branch → local checkout
// ---------------------------------------------------------------------------

describe("arrival: discovery state transport (discovery-state branch)", () => {
  function git(cwd: string, ...args: string[]): string {
    return execFileSync("git", args, {
      cwd,
      stdio: "pipe",
      encoding: "utf-8",
      env: {
        ...process.env,
        GIT_AUTHOR_NAME: "arrival",
        GIT_AUTHOR_EMAIL: "arrival@example.invalid",
        GIT_COMMITTER_NAME: "arrival",
        GIT_COMMITTER_EMAIL: "arrival@example.invalid",
      },
    });
  }

  it("a run published on a runner reaches a checkout, and the tab renders it", async () => {
    // Three repos standing in for the three real machines: a remote, the
    // runner's clone (which produces and publishes), and a developer's clone
    // (which syncs and reads). Anything less than this cannot catch the
    // failure the issue describes — a workflow that runs, writes state on a
    // disposable disk, and leaves every local Discovery tab empty.
    const remote = makeWorkspace("ng-arrival-discovery-remote-");
    fs.rmSync(remote, { recursive: true, force: true });
    fs.mkdirSync(remote, { recursive: true });
    git(remote, "init", "--quiet", "--bare");

    const runner = makeWorkspace("ng-arrival-discovery-runner-");
    git(runner, "init", "--quiet", "--initial-branch", "trunk");
    fs.writeFileSync(path.join(runner, "README.md"), "runner\n", "utf-8");
    git(runner, "add", "-A");
    git(runner, "commit", "--quiet", "-m", "seed");
    git(runner, "remote", "add", "origin", remote);
    git(runner, "push", "--quiet", "-u", "origin", "trunk");

    recordReleaseWatchRun(runner);
    execFileSync("bash", [statePublish, "--message", "arrival: release-watch"], {
      cwd: runner,
      stdio: "pipe",
    });

    const local = makeWorkspace("ng-arrival-discovery-local-");
    fs.rmSync(local, { recursive: true, force: true });
    git(path.dirname(local), "clone", "--quiet", remote, local);
    git(local, "checkout", "--quiet", "trunk");
    fs.mkdirSync(path.join(local, ".nightgauge"), { recursive: true });
    fs.writeFileSync(
      path.join(local, ".nightgauge", "config.yaml"),
      "github:\n  owner: nightgauge\n  repo: nightgauge\n",
      "utf-8"
    );

    // Nothing has fetched the state yet: this is the state the tab was stuck
    // in for the whole life of the feature.
    expect(fs.existsSync(path.join(local, ".nightgauge", "release-watch"))).toBe(false);

    execFileSync("bash", [stateSync], { cwd: local, stdio: "pipe" });

    const synced = path.join(
      local,
      ".nightgauge",
      "release-watch",
      "creation-log-claude-code.json"
    );
    expect(fs.existsSync(synced)).toBe(true);
    const record = JSON.parse(fs.readFileSync(synced, "utf-8"));
    expect(record.new_version).toBe(NEW_VERSION);
    expect(record.issues_created[0].title).toBe(CREATED_TITLE);

    // The claim is not "a file arrived" — it is that the tab shows the run.
    dashboard = await newDashboard(local);
    const text = discoveryText(dashboard);
    expect(text).toContain(NEW_VERSION);
    expect(text).toContain(CREATED_TITLE);
    expect(text).not.toContain("No discovery activity yet");
  });
});
