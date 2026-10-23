/**
 * enabledReposConfig.test.ts
 *
 * Issue #3641 — covers the machine-tier writer:
 *   - writeEnabledRepos writes to ~/.nightgauge/config.yaml via writeGlobal
 *   - writeEnabledRepos always clears the legacy runtime memento entry
 *   - writeEnabledRepos with empty list writes `enabled_repos: []` to machine
 *     (literal scan-all sentinel) — does NOT delete from machine
 *   - readEnabledRepos still sources from `ConfigBridge.getEffectiveConfig()`
 *   - filters non-string and whitespace-only entries
 *   - pure helpers (entryMatchesRepo, isRepoEnabledForAutonomous) unchanged
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("vscode", () => ({}));

import {
  createEnabledReposConfigService,
  entryMatchesRepo,
  isRepoEnabledForAutonomous,
  type RuntimeStateStoreLike,
  type ConfigBridgeLike,
  type MachineYamlWriterLike,
} from "../../src/utils/enabledReposConfig";

interface RuntimeCall {
  op: "delete";
  path: string;
  opts?: { repoSlug?: string; scope?: "workspace" | "global" };
}

function makeMockRuntimeStore(): RuntimeStateStoreLike & { calls: RuntimeCall[] } {
  const calls: RuntimeCall[] = [];
  return {
    calls,
    delete: vi.fn(async (path, opts) => {
      calls.push({ op: "delete", path, opts });
    }),
  };
}

interface YamlCall {
  partial: unknown;
}

function makeMockMachineYamlWriter(): MachineYamlWriterLike & { calls: YamlCall[] } {
  const calls: YamlCall[] = [];
  return {
    calls,
    writeGlobal: vi.fn(async (partial) => {
      calls.push({ partial });
      return { success: true };
    }),
  };
}

function makeMockConfigBridge(enabledRepos?: unknown): ConfigBridgeLike {
  return {
    getEffectiveConfig: () => ({
      config: {
        autonomous: {
          enabled_repos: enabledRepos,
        },
      },
    }),
  };
}

describe("createEnabledReposConfigService — writes through machine YAML (#3641)", () => {
  let runtime: ReturnType<typeof makeMockRuntimeStore>;
  let yaml: ReturnType<typeof makeMockMachineYamlWriter>;

  beforeEach(() => {
    runtime = makeMockRuntimeStore();
    yaml = makeMockMachineYamlWriter();
  });

  it("writeEnabledRepos with non-empty list writes to machine YAML at autonomous.enabled_repos", async () => {
    const svc = createEnabledReposConfigService(runtime, makeMockConfigBridge(), yaml);

    await svc.writeEnabledRepos(["alpha", "beta"]);

    expect(yaml.calls).toEqual([{ partial: { autonomous: { enabled_repos: ["alpha", "beta"] } } }]);
  });

  it("writeEnabledRepos with empty list writes literal `enabled_repos: []` to machine (scan-all sentinel)", async () => {
    // The bug from #3641 had the v1 implementation deleting the runtime
    // memento when selected==[], which let lower tiers (machine YAML's
    // explicit short list) shadow back through. Writing the literal []
    // to machine puts the scan-all decision in the authoritative tier.
    const svc = createEnabledReposConfigService(runtime, makeMockConfigBridge(), yaml);

    await svc.writeEnabledRepos([]);

    expect(yaml.calls).toEqual([{ partial: { autonomous: { enabled_repos: [] } } }]);
  });

  it("writeEnabledRepos clears the legacy runtime memento at BOTH scopes on every write (#3650)", async () => {
    // Issue #3650 (Part C): legacy code wrote at `scope: "workspace"`, but
    // defensive cleanup must also clear `scope: "global"` because the v2
    // legacy-keys migration is gated by a globalState STATE_KEY and runs
    // at most once per VSCode install (not once per workspace). Multi-
    // worktree users keep a stale workspaceState overlay shadowing the
    // machine YAML in every workspace except the one the migration ran
    // in — clearing both scopes here unblocks every workspace on its
    // first checkbox toggle.
    const svc = createEnabledReposConfigService(runtime, makeMockConfigBridge(), yaml);

    await svc.writeEnabledRepos(["alpha"]);

    expect(runtime.calls).toEqual([
      { op: "delete", path: "autonomous.enabled_repos", opts: { scope: "workspace" } },
      { op: "delete", path: "autonomous.enabled_repos", opts: { scope: "global" } },
    ]);
  });

  it("writeEnabledRepos clears the legacy memento (both scopes) even when the write is empty (scan-all)", async () => {
    const svc = createEnabledReposConfigService(runtime, makeMockConfigBridge(), yaml);

    await svc.writeEnabledRepos([]);

    expect(runtime.calls).toEqual([
      { op: "delete", path: "autonomous.enabled_repos", opts: { scope: "workspace" } },
      { op: "delete", path: "autonomous.enabled_repos", opts: { scope: "global" } },
    ]);
  });

  it("writeEnabledRepos survives memento-delete failures (machine-tier write is authoritative)", async () => {
    runtime.delete = vi.fn(async () => {
      throw new Error("memento unavailable");
    });
    const svc = createEnabledReposConfigService(runtime, makeMockConfigBridge(), yaml);

    await expect(svc.writeEnabledRepos(["alpha"])).resolves.toBeUndefined();
    expect(yaml.calls).toHaveLength(1); // machine write still landed
  });

  it("writeEnabledRepos: a workspace-scope delete failure does NOT prevent the global-scope delete (#3650)", async () => {
    // Defensive: the two cleanup calls live in independent try/catch
    // blocks. If the workspace delete throws, the global delete must
    // still run so a partially-cleaned cache doesn't leave the runtime
    // overlay alive on the second scope.
    let workspaceCalls = 0;
    let globalCalls = 0;
    const partialFailRuntime: RuntimeStateStoreLike = {
      delete: vi.fn(async (_path, opts) => {
        if (opts?.scope === "workspace") {
          workspaceCalls++;
          throw new Error("workspace memento unavailable");
        }
        if (opts?.scope === "global") {
          globalCalls++;
        }
      }),
    };
    const svc = createEnabledReposConfigService(partialFailRuntime, makeMockConfigBridge(), yaml);

    await expect(svc.writeEnabledRepos(["alpha"])).resolves.toBeUndefined();
    expect(workspaceCalls).toBe(1);
    expect(globalCalls).toBe(1);
    expect(yaml.calls).toHaveLength(1);
  });
});

describe("createEnabledReposConfigService — reads through merged config", () => {
  it("readEnabledRepos returns the merged value when set", () => {
    const bridge = makeMockConfigBridge(["alpha", "beta"]);
    const svc = createEnabledReposConfigService(
      makeMockRuntimeStore(),
      bridge,
      makeMockMachineYamlWriter()
    );

    expect(svc.readEnabledRepos()).toEqual(["alpha", "beta"]);
  });

  it("readEnabledRepos returns [] when no tier sets the key (merged-view fall-through)", () => {
    const bridge = makeMockConfigBridge(undefined);
    const svc = createEnabledReposConfigService(
      makeMockRuntimeStore(),
      bridge,
      makeMockMachineYamlWriter()
    );

    expect(svc.readEnabledRepos()).toEqual([]);
  });

  it("readEnabledRepos returns [] when getEffectiveConfig() is null", () => {
    const bridge: ConfigBridgeLike = { getEffectiveConfig: () => null };
    const svc = createEnabledReposConfigService(
      makeMockRuntimeStore(),
      bridge,
      makeMockMachineYamlWriter()
    );

    expect(svc.readEnabledRepos()).toEqual([]);
  });

  it("readEnabledRepos filters non-string entries and whitespace-only strings", () => {
    const bridge = makeMockConfigBridge(["alpha", "", "  ", 42, null, "beta"]);
    const svc = createEnabledReposConfigService(
      makeMockRuntimeStore(),
      bridge,
      makeMockMachineYamlWriter()
    );

    expect(svc.readEnabledRepos()).toEqual(["alpha", "beta"]);
  });

  it("readEnabledRepos returns [] when value is not an array", () => {
    const bridge = makeMockConfigBridge("not-an-array");
    const svc = createEnabledReposConfigService(
      makeMockRuntimeStore(),
      bridge,
      makeMockMachineYamlWriter()
    );

    expect(svc.readEnabledRepos()).toEqual([]);
  });
});

describe("entryMatchesRepo (pure helper, unchanged)", () => {
  it("matches short names case-insensitively", () => {
    expect(entryMatchesRepo("acme-platform", "Acme-Platform")).toBe(true);
  });

  it("matches fully-qualified names by the post-slash segment", () => {
    expect(entryMatchesRepo("acme/acme-platform", "acme-platform")).toBe(true);
  });

  it("returns false for non-matches", () => {
    expect(entryMatchesRepo("nightgauge", "acme-platform")).toBe(false);
  });

  it("tolerates whitespace and empty entries", () => {
    expect(entryMatchesRepo("  ", "anything")).toBe(false);
    expect(entryMatchesRepo("  nightgauge ", "nightgauge")).toBe(true);
  });
});

describe("isRepoEnabledForAutonomous (pure helper, unchanged)", () => {
  it("treats empty enabled_repos as scan-all (every repo enabled)", () => {
    expect(isRepoEnabledForAutonomous("platform", [])).toBe(true);
  });

  it("returns true for listed short name", () => {
    expect(isRepoEnabledForAutonomous("platform", ["platform"])).toBe(true);
  });

  it("returns true for listed fully-qualified name", () => {
    expect(isRepoEnabledForAutonomous("platform", ["nightgauge/platform"])).toBe(true);
  });

  it("returns false for repo not in the allowlist", () => {
    expect(isRepoEnabledForAutonomous("angular", ["platform"])).toBe(false);
  });
});
