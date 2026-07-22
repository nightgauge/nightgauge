/**
 * incrediConfig.token.test.ts
 *
 * Unit tests for the GitHub auth token functions added in Issue #2670:
 *   - expandEnvVar()
 *   - getGitHubAuthToken()
 *   - getGitHubAuthTokens()
 *
 * @see Issue #2670 - Update extension token resolution to prefer config tokens
 * @see Issue #2663 - Per-project and per-org GitHub token config
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs";

// Mock vscode module
vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/test/workspace" } }],
  },
}));

// Mock configPathResolver
vi.mock("../../src/utils/configPathResolver", () => ({
  resolveConfigPathSync: vi.fn(),
  logDeprecationWarning: vi.fn(),
}));

// Mock fs
vi.mock("node:fs", () => ({
  readFileSync: vi.fn(),
  existsSync: vi.fn(),
}));

import {
  expandEnvVar,
  getGitHubAuthToken,
  getGitHubAuthTokens,
} from "../../src/utils/incrediConfig";
import { resolveConfigPathSync } from "../../src/utils/configPathResolver";

const mockResolveConfigPathSync = vi.mocked(resolveConfigPathSync);
const mockReadFileSync = vi.mocked(fs.readFileSync);
const mockExistsSync = vi.mocked(fs.existsSync);

describe("expandEnvVar", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    process.env = { ...originalEnv };
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  it("returns literal value when it does not start with env:", () => {
    expect(expandEnvVar("ghp_abc123")).toBe("ghp_abc123");
  });

  it("expands env:VAR_NAME to process.env value", () => {
    process.env.MY_PAT = "ghp_from_env";
    expect(expandEnvVar("env:MY_PAT")).toBe("ghp_from_env");
  });

  it("returns null when env:VAR_NAME env var is not set", () => {
    delete process.env.UNSET_TOKEN;
    expect(expandEnvVar("env:UNSET_TOKEN")).toBeNull();
  });

  it("returns null when env:VAR_NAME env var is empty string", () => {
    process.env.EMPTY_TOKEN = "";
    expect(expandEnvVar("env:EMPTY_TOKEN")).toBeNull();
  });

  it("returns null when env: prefix has no var name", () => {
    expect(expandEnvVar("env:")).toBeNull();
  });

  it("returns null when env: prefix has only whitespace var name", () => {
    expect(expandEnvVar("env:  ")).toBeNull();
  });
});

describe("getGitHubAuthToken", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    vi.clearAllMocks();
    process.env = { ...originalEnv };

    // Default: workspace config exists
    mockResolveConfigPathSync.mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });

    // Default: no global config
    mockExistsSync.mockReturnValue(false);
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  it("returns null when no config file exists", () => {
    mockResolveConfigPathSync.mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: false,
    });
    expect(getGitHubAuthToken("/test/workspace")).toBeNull();
  });

  it("returns literal token from github_auth.token", () => {
    mockReadFileSync.mockReturnValue(`
github_auth:
  token: ghp_literal_token
`);
    expect(getGitHubAuthToken("/test/workspace")).toBe("ghp_literal_token");
  });

  it("prefers config.local.yaml (local tier) over the committed workspace config", () => {
    // The gitignored local override exists alongside the project config; its
    // token must win so concurrent workspaces owned by different GitHub users
    // each resolve their own per-repo token.
    mockExistsSync.mockImplementation((p) => String(p).endsWith(".nightgauge/config.local.yaml"));
    mockReadFileSync.mockImplementation((p) =>
      String(p).endsWith("config.local.yaml")
        ? "github_auth:\n  token: ghp_local_tier\n"
        : "github_auth:\n  token: ghp_project_tier\n"
    );
    expect(getGitHubAuthToken("/test/workspace")).toBe("ghp_local_tier");
  });

  it("returns null when github_auth.token is missing from config", () => {
    mockReadFileSync.mockReturnValue(`
github_auth:
  users:
    acme: someuser
`);
    expect(getGitHubAuthToken("/test/workspace")).toBeNull();
  });

  it("expands env:VAR_NAME in token value", () => {
    process.env.MY_PAT = "ghp_expanded";
    mockReadFileSync.mockReturnValue(`
github_auth:
  token: env:MY_PAT
`);
    expect(getGitHubAuthToken("/test/workspace")).toBe("ghp_expanded");
  });

  it("returns null when env:VAR_NAME expansion fails (env var not set)", () => {
    delete process.env.MISSING_PAT;
    mockReadFileSync.mockReturnValue(`
github_auth:
  token: env:MISSING_PAT
`);
    expect(getGitHubAuthToken("/test/workspace")).toBeNull();
  });

  it("returns null when config has no github_auth section", () => {
    mockReadFileSync.mockReturnValue(`
pipeline:
  auto_fix: true
`);
    expect(getGitHubAuthToken("/test/workspace")).toBeNull();
  });

  it("prefers workspace config over global config", () => {
    mockExistsSync.mockImplementation((p) => String(p).includes(".nightgauge/config.yaml"));
    // Workspace config has token
    mockReadFileSync
      .mockImplementationOnce(
        () => `
github_auth:
  token: ghp_workspace_token
`
      )
      .mockImplementationOnce(
        () => `
github_auth:
  token: ghp_global_token
`
      );
    expect(getGitHubAuthToken("/test/workspace")).toBe("ghp_workspace_token");
  });

  it("falls back to global config when workspace config has no token", () => {
    // Workspace config exists but has no token
    mockReadFileSync.mockImplementationOnce(
      () => `
pipeline:
  auto_fix: true
`
    );
    // Global config exists and has token
    mockExistsSync.mockReturnValue(true);
    mockReadFileSync.mockImplementationOnce(
      () => `
github_auth:
  token: ghp_global_token
`
    );
    expect(getGitHubAuthToken("/test/workspace")).toBe("ghp_global_token");
  });

  it("returns null on config read error", () => {
    mockReadFileSync.mockImplementation(() => {
      throw new Error("ENOENT");
    });
    expect(getGitHubAuthToken("/test/workspace")).toBeNull();
  });
});

describe("getGitHubAuthTokens", () => {
  const originalEnv = process.env;

  beforeEach(() => {
    vi.clearAllMocks();
    process.env = { ...originalEnv };

    mockResolveConfigPathSync.mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: true,
    });

    mockExistsSync.mockReturnValue(false);
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  it("returns empty object when no config file exists", () => {
    mockResolveConfigPathSync.mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: false,
    });
    expect(getGitHubAuthTokens("/test/workspace")).toEqual({});
  });

  it("returns empty object when github_auth.tokens section is missing", () => {
    mockReadFileSync.mockReturnValue(`
github_auth:
  token: ghp_direct
`);
    expect(getGitHubAuthTokens("/test/workspace")).toEqual({});
  });

  it("returns org-to-token mapping from github_auth.tokens", () => {
    mockReadFileSync.mockReturnValue(`
github_auth:
  tokens:
    acme: ghp_acme_token
    myorg: ghp_myorg_token
`);
    expect(getGitHubAuthTokens("/test/workspace")).toEqual({
      acme: "ghp_acme_token",
      myorg: "ghp_myorg_token",
    });
  });

  it("expands env:VAR_NAME in token values", () => {
    process.env.ACME_PAT = "ghp_acme_expanded";
    mockReadFileSync.mockReturnValue(`
github_auth:
  tokens:
    acme: env:ACME_PAT
`);
    expect(getGitHubAuthTokens("/test/workspace")).toEqual({
      acme: "ghp_acme_expanded",
    });
  });

  it("skips entries where env:VAR_NAME is not set", () => {
    delete process.env.MISSING_PAT;
    process.env.PRESENT_PAT = "ghp_present";
    mockReadFileSync.mockReturnValue(`
github_auth:
  tokens:
    org1: env:MISSING_PAT
    org2: env:PRESENT_PAT
`);
    expect(getGitHubAuthTokens("/test/workspace")).toEqual({
      org2: "ghp_present",
    });
  });

  it("returns empty object when config has no github_auth section", () => {
    mockReadFileSync.mockReturnValue(`
pipeline:
  auto_fix: true
`);
    expect(getGitHubAuthTokens("/test/workspace")).toEqual({});
  });

  it("merges workspace and global config without duplicating entries", () => {
    // Workspace config has org1
    mockReadFileSync.mockImplementationOnce(
      () => `
github_auth:
  tokens:
    org1: ghp_workspace_org1
`
    );
    // Global config has org1 + org2
    mockExistsSync.mockReturnValue(true);
    mockReadFileSync.mockImplementationOnce(
      () => `
github_auth:
  tokens:
    org1: ghp_global_org1
    org2: ghp_global_org2
`
    );
    const result = getGitHubAuthTokens("/test/workspace");
    // org1 from workspace takes precedence; org2 comes from global
    expect(result.org1).toBe("ghp_workspace_org1");
    expect(result.org2).toBe("ghp_global_org2");
  });

  it("returns empty object on config read error", () => {
    mockReadFileSync.mockImplementation(() => {
      throw new Error("ENOENT");
    });
    expect(getGitHubAuthTokens("/test/workspace")).toEqual({});
  });
});
