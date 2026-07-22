/**
 * ExtensionStalenessService — detects when the running VSCode extension build
 * is behind the workspace's main branch on critical pipeline paths.
 *
 * Why this service exists: the user has been hitting recurring autonomous-mode
 * failures where a fix is shipped on main but not yet running because the
 * extension wasn't rebuilt and reloaded. Each missed deploy can cost $14-$30
 * per failure (#3204, #3220, #3230). This service closes that gap by:
 *
 *   1. Reading `dist/build-info.json` (stamped at build time by
 *      `scripts/dev-install.sh` and the release workflow) to know what
 *      commit the running extension was built from.
 *   2. Running `git rev-parse HEAD` against the workspace to know what
 *      commit is currently checked out.
 *   3. If they differ, running `git diff --name-only <build>..<head>` to
 *      see which files changed.
 *   4. Classifying changed files against a hardcoded CRITICAL_PATHS list —
 *      paths where stale code is known to cause cost-burn or wrong dispatch
 *      (skillRunner, monitoringResolver, AutoRetroService, plugin hooks, Go
 *      orchestrator).
 *
 * Consumers:
 *   - Status bar (`ExtensionStalenessStatusItem`) shows a warning when stale —
 *     critical staleness shows $(warning) with file count, non-critical shows a
 *     muted $(warning). No dispatch is ever blocked (#3532).
 *   - The `nightgauge.refreshExtensionFromMain` command kicks off the
 *     dev-install + reload-window sequence.
 *
 * @see Issue #3300 — Extension staleness detection.
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import * as vscode from "vscode";
import type { Logger } from "../utils/logger";

const execFileAsync = promisify(execFile);

/** Periodic recheck cadence — picks up commits made during long sessions. */
const RECHECK_INTERVAL_MS = 5 * 60_000;

/**
 * Critical paths — files where stale code is known to cause cost-burn or
 * incorrect dispatch. When the workspace HEAD has commits that touch ANY of
 * these paths and the running build doesn't, dispatch is refused.
 *
 * Each entry is a path prefix (no globbing) — `git diff --name-only` results
 * are tested with `startsWith`. Wide prefixes are intentional (e.g. all of
 * `internal/orchestrator/` is critical) so any new file in those areas is
 * automatically protected.
 */
export const CRITICAL_PATHS: readonly string[] = [
  // TypeScript pipeline execution
  "packages/nightgauge-vscode/src/utils/skillRunner.ts",
  "packages/nightgauge-vscode/src/utils/resolvers/monitoringResolver.ts",
  "packages/nightgauge-vscode/src/utils/resolvers/modelResolver.ts",
  "packages/nightgauge-vscode/src/utils/resolvers/stageResolver.ts",
  "packages/nightgauge-vscode/src/services/AutoRetroService.ts",
  "packages/nightgauge-vscode/src/services/ConcurrentPipelineManager.ts",
  "packages/nightgauge-vscode/src/services/PipelineBridge.ts",
  "packages/nightgauge-vscode/src/services/SkillRunner.ts",
  // Plugin hooks (resolve via $CLAUDE_PLUGIN_ROOT — read fresh from repo every spawn)
  "claude-plugins/nightgauge/hooks/",
  // Go orchestrator
  "internal/orchestrator/",
  "internal/ipc/",
  // Schema definitions
  "packages/nightgauge-vscode/src/schemas/",
];

export interface BuildInfo {
  commitSha: string;
  branch: string;
  commitTimestamp: string;
  buildTimestamp: string;
  schemaVersion: string;
}

export type StalenessState =
  | { kind: "fresh"; buildSha: string; currentSha: string }
  | { kind: "unknown"; reason: string }
  | {
      kind: "stale";
      buildSha: string;
      currentSha: string;
      commitsBehind: number;
      criticalPathsChanged: readonly string[];
      otherPathsChanged: readonly string[];
    };

export class ExtensionStalenessService implements vscode.Disposable {
  private state: StalenessState = { kind: "unknown", reason: "not yet checked" };
  private timer: ReturnType<typeof setInterval> | null = null;
  private readonly _onChanged = new vscode.EventEmitter<StalenessState>();
  readonly onChanged = this._onChanged.event;

  constructor(
    private readonly extensionDistDir: string,
    private readonly workspaceRoot: string,
    private readonly logger: Logger
  ) {}

  /** Most-recent staleness assessment. May be stale until first refresh completes. */
  getState(): StalenessState {
    return this.state;
  }

  /**
   * True when stale AND any changed file matches CRITICAL_PATHS. This is the
   * gate ConcurrentPipelineManager consults before dispatching new slots.
   */
  isCriticallyStale(): boolean {
    return this.state.kind === "stale" && this.state.criticalPathsChanged.length > 0;
  }

  /** Begin periodic checks. Runs an initial check immediately. */
  async start(): Promise<void> {
    await this.refresh();
    this.timer = setInterval(() => {
      void this.refresh();
    }, RECHECK_INTERVAL_MS);
  }

  /** Run a single staleness check and update state. */
  async refresh(): Promise<StalenessState> {
    const next = await this.compute();
    if (!isSameState(this.state, next)) {
      this.state = next;
      this._onChanged.fire(next);
      this.logger.info("ExtensionStaleness state changed", { state: summarize(next) });
    } else {
      this.state = next;
    }
    return next;
  }

