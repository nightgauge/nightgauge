/**
 * ExtensionStalenessService.test.ts (#3300)
 *
 * Verifies the staleness detection logic against a real ephemeral git repo
 * fixture so the service exercises real `git rev-parse`, `git rev-list`, and
 * `git diff --name-only` commands — not mocked. The build-info.json file is
 * written by the test setup to simulate "extension built at commit X".
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import {
  ExtensionStalenessService,
  CRITICAL_PATHS,
} from "../../src/services/ExtensionStalenessService";

const exec = promisify(execFile);

function silentLogger() {
  return {
    info: () => {},
    warn: () => {},
    error: () => {},
    debug: () => {},
  } as unknown as Parameters<typeof ExtensionStalenessService.prototype.constructor>[2];
}

async function gitInit(dir: string): Promise<void> {
  await exec("git", ["init", "-q"], { cwd: dir });
  await exec("git", ["config", "user.email", "t@t"], { cwd: dir });
  await exec("git", ["config", "user.name", "t"], { cwd: dir });
  await exec("git", ["config", "commit.gpgsign", "false"], { cwd: dir });
}

async function gitCommit(dir: string, message: string): Promise<string> {
  await exec("git", ["add", "-A"], { cwd: dir });
  await exec("git", ["commit", "-q", "-m", message, "--allow-empty"], { cwd: dir });
  const { stdout } = await exec("git", ["rev-parse", "HEAD"], { cwd: dir });
  return stdout.trim();
}

async function writeFile(dir: string, rel: string, contents: string): Promise<void> {
  const full = path.join(dir, rel);
  await fs.mkdir(path.dirname(full), { recursive: true });
  await fs.writeFile(full, contents, "utf-8");
}

async function writeBuildInfo(distDir: string, commitSha: string): Promise<void> {
  await fs.mkdir(distDir, { recursive: true });
  await fs.writeFile(
    path.join(distDir, "build-info.json"),
    JSON.stringify({
      commitSha,
      branch: "main",
      commitTimestamp: "2026-05-08T00:00:00Z",
      buildTimestamp: "2026-05-08T00:01:00Z",
      schemaVersion: "1",
    }),
    "utf-8"
  );
}

describe("ExtensionStalenessService (#3300)", () => {
  let tmpRoot: string;
  let workspaceDir: string;
  let distDir: string;

  beforeEach(async () => {
    tmpRoot = await fs.mkdtemp(path.join(os.tmpdir(), "ext-staleness-"));
    workspaceDir = path.join(tmpRoot, "workspace");
    distDir = path.join(tmpRoot, "dist");
    await fs.mkdir(workspaceDir, { recursive: true });
    await gitInit(workspaceDir);
  });

  afterEach(async () => {
    await fs.rm(tmpRoot, { recursive: true, force: true });
  });

  it("returns 'fresh' when build SHA equals workspace HEAD", async () => {
    await writeFile(workspaceDir, "README.md", "# initial");
    const sha = await gitCommit(workspaceDir, "initial");
    await writeBuildInfo(distDir, sha);

    const svc = new ExtensionStalenessService(distDir, workspaceDir, silentLogger());
    const state = await svc.refresh();
    expect(state.kind).toBe("fresh");
    if (state.kind === "fresh") {
      expect(state.buildSha).toBe(sha);
      expect(state.currentSha).toBe(sha);
    }
    expect(svc.isCriticallyStale()).toBe(false);
    svc.dispose();
  });

  it("returns 'unknown' when build-info.json is missing", async () => {
    await writeFile(workspaceDir, "README.md", "# initial");
    await gitCommit(workspaceDir, "initial");
    // no writeBuildInfo

    const svc = new ExtensionStalenessService(distDir, workspaceDir, silentLogger());
    const state = await svc.refresh();
    expect(state.kind).toBe("unknown");
    expect(svc.isCriticallyStale()).toBe(false);
    svc.dispose();
  });

  // Issue #3650 (Part B): the dev-install + release stamping pipelines have
  // assertions that fail loud if `dist/build-info.json` is missing or empty
  // post-build. Pin the reason string here so a future refactor that changes
  // the unknown reason doesn't silently desync the script-side messaging.
  it("missing build-info.json reports the stale-detection-blind reason verbatim", async () => {
    await writeFile(workspaceDir, "README.md", "# initial");
    await gitCommit(workspaceDir, "initial");
    // no writeBuildInfo

    const svc = new ExtensionStalenessService(distDir, workspaceDir, silentLogger());
    const state = await svc.refresh();
    expect(state.kind).toBe("unknown");
    if (state.kind === "unknown") {
      expect(state.reason).toContain("dist/build-info.json missing");
      expect(state.reason).toContain("extension built without provenance stamp");
    }
    svc.dispose();
  });

  // Issue #3650 (Part B): pin the malformed-JSON path. The check-build-info
  // helper (`packages/nightgauge-vscode/scripts/check-build-info.sh`)
  // rejects an unparseable file at build time so the reader never sees this
  // case — but if a future change removes that pre-check we want the reader
  // to still degrade gracefully instead of throwing.
  it("malformed build-info.json degrades to 'unknown' rather than throwing", async () => {
    await writeFile(workspaceDir, "README.md", "# initial");
    await gitCommit(workspaceDir, "initial");
    // Write a deliberately broken JSON file at the path readBuildInfo() reads.
    const fs = await import("node:fs/promises");
    const pathMod = await import("node:path");
    await fs.mkdir(distDir, { recursive: true });
    await fs.writeFile(pathMod.join(distDir, "build-info.json"), "{ not json", "utf-8");

    const svc = new ExtensionStalenessService(distDir, workspaceDir, silentLogger());
    const state = await svc.refresh();
    expect(state.kind).toBe("unknown");
    svc.dispose();
  });

  it("classifies as STALE on critical paths when those paths have new commits", async () => {
    // Build commit: just a README.
    await writeFile(workspaceDir, "README.md", "# initial");
    const buildSha = await gitCommit(workspaceDir, "initial");
    await writeBuildInfo(distDir, buildSha);

    // New commit on a critical path (skillRunner.ts).
    await writeFile(
      workspaceDir,
      "packages/nightgauge-vscode/src/utils/skillRunner.ts",
      "// updated"
    );
    await gitCommit(workspaceDir, "modify skillRunner");

    const svc = new ExtensionStalenessService(distDir, workspaceDir, silentLogger());
    const state = await svc.refresh();
    expect(state.kind).toBe("stale");
    if (state.kind === "stale") {
      expect(state.commitsBehind).toBe(1);
      expect(state.criticalPathsChanged).toContain(
        "packages/nightgauge-vscode/src/utils/skillRunner.ts"
      );
    }
    expect(svc.isCriticallyStale()).toBe(true);
    svc.dispose();
  });

  it("classifies as STALE but not critically so when only docs changed", async () => {
    await writeFile(workspaceDir, "README.md", "# initial");
    const buildSha = await gitCommit(workspaceDir, "initial");
    await writeBuildInfo(distDir, buildSha);

    // New commit only touches docs/.
    await writeFile(workspaceDir, "docs/CHANGELOG.md", "# changes");
    await gitCommit(workspaceDir, "update docs");

    const svc = new ExtensionStalenessService(distDir, workspaceDir, silentLogger());
    const state = await svc.refresh();
    expect(state.kind).toBe("stale");
    if (state.kind === "stale") {
      expect(state.criticalPathsChanged).toEqual([]);
      expect(state.otherPathsChanged.length).toBeGreaterThan(0);
    }
    // Critical staleness gate is FALSE — dispatch should still proceed.
    expect(svc.isCriticallyStale()).toBe(false);
    svc.dispose();
  });

  it("counts multiple commits behind correctly", async () => {
    await writeFile(workspaceDir, "README.md", "# initial");
    const buildSha = await gitCommit(workspaceDir, "initial");
    await writeBuildInfo(distDir, buildSha);

    // Three new commits, each touching a different critical path.
    await writeFile(workspaceDir, "internal/orchestrator/scheduler.go", "// 1");
    await gitCommit(workspaceDir, "scheduler change");
    await writeFile(
      workspaceDir,
      "packages/nightgauge-vscode/src/services/AutoRetroService.ts",
      "// 2"
    );
    await gitCommit(workspaceDir, "retro change");
    await writeFile(workspaceDir, "claude-plugins/nightgauge/hooks/lib/guard.sh", "# 3");
    await gitCommit(workspaceDir, "guard change");

    const svc = new ExtensionStalenessService(distDir, workspaceDir, silentLogger());
    const state = await svc.refresh();
    expect(state.kind).toBe("stale");
    if (state.kind === "stale") {
      expect(state.commitsBehind).toBe(3);
      expect(state.criticalPathsChanged.length).toBe(3);
    }
    svc.dispose();
  });

  it("CRITICAL_PATHS includes all expected pipeline-execution files", () => {
    expect(CRITICAL_PATHS).toContain("packages/nightgauge-vscode/src/utils/skillRunner.ts");
    expect(CRITICAL_PATHS).toContain("packages/nightgauge-vscode/src/services/AutoRetroService.ts");
    expect(CRITICAL_PATHS).toContain("internal/orchestrator/");
    expect(CRITICAL_PATHS).toContain("claude-plugins/nightgauge/hooks/");
  });

  it("transitions getState() through unknown -> fresh -> stale across refreshes", async () => {
    // Initial state before any refresh is 'unknown'.
    const svc = new ExtensionStalenessService(distDir, workspaceDir, silentLogger());
    expect(svc.getState().kind).toBe("unknown");

    // After build is stamped at HEAD, refresh yields 'fresh'.
    await writeFile(workspaceDir, "README.md", "# initial");
    const buildSha = await gitCommit(workspaceDir, "initial");
    await writeBuildInfo(distDir, buildSha);
    await svc.refresh();
    expect(svc.getState().kind).toBe("fresh");

    // After a new commit on a critical path, refresh yields 'stale' with
    // criticalPathsChanged populated.
    await writeFile(
      workspaceDir,
      "packages/nightgauge-vscode/src/utils/skillRunner.ts",
      "// updated"
    );
    await gitCommit(workspaceDir, "stale me");
    await svc.refresh();
    expect(svc.getState().kind).toBe("stale");
    expect(svc.isCriticallyStale()).toBe(true);
    svc.dispose();
  });
});
