/**
 * sequentialRepoConfig.test.ts
 *
 * Unified concurrency model (#3781) — per-repo cap service:
 *   - reads resolve against `concurrency.{per_repo_max,repository_overrides}`
 *     via `ConfigBridge.getEffectiveConfig()`
 *   - the effective cap is `repository_overrides[repo] ?? per_repo_max ?? 1`
 *   - writes route through `IncrediYamlService.writeGlobal()` to
 *     `concurrency.repository_overrides.<repo>` (machine tier)
 *   - legacy runtime memento entries are best-effort cleared on every write
 *   - the pre-#3781 keys (autonomous.repositories.*) are gone — nothing here
 *     reads or writes them
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("vscode", () => ({}));

import {
  createSequentialRepoConfigService,
  DEFAULT_PER_REPO_MAX,
  DEFAULT_WORKSPACE_MAX,
  type RuntimeStateStoreLike,
  type ConfigBridgeLike,
  type MachineYamlWriterLike,
} from "../../src/utils/sequentialRepoConfig";
import { mergeConfigs } from "../../src/config/configMergeEngine";

interface RuntimeCall {
  op: "delete";
  path: string;
  opts?: { repoSlug?: string };
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

function makeMockConfigBridge(concurrency?: {
  workspace_max?: number;
  per_repo_max?: number;
  repository_overrides?: Record<string, number | undefined>;
}): ConfigBridgeLike {
  return {
    getEffectiveConfig: () => ({ config: { concurrency } }),
  };
}

describe("createSequentialRepoConfigService — writes through machine YAML", () => {
  let runtime: ReturnType<typeof makeMockRuntimeStore>;
  let yaml: ReturnType<typeof makeMockMachineYamlWriter>;

  beforeEach(() => {
    runtime = makeMockRuntimeStore();
    yaml = makeMockMachineYamlWriter();
  });

  it("writeRepoConcurrencyCap(N) writes concurrency.repository_overrides.<repo>=N", async () => {
    const svc = createSequentialRepoConfigService(runtime, makeMockConfigBridge(), yaml);

    await svc.writeRepoConcurrencyCap("nightgauge/repo-a", 3);

    expect(yaml.calls).toEqual([
      {
        partial: {
          concurrency: { repository_overrides: { "nightgauge/repo-a": 3 } },
        },
      },
    ]);
  });

  it("floors fractional input", async () => {
    const svc = createSequentialRepoConfigService(runtime, makeMockConfigBridge(), yaml);

    await svc.writeRepoConcurrencyCap("nightgauge/repo-a", 3.7);

    const partial = yaml.calls[0]?.partial as {
      concurrency?: { repository_overrides?: Record<string, number> };
    };
    expect(partial?.concurrency?.repository_overrides?.["nightgauge/repo-a"]).toBe(3);
  });

  it("clamps non-finite / <1 input up to the per-repo default", async () => {
    const svc = createSequentialRepoConfigService(runtime, makeMockConfigBridge(), yaml);

    await svc.writeRepoConcurrencyCap("nightgauge/repo-a", 0);
    await svc.writeRepoConcurrencyCap("nightgauge/repo-a", -5);
    await svc.writeRepoConcurrencyCap("nightgauge/repo-a", Number.NaN);

    for (const call of yaml.calls) {
      const partial = call.partial as {
        concurrency?: { repository_overrides?: Record<string, number> };
      };
      expect(partial?.concurrency?.repository_overrides?.["nightgauge/repo-a"]).toBe(
        DEFAULT_PER_REPO_MAX
      );
    }
  });

  it("clears both legacy runtime mementos on every write", async () => {
    const svc = createSequentialRepoConfigService(runtime, makeMockConfigBridge(), yaml);

    await svc.writeRepoConcurrencyCap("nightgauge/repo-a", 5);

    expect(runtime.calls).toEqual([
      { op: "delete", path: "sequential", opts: { repoSlug: "nightgauge/repo-a" } },
      { op: "delete", path: "max_concurrent", opts: { repoSlug: "nightgauge/repo-a" } },
    ]);
  });

  it("survives memento-delete failure (machine-tier write is authoritative)", async () => {
    runtime.delete = vi.fn(async () => {
      throw new Error("memento unavailable");
    });
    const svc = createSequentialRepoConfigService(runtime, makeMockConfigBridge(), yaml);

    await expect(svc.writeRepoConcurrencyCap("nightgauge/repo-a", 2)).resolves.toBeUndefined();
    expect(yaml.calls).toHaveLength(1);
  });
});

describe("createSequentialRepoConfigService — reads resolve the effective cap", () => {
  it("falls back to per_repo_max default (1) when nothing is set", () => {
    const svc = createSequentialRepoConfigService(
      makeMockRuntimeStore(),
      makeMockConfigBridge({}),
      makeMockMachineYamlWriter()
    );
    expect(svc.resolveRepoConcurrencyCap("nightgauge/repo-a")).toBe(DEFAULT_PER_REPO_MAX);
    expect(svc.readSequentialRepo("nightgauge/repo-a")).toBe(true);
    expect(svc.readMaxConcurrentRepo("nightgauge/repo-a")).toBe(1);
  });

  it("uses per_repo_max when no override exists", () => {
    const svc = createSequentialRepoConfigService(
      makeMockRuntimeStore(),
      makeMockConfigBridge({ per_repo_max: 2 }),
      makeMockMachineYamlWriter()
    );
    expect(svc.resolveRepoConcurrencyCap("nightgauge/repo-a")).toBe(2);
    expect(svc.readSequentialRepo("nightgauge/repo-a")).toBe(false);
  });

  it("an explicit override wins over per_repo_max", () => {
    const svc = createSequentialRepoConfigService(
      makeMockRuntimeStore(),
      makeMockConfigBridge({
        per_repo_max: 2,
        repository_overrides: { "nightgauge/repo-a": 4 },
      }),
      makeMockMachineYamlWriter()
    );
    expect(svc.resolveRepoConcurrencyCap("nightgauge/repo-a")).toBe(4);
  });

  it("matches an override by short folder name", () => {
    const svc = createSequentialRepoConfigService(
      makeMockRuntimeStore(),
      makeMockConfigBridge({ repository_overrides: { "repo-a": 3 } }),
      makeMockMachineYamlWriter()
    );
    expect(svc.resolveRepoConcurrencyCap("repo-a")).toBe(3);
  });

  it("matches an owner/repo override key when queried by short name", () => {
    const svc = createSequentialRepoConfigService(
      makeMockRuntimeStore(),
      makeMockConfigBridge({ repository_overrides: { "nightgauge/repo-a": 5 } }),
      makeMockMachineYamlWriter()
    );
    expect(svc.resolveRepoConcurrencyCap("repo-a")).toBe(5);
  });

  it("floors a fractional override and ignores <1 / non-finite overrides", () => {
    const svc = createSequentialRepoConfigService(
      makeMockRuntimeStore(),
      makeMockConfigBridge({
        per_repo_max: 2,
        repository_overrides: { frac: 4.9, bad: 0 },
      }),
      makeMockMachineYamlWriter()
    );
    expect(svc.resolveRepoConcurrencyCap("frac")).toBe(4);
    // `bad: 0` is invalid → falls back to per_repo_max
    expect(svc.resolveRepoConcurrencyCap("bad")).toBe(2);
  });

  it("falls back to defaults when getEffectiveConfig() is null (early activation)", () => {
    const bridge: ConfigBridgeLike = { getEffectiveConfig: () => null };
    const svc = createSequentialRepoConfigService(
      makeMockRuntimeStore(),
      bridge,
      makeMockMachineYamlWriter()
    );
    expect(svc.resolveRepoConcurrencyCap("nightgauge/repo-a")).toBe(DEFAULT_PER_REPO_MAX);
    expect(svc.readWorkspaceMax()).toBe(DEFAULT_WORKSPACE_MAX);
  });

  it("readWorkspaceMax returns the configured workspace_max", () => {
    const svc = createSequentialRepoConfigService(
      makeMockRuntimeStore(),
      makeMockConfigBridge({ workspace_max: 5 }),
      makeMockMachineYamlWriter()
    );
    expect(svc.readWorkspaceMax()).toBe(5);
  });
});

describe("createSequentialRepoConfigService — de-mock: real merge engine as bridge", () => {
  // These tests drive the service through the real configMergeEngine rather
  // than a hand-built stub, so a key rename in schema.ts propagates into a
  // test failure here rather than silently passing against stale mock data.

  function bridgeFromMerge(concurrencyPartial: {
    workspace_max?: number;
    per_repo_max?: number;
    repository_overrides?: Record<string, number>;
  }): ConfigBridgeLike {
    const result = mergeConfigs({
      project: { concurrency: concurrencyPartial },
    });
    return {
      getEffectiveConfig: () => ({ config: result.config }),
    };
  }

  it("resolves per-repo cap from real merge engine (override wins)", () => {
    const bridge = bridgeFromMerge({
      workspace_max: 3,
      per_repo_max: 2,
      repository_overrides: { myrepo: 5 },
    });
    const svc = createSequentialRepoConfigService(
      makeMockRuntimeStore(),
      bridge,
      makeMockMachineYamlWriter()
    );
    expect(svc.resolveRepoConcurrencyCap("myrepo")).toBe(5);
    expect(svc.resolveRepoConcurrencyCap("other")).toBe(2);
    expect(svc.readWorkspaceMax()).toBe(3);
  });

  it("falls back to per_repo_max from real merge engine when no override", () => {
    const bridge = bridgeFromMerge({ per_repo_max: 3 });
    const svc = createSequentialRepoConfigService(
      makeMockRuntimeStore(),
      bridge,
      makeMockMachineYamlWriter()
    );
    expect(svc.resolveRepoConcurrencyCap("any-repo")).toBe(3);
    expect(svc.readSequentialRepo("any-repo")).toBe(false);
  });

  it("uses defaults from real merge engine when concurrency block is empty", () => {
    const bridge = bridgeFromMerge({});
    const svc = createSequentialRepoConfigService(
      makeMockRuntimeStore(),
      bridge,
      makeMockMachineYamlWriter()
    );
    expect(svc.resolveRepoConcurrencyCap("any-repo")).toBe(DEFAULT_PER_REPO_MAX);
    expect(svc.readWorkspaceMax()).toBe(DEFAULT_WORKSPACE_MAX);
  });
});
