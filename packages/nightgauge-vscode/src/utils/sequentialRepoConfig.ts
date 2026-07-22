/**
 * Per-repo concurrency cap, resolved against the unified concurrency model.
 *
 * Concurrency is owned by ONE machine-tier block (#3781), the single source
 * of truth read by both the Go binary and this extension:
 *
 *   concurrency:
 *     workspace_max: 3          # max issues running across ALL repos, combined
 *     per_repo_max: 1           # default cap within a SINGLE repo (1 = serialize)
 *     repository_overrides:     # optional per-repo override of per_repo_max
 *       my-repo: 2
 *       nightgauge/other-repo: 3
 *
 * The effective cap for a repo is:
 *   repository_overrides["owner/repo"] ?? repository_overrides["repo"]
 *     ?? per_repo_max ?? DEFAULT_PER_REPO_MAX (1)
 *
 * Every repo therefore has a defined cap — there is no "unlimited per repo"
 * state; the most a single repo can run is `workspace_max`. Writes route
 * through `IncrediYamlService.writeGlobal()` (machine tier — the only tier
 * the Go binary reads from inside a worktree) and best-effort clear any
 * stale legacy runtime-memento entries.
 *
 * The pre-#3781 keys (`autonomous.repositories.<repo>.sequential` and
 * `.max_concurrent`, `pipeline.max_concurrent`, `autonomous.max_concurrent`)
 * were deleted with no backwards-compat path; nothing reads or writes them.
 *
 * @see internal/config/config.go — ResolveConcurrency / CapForRepo
 * @see internal/config/merge.go — LoadMerged
 * @see docs/SETTINGS_ARCHITECTURE.md — "Tier 2: Machine"
 */

import type { ConfigBridge } from "../services/ConfigBridge";
import type { RuntimeStateStore } from "../config/RuntimeStateStore";
import type { IncrediYamlService } from "../views/settings/IncrediYamlService";
import type { IncrediConfig } from "../config/schema";

/** Fallback when `concurrency.per_repo_max` is unset. Mirrors Go `DefaultPerRepoMax`. */
export const DEFAULT_PER_REPO_MAX = 1;
/** Fallback when `concurrency.workspace_max` is unset. Mirrors Go `DefaultWorkspaceMax`. */
export const DEFAULT_WORKSPACE_MAX = 3;

/**
 * Subset of `RuntimeStateStore` this module depends on. Only the legacy-
 * memento cleanup path remains here after #3641 routed writes to machine
 * YAML.
 */
export interface RuntimeStateStoreLike {
  delete(path: string, opts?: { repoSlug?: string }): Promise<void>;
}

/**
 * Subset of `ConfigBridge` this module reads through. Allows tests to
 * supply a stub merged config without spinning up a real bridge.
 */
export interface ConfigBridgeLike {
  getEffectiveConfig(): {
    config?: {
      concurrency?: {
        workspace_max?: number;
        per_repo_max?: number;
        repository_overrides?: Record<string, number | undefined>;
      };
    };
  } | null;
}

/**
 * Subset of `IncrediYamlService` this module writes through.
 */
export interface MachineYamlWriterLike {
  writeGlobal(partial: Partial<IncrediConfig>): Promise<{ success: boolean; error?: string }>;
}

/**
 * Methods exposed by the per-repo concurrency service.
 */
export interface SequentialRepoConfigService {
  /** Resolved effective cap for the repo (always ≥1). */
  resolveRepoConcurrencyCap(repoName: string): number;
  /** True when the resolved cap is exactly 1 (serialized). */
  readSequentialRepo(repoName: string): boolean;
  /** Resolved effective cap — alias retained for tree-item rendering. */
  readMaxConcurrentRepo(repoName: string): number;
  /** The workspace-wide combined ceiling (defaults to {@link DEFAULT_WORKSPACE_MAX}). */
  readWorkspaceMax(): number;
  /** Write an explicit per-repo override (cap ≥1). */
  writeRepoConcurrencyCap(repoName: string, cap: number): Promise<void>;
}

/**
 * Factory: capture a `RuntimeStateStore` for legacy-memento cleanup, a
 * `ConfigBridge` for merged reads, and an `IncrediYamlService` (or stub)
 * for machine-tier writes.
 */
export function createSequentialRepoConfigService(
  runtimeStore: RuntimeStateStoreLike | RuntimeStateStore,
  configBridge: ConfigBridgeLike | ConfigBridge,
  machineYamlWriter: MachineYamlWriterLike | IncrediYamlService
): SequentialRepoConfigService {
  const store = runtimeStore as RuntimeStateStoreLike;
  const bridge = configBridge as ConfigBridgeLike;
  const writer = machineYamlWriter as MachineYamlWriterLike;

  function readConcurrency() {
    return bridge.getEffectiveConfig()?.config?.concurrency ?? undefined;
  }

  function perRepoDefault(): number {
    const v = readConcurrency()?.per_repo_max;
    return typeof v === "number" && Number.isFinite(v) && v >= 1
      ? Math.floor(v)
      : DEFAULT_PER_REPO_MAX;
  }

  function lookupOverride(repoName: string): number | undefined {
    const overrides = readConcurrency()?.repository_overrides;
    if (!overrides) return undefined;
    // Extract the short name (segment after the last "/") so both lookup
    // directions work, mirroring Go's CapForRepo logic:
    //   query="nightgauge/flutter", key="flutter"  → short match
    //   query="flutter",          key="nightgauge/flutter" → suffix match
    const slashIdx = repoName.lastIndexOf("/");
    const shortName = slashIdx >= 0 ? repoName.slice(slashIdx + 1) : repoName;
    const candidates = Object.keys(overrides).filter(
      (k) => k === repoName || k.endsWith(`/${repoName}`) || k === shortName
    );
    const key = candidates.find((k) => k === repoName) ?? candidates[0];
    if (key === undefined) return undefined;
    const v = overrides[key];
    return typeof v === "number" && Number.isFinite(v) && v >= 1 ? Math.floor(v) : undefined;
  }

  async function clearLegacyMemento(field: "sequential" | "max_concurrent", repoSlug: string) {
    try {
      await store.delete(field, { repoSlug });
    } catch {
      // Best-effort — machine-tier write is the authoritative source.
    }
  }

  return {
    resolveRepoConcurrencyCap(repoName: string): number {
      return lookupOverride(repoName) ?? perRepoDefault();
    },

    readSequentialRepo(repoName: string): boolean {
      return this.resolveRepoConcurrencyCap(repoName) === 1;
    },

    readMaxConcurrentRepo(repoName: string): number {
      return this.resolveRepoConcurrencyCap(repoName);
    },

    readWorkspaceMax(): number {
      const v = readConcurrency()?.workspace_max;
      return typeof v === "number" && Number.isFinite(v) && v >= 1
        ? Math.floor(v)
        : DEFAULT_WORKSPACE_MAX;
    },

    async writeRepoConcurrencyCap(repoName: string, cap: number): Promise<void> {
      const value = Number.isFinite(cap) && cap >= 1 ? Math.floor(cap) : DEFAULT_PER_REPO_MAX;
      await writer.writeGlobal({
        concurrency: {
          repository_overrides: {
            [repoName]: value,
          },
        },
      } as Partial<IncrediConfig>);
      // Drop any stale pre-#3781 runtime mementos for this repo.
      await clearLegacyMemento("sequential", repoName);
      await clearLegacyMemento("max_concurrent", repoName);
    },
  };
}