  private async compute(): Promise<StalenessState> {
    const buildInfo = await this.readBuildInfo();
    if (!buildInfo) {
      return {
        kind: "unknown",
        reason: "dist/build-info.json missing — extension built without provenance stamp",
      };
    }
    if (buildInfo.commitSha === "unknown" || !buildInfo.commitSha) {
      return { kind: "unknown", reason: "build-info.json has no commitSha" };
    }

    const currentSha = await this.gitRevParse("HEAD");
    if (!currentSha) {
      return { kind: "unknown", reason: "git rev-parse HEAD failed" };
    }
    if (currentSha === buildInfo.commitSha) {
      return { kind: "fresh", buildSha: buildInfo.commitSha, currentSha };
    }

    // If the build SHA doesn't exist in this workspace's git history the
    // extension is installed in a different repo — silently treat as unknown
    // rather than firing a bogus "0 commits behind" warning.
    const buildShaExists = await this.shaExistsInRepo(buildInfo.commitSha);
    if (!buildShaExists) {
      return {
        kind: "unknown",
        reason: "build SHA not found in this workspace — extension is running in a different repo",
      };
    }

    const commitsBehind = await this.countCommitsBehind(buildInfo.commitSha, currentSha);
    const changedFiles = await this.diffNames(buildInfo.commitSha, currentSha);
    const criticalPathsChanged: string[] = [];
    const otherPathsChanged: string[] = [];
    for (const f of changedFiles) {
      if (CRITICAL_PATHS.some((prefix) => f.startsWith(prefix))) {
        criticalPathsChanged.push(f);
      } else {
        otherPathsChanged.push(f);
      }
    }
    return {
      kind: "stale",
      buildSha: buildInfo.commitSha,
      currentSha,
      commitsBehind,
      criticalPathsChanged,
      otherPathsChanged,
    };
  }

  private async readBuildInfo(): Promise<BuildInfo | null> {
    try {
      const raw = await fs.readFile(path.join(this.extensionDistDir, "build-info.json"), "utf-8");
      const parsed = JSON.parse(raw) as Partial<BuildInfo>;
      if (!parsed.commitSha) return null;
      return {
        commitSha: parsed.commitSha,
        branch: parsed.branch ?? "unknown",
        commitTimestamp: parsed.commitTimestamp ?? "unknown",
        buildTimestamp: parsed.buildTimestamp ?? "unknown",
        schemaVersion: parsed.schemaVersion ?? "1",
      };
    } catch {
      return null;
    }
  }

  private async gitRevParse(ref: string): Promise<string | null> {
    try {
      const { stdout } = await execFileAsync("git", ["rev-parse", ref], {
        cwd: this.workspaceRoot,
      });
      return stdout.trim();
    } catch {
      return null;
    }
  }

  private async shaExistsInRepo(sha: string): Promise<boolean> {
    try {
      await execFileAsync("git", ["cat-file", "-e", sha], { cwd: this.workspaceRoot });
      return true;
    } catch {
      return false;
    }
  }

  private async countCommitsBehind(buildSha: string, currentSha: string): Promise<number> {
    try {
      const { stdout } = await execFileAsync(
        "git",
        ["rev-list", "--count", `${buildSha}..${currentSha}`],
        { cwd: this.workspaceRoot }
      );
      const n = Number.parseInt(stdout.trim(), 10);
      return Number.isFinite(n) ? n : 0;
    } catch {
      return 0;
    }
  }

  private async diffNames(buildSha: string, currentSha: string): Promise<string[]> {
    try {
      const { stdout } = await execFileAsync(
        "git",
        ["diff", "--name-only", `${buildSha}..${currentSha}`],
        { cwd: this.workspaceRoot, maxBuffer: 4 * 1024 * 1024 }
      );
      return stdout
        .split("\n")
        .map((s) => s.trim())
        .filter((s) => s.length > 0);
    } catch {
      return [];
    }
  }

  dispose(): void {
    if (this.timer !== null) {
      clearInterval(this.timer);
      this.timer = null;
    }
    this._onChanged.dispose();
  }
}

function isSameState(a: StalenessState, b: StalenessState): boolean {
  if (a.kind !== b.kind) return false;
  if (a.kind === "fresh" && b.kind === "fresh") {
    return a.buildSha === b.buildSha && a.currentSha === b.currentSha;
  }
  if (a.kind === "unknown" && b.kind === "unknown") {
    return a.reason === b.reason;
  }
  if (a.kind === "stale" && b.kind === "stale") {
    return (
      a.buildSha === b.buildSha &&
      a.currentSha === b.currentSha &&
      a.commitsBehind === b.commitsBehind &&
      a.criticalPathsChanged.length === b.criticalPathsChanged.length
    );
  }
  return false;
}

function summarize(s: StalenessState): Record<string, unknown> {
  if (s.kind === "fresh") return { kind: "fresh", buildSha: s.buildSha.slice(0, 8) };
  if (s.kind === "unknown") return { kind: "unknown", reason: s.reason };
  return {
    kind: "stale",
    buildSha: s.buildSha.slice(0, 8),
    currentSha: s.currentSha.slice(0, 8),
    commitsBehind: s.commitsBehind,
    criticalPathsChangedCount: s.criticalPathsChanged.length,
    otherPathsChangedCount: s.otherPathsChanged.length,
  };
}
