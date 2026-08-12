/**
 * resolveAgentRunnerRoot.test.ts
 *
 * Unit tests for the agent-runner root gating logic in bootstrap/services.ts.
 * #4117: the agent runner must no longer be gated solely on a single resolved
 * `incrediRoot` — a multi-root `.code-workspace` where WorkspaceManager has
 * discovered at least one repository, but folders[0] didn't resolve to a git
 * root, must still construct the runner.
 *
 * What is actually covered here: the five behavioral cases pin the exported
 * helper's resolution order (incrediRoot → first discovered repository →
 * null), and the source-pin arm at the end pins that the bootstrap's runner
 * gate binds and reads that helper's result. The construction wiring itself —
 * IssueQueueService / ConcurrentPipelineManager / AgentCommandStreamService —
 * is NOT executed by this file; `registerServices()` is never invoked.
 *
 * This file imports the real `resolveAgentRunnerRoot` exported from
 * bootstrap/services.ts rather than reimplementing it. The point of the guard
 * is what the SHIPPED code does when `incrediRoot` is null, and a
 * reimplementation cannot witness a regression in shipped code: this file
 * previously carried its own copy of the one-line fallback, so gutting
 * `resolveAgentRunnerRoot` to `return null` left every case here green while
 * the multi-root workspace it exists for silently lost its runner. Against the
 * imported symbol that same gutting now fails 4 of the 6 behavioral cases —
 * the falsifiability the mirror predecessor lacked. See docs/TESTING.md
 * § Testing Anti-Patterns, "Mirror Tests".
 *
 * Importing services.ts (101 imports) is costless in tests: tests/setup.ts
 * registers a suite-wide `vi.mock("vscode", ...)` via vitest `setupFiles`, and
 * `resolveAgentRunnerRoot` is a pure exported function — the module graph
 * loads under the global mock and nothing in it runs.
 *
 * @see Issue #4117 — Agent runner gated on a single incrediRoot
 * @see Issue #404 — mirror tests replaced by imports of the real symbol
 */

import { describe, it, expect, vi } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";

import { resolveAgentRunnerRoot } from "../../src/bootstrap/services";
import type { WorkspaceManager } from "../../src/services/WorkspaceManager";

const SERVICES_PATH = path.resolve(__dirname, "../../src/bootstrap/services.ts");
const servicesSource = readFileSync(SERVICES_PATH, "utf-8");

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * A WorkspaceManager stub carrying only the one method the function under test
 * calls, cast to the real type. The cast erases compile-time checking, and
 * tests/ in this package are not typechecked — so the cast is not what keeps
 * the stub honest. What does is that the real `resolveAgentRunnerRoot` consumes
 * it: change the shape it reads (`getAllRepositories()[0].path`) and these
 * cases fail at runtime. The cast stays because it is house style here (see
 * tests/bootstrap/Container.test.ts), and because adding an exported shim
 * interface to src to serve a test would be the same
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

  it("treats an empty-string incrediRoot as resolved — nullish coalescing, not truthiness", () => {
    // Pins `??` against an accidental `||`. Ship deliberately does not fall
    // through on a falsy-but-present incrediRoot; the rest of services.ts
    // truthiness-checks incrediRoot, so the divergence is pinned here on purpose.
    const workspaceManager = makeWorkspaceManager([{ path: "/workspace/repo-a" }]);
    expect(resolveAgentRunnerRoot("", workspaceManager)).toBe("");
  });

  it("the bootstrap gates the agent runner on resolveAgentRunnerRoot, not incrediRoot alone", () => {
    // The helper above is pure and fully covered; its ONLY production consumer is
    // the runner gate in registerServices(). Without this pin, reverting #4117 there
    // (`const runnerRoot = incrediRoot;`) leaves every case above green.
    expect(servicesSource).toMatch(
      /const runnerRoot = resolveAgentRunnerRoot\(\s*incrediRoot,\s*workspaceManager\s*\);\s*\n\s*if \(runnerRoot\) \{/
    );
  });
});
