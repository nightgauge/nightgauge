/**
 * perRepoGitHubTokenEnv.test.ts
 *
 * Verifies that the per-repo token is mirrored into BOTH the terminal
 * EnvironmentVariableCollection and the extension host's own process.env, and
 * that the extension-host env is restored to its ambient value (never clobbered)
 * when no per-repo token resolves.
 *
 * @see Issue #2487, #2670 — config-based per-repo token resolution
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

const fsWatcher = {
  onDidCreate: vi.fn(),
  onDidChange: vi.fn(),
  onDidDelete: vi.fn(),
  dispose: vi.fn(),
};

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/test/workspace" } }],
    onDidChangeWorkspaceFolders: vi.fn(() => ({ dispose: vi.fn() })),
    createFileSystemWatcher: vi.fn(() => fsWatcher),
  },
}));

const mockResolve = vi.fn();
vi.mock("../../src/utils/skillRunner", () => ({
  resolveTokenForSubprocess: (root: string) => mockResolve(root),
}));

import { applyPerRepoGitHubTokenEnv } from "../../src/utils/perRepoGitHubTokenEnv";

function makeContext() {
  const store = new Map<string, string>();
  return {
    subscriptions: [] as unknown[],
    environmentVariableCollection: {
      description: "",
      replace: vi.fn((k: string, v: string) => store.set(k, v)),
      delete: vi.fn((k: string) => store.delete(k)),
      _store: store,
    },
  } as unknown as import("vscode").ExtensionContext;
}

describe("applyPerRepoGitHubTokenEnv", () => {
  const origGh = process.env.GH_TOKEN;
  const origGithub = process.env.GITHUB_TOKEN;

  beforeEach(() => {
    mockResolve.mockReset();
    delete process.env.GH_TOKEN;
    delete process.env.GITHUB_TOKEN;
  });

  afterEach(() => {
    if (origGh === undefined) delete process.env.GH_TOKEN;
    else process.env.GH_TOKEN = origGh;
    if (origGithub === undefined) delete process.env.GITHUB_TOKEN;
    else process.env.GITHUB_TOKEN = origGithub;
  });

  it("injects the per-repo token into both process.env and the terminal collection", () => {
    mockResolve.mockReturnValue({ token: "ghp_repo_a" });
    const ctx = makeContext();

    applyPerRepoGitHubTokenEnv(ctx);

    // Extension host: direct `gh`/`gh api` calls inherit process.env.
    expect(process.env.GH_TOKEN).toBe("ghp_repo_a");
    expect(process.env.GITHUB_TOKEN).toBe("ghp_repo_a");
    // Integrated terminals: the env collection.
    expect(ctx.environmentVariableCollection.replace).toHaveBeenCalledWith(
      "GH_TOKEN",
      "ghp_repo_a"
    );
    expect(ctx.environmentVariableCollection.replace).toHaveBeenCalledWith(
      "GITHUB_TOKEN",
      "ghp_repo_a"
    );
  });

  it("restores ambient process.env (never clobbers it) when no per-repo token resolves", () => {
    process.env.GH_TOKEN = "ambient_token";
    mockResolve.mockReturnValue(null);
    const ctx = makeContext();

    applyPerRepoGitHubTokenEnv(ctx);

    // Ambient value preserved verbatim — not deleted, not overwritten.
    expect(process.env.GH_TOKEN).toBe("ambient_token");
    // Terminal collection override is cleared so the ambient gh auth shows through.
    expect(ctx.environmentVariableCollection.delete).toHaveBeenCalledWith("GH_TOKEN");
  });

  it("is fail-safe: never throws when token resolution errors", () => {
    mockResolve.mockImplementation(() => {
      throw new Error("resolution boom");
    });
    const ctx = makeContext();

    expect(() => applyPerRepoGitHubTokenEnv(ctx)).not.toThrow();
  });
});
