/**
 * resolveAgentRunnerRoot.test.ts
 *
 * Unit tests for the agent-runner root gating logic in bootstrap/services.ts.
 * Verifies that IssueQueueService / ConcurrentPipelineManager / (transitively)
 * AgentCommandStreamService are no longer gated solely on a single resolved
 * `incrediRoot` — a multi-root `.code-workspace` where WorkspaceManager has
 * discovered at least one repository, but folders[0] didn't resolve to a git
 * root, must still construct the runner.
 *
 * `resolveAgentRunnerRoot` is exported from bootstrap/services.ts, but that
 * file has a very large import graph (100+ service modules, most importing
 * `vscode`) that isn't practical to mock for a focused unit test — the same
 * constraint documented in tests/bootstrap/goHistoryBridge.test.ts. Following
 * that file's established pattern, this test reimplements the pure gating
 * logic in isolation. Keep this mirror in sync with
 * `resolveAgentRunnerRoot` in src/bootstrap/services.ts.
 *
 * @see Issue #4117 — Agent runner gated on a single incrediRoot
 */

import { describe, it, expect, vi } from "vitest";

// ---------------------------------------------------------------------------
// Mirrors resolveAgentRunnerRoot() from src/bootstrap/services.ts
// ---------------------------------------------------------------------------

interface RepoLike {
  path: string;
}

interface WorkspaceManagerLike {
  getAllRepositories(): RepoLike[];
}

function resolveAgentRunnerRoot(
  incrediRootValue: string | null,
  workspaceManagerValue: WorkspaceManagerLike | null
): string | null {
  return incrediRootValue ?? workspaceManagerValue?.getAllRepositories()[0]?.path ?? null;
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function makeWorkspaceManager(repos: RepoLike[]): WorkspaceManagerLike {
  return {
    getAllRepositories: vi.fn().mockReturnValue(repos),
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("resolveAgentRunnerRoot (#4117)", () => {
  it("prefers incrediRoot when it resolved — no behavior change for single-root workspaces", () => {
    const workspaceManager = makeWorkspaceManager([{ path: "/other-repo" }]);
    expect(resolveAgentRunnerRoot("/resolved-git-root", workspaceManager)).toBe(
      "/resolved-git-root"
    );
  });

  it("prefers incrediRoot even when WorkspaceManager is null (pre-#4117 single-root path)", () => {
    expect(resolveAgentRunnerRoot("/resolved-git-root", null)).toBe("/resolved-git-root");
  });

  it("falls back to the first discovered repository when incrediRoot did not resolve", () => {
    // Multi-root .code-workspace: folders[0] isn't a git repo (or isn't the
    // intended target), so incrediRoot is null — but WorkspaceManager already
    // discovered every open folder as a repository (registration is
    // multi-repo aware). The runner should still construct against the first
    // discovered repo instead of never existing.
    const workspaceManager = makeWorkspaceManager([
      { path: "/workspace/repo-a" },
      { path: "/workspace/repo-b" },
    ]);
    expect(resolveAgentRunnerRoot(null, workspaceManager)).toBe("/workspace/repo-a");
  });

  it("returns null when incrediRoot is absent and WorkspaceManager is null — graceful no-op, no crash", () => {
    // No workspace folders open at all. Matches prior `if (incrediRoot)`
    // behavior: the runner simply doesn't construct.
    expect(resolveAgentRunnerRoot(null, null)).toBeNull();
  });

  it("returns null when incrediRoot is absent and WorkspaceManager discovered zero repositories — graceful no-op", () => {
    // e.g. explicit .vscode/nightgauge-workspace.yaml lists zero repos,
    // or N:1 shared-project derivation failed. Nothing to run against — the
    // runner must not construct, and this must not throw.
    const workspaceManager = makeWorkspaceManager([]);
    expect(resolveAgentRunnerRoot(null, workspaceManager)).toBeNull();
  });
});
