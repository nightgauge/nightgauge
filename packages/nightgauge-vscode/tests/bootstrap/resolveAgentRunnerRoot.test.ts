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
 * Like tests/bootstrap/terminalFunnelTarget.test.ts (#302), this file imports
 * the real `resolveAgentRunnerRoot` exported from bootstrap/services.ts rather
 * than reimplementing it. The point of the guard is what the SHIPPED code does
 * when `incrediRoot` is null, and a reimplementation cannot witness a
 * regression in shipped code: this file previously carried its own copy of the
 * one-line fallback, so gutting `resolveAgentRunnerRoot` to `return null` left
 * every case here green while the multi-root workspace it exists for silently
 * lost its runner.
 *
 * @see Issue #4117 — Agent runner gated on a single incrediRoot
 * @see Issue #404 — mirror tests replaced by imports of the real symbol
 */

import { describe, it, expect, vi } from "vitest";

import { resolveAgentRunnerRoot } from "../../src/bootstrap/services";
import type { WorkspaceManager } from "../../src/services/WorkspaceManager";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * A WorkspaceManager stub carrying only the one method the function under test
 * calls. Cast rather than shimmed: the real type is the contract, and adding an
 * exported shim interface to src to serve a test would be the same
 * test-shaped-code-in-ship problem from the other direction.
 */
function makeWorkspaceManager(repos: Array<{ path: string }>): WorkspaceManager {
  return {
    getAllRepositories: vi.fn().mockReturnValue(repos),
  } as unknown as WorkspaceManager;
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
