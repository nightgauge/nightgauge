/**
 * nightgaugeConfig.token.test.ts
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
} from "../../src/utils/nightgaugeConfig";
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
    // Drop queued once-implementations a test left unconsumed (a tier that
    // short-circuits never reads the next file), so tests stay independent.
    mockReadFileSync.mockReset();
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

  it("ignores a literal token in the workspace config (repository tier: env: only)", () => {
    mockReadFileSync.mockReturnValue(`
github_auth:
  token: ghp_literal_token
`);
    expect(getGitHubAuthToken("/test/workspace")).toBeNull();
  });

  it("ignores a literal token in config.local.yaml (repository tier: env: only)", () => {
    // Only the local tier exists and it carries a literal: nothing is exported,
    // matching the binary, which refuses the config.
    mockResolveConfigPathSync.mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: false,
    });
    mockExistsSync.mockImplementation((p) => String(p).endsWith(".nightgauge/config.local.yaml"));
    mockReadFileSync.mockReturnValue("github_auth:\n  token: ghp_local_literal\n");
    expect(getGitHubAuthToken("/test/workspace")).toBeNull();
  });

  it("reads the machine config where the binary does (NIGHTGAUGE_CONFIG_HOME)", () => {
    process.env.NIGHTGAUGE_CONFIG_HOME = "/machine-home";
    mockResolveConfigPathSync.mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: false,
    });
    mockExistsSync.mockImplementation((p) => String(p) === "/machine-home/config.yaml");
    mockReadFileSync.mockImplementation((p) =>
      String(p) === "/machine-home/config.yaml" ? "github_auth:\n  token: ghp_machine_home\n" : ""
    );
    expect(getGitHubAuthToken("/test/workspace")).toBe("ghp_machine_home");
  });

  it("returns a literal token from the machine config", () => {
    mockResolveConfigPathSync.mockReturnValue({
      path: "/test/workspace/.nightgauge/config.yaml",
      isLegacy: false,
      exists: false,
    });
    mockExistsSync.mockImplementation((p) => !String(p).includes("/test/workspace/"));
    mockReadFileSync.mockReturnValue(`
github_auth:
  token: ghp_machine_literal
`);
    expect(getGitHubAuthToken("/test/workspace")).toBe("ghp_machine_literal");
  });

  it("prefers config.local.yaml (local tier) over the committed workspace config", () => {
    // The gitignored local override exists alongside the project config; its
    // token must win so concurrent workspaces owned by different GitHub users
    // each resolve their own per-repo token.
    mockExistsSync.mockImplementation((p) => String(p).endsWith(".nightgauge/config.local.yaml"));
    process.env.LOCAL_TIER_PAT = "ghp_local_tier";
    process.env.PROJECT_TIER_PAT = "ghp_project_tier";
    mockReadFileSync.mockImplementation((p) =>
      String(p).endsWith("config.local.yaml")
        ? "github_auth:\n  token: env:LOCAL_TIER_PAT\n"
        : "github_auth:\n  token: env:PROJECT_TIER_PAT\n"
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
    process.env.WORKSPACE_PAT = "ghp_workspace_token";
    // Workspace config has token
    mockReadFileSync
      .mockImplementationOnce(
        () => `
github_auth:
  token: env:WORKSPACE_PAT
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
    // Workspace tiers exist but carry no token; the machine config has a
    // literal one, which only that tier may hold.
    mockExistsSync.mockReturnValue(true);
    mockReadFileSync.mockImplementation((p) =>
      String(p).includes("/test/workspace/")
        ? "pipeline:\n  auto_fix: true\n"
        : "github_auth:\n  token: ghp_global_token\n"
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
    // Drop queued once-implementations a test left unconsumed (a tier that
    // short-circuits never reads the next file), so tests stay independent.
    mockReadFileSync.mockReset();
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
    process.env.ACME_PAT = "ghp_acme_token";
    process.env.MYORG_PAT = "ghp_myorg_token";
    mockReadFileSync.mockReturnValue(`
github_auth:
  tokens:
    acme: env:ACME_PAT
    myorg: env:MYORG_PAT
`);
    expect(getGitHubAuthTokens("/test/workspace")).toEqual({
      acme: "ghp_acme_token",
      myorg: "ghp_myorg_token",
    });
  });

  it("ignores literal tokens in the workspace config (repository tier: env: only)", () => {
    mockReadFileSync.mockReturnValue(`
github_auth:
  tokens:
    acme: ghp_acme_literal
`);
    expect(getGitHubAuthTokens("/test/workspace")).toEqual({});
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
    // Workspace config has org1 (as an env: reference, the only form a
    // repository tier accepts); the machine config has org1 + org2 literals.
    process.env.WORKSPACE_ORG1_PAT = "ghp_workspace_org1";
    mockExistsSync.mockImplementation((p) => !String(p).endsWith("config.local.yaml"));
    mockReadFileSync.mockImplementation((p) =>
      String(p).includes("/test/workspace/")
        ? "github_auth:\n  tokens:\n    org1: env:WORKSPACE_ORG1_PAT\n"
        : "github_auth:\n  tokens:\n    org1: ghp_global_org1\n    org2: ghp_global_org2\n"
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
