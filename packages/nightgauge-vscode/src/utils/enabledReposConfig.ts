/**
 * `autonomous.enabled_repos` machine-tier writer + pure helpers.
 *
 * Issue #3641 reclassified `autonomous.enabled_repos` from Runtime
 * workspaceState to **Machine tier** (`~/.nightgauge/config.yaml`) —
 * workspaceState was keyed by workspace folder URI, so every spawned git
 * worktree was a distinct bucket and the developer's autonomy policy never
 * propagated across them. Machine tier is the only tier the Go binary reads
 * directly from inside a worktree.
 *
 * This module now writes to machine YAML via `IncrediYamlService.writeGlobal()`
 * and best-effort clears the legacy runtime-memento entry so it can't shadow
 * the new machine value through the merge engine's runtime tier.
 *
 * Reads still come through `ConfigBridge.getEffectiveConfig()` so the
 * 7-tier merge produces the effective list.
 *
 * Empty list semantics: `enabled_repos: []` in machine YAML and "key absent"
 * both mean "scan all configured repos". The UI uses `[]` to express
 * "reset to scan-all" (e.g. when every workspace repo is checked or when
 * the user clears the entire selection).
 *
 * @see internal/config/config.go — ResolvedEnabledRepos
 * @see internal/config/merge.go — LoadMerged
 * @see docs/SETTINGS_ARCHITECTURE.md — "Tier 2: Machine"
 */

import type { ConfigBridge } from "../services/ConfigBridge";
import type { RuntimeStateStore } from "../config/RuntimeStateStore";
import type { IncrediYamlService } from "../views/settings/IncrediYamlService";
import type { IncrediConfig } from "../config/schema";

/** Path used for the runtime-tier cleanup — also the schema location. */
const ENABLED_REPOS_PATH = "autonomous.enabled_repos";

/**
 * Subset of `RuntimeStateStore` this module depends on. Declaring the
 * surface here lets tests pass a stub without instantiating a real store.
 */
export interface RuntimeStateStoreLike {
  delete(path: string, opts?: { repoSlug?: string; scope?: "workspace" | "global" }): Promise<void>;
}

/**
 * Subset of `ConfigBridge` this module reads through.
 */
export interface ConfigBridgeLike {
  getEffectiveConfig(): {
    config?: {
      autonomous?: {
        enabled_repos?: unknown;
      };
    };
  } | null;
}

/**
 * Subset of `IncrediYamlService` this module writes through. Only the
 * machine-tier writer is needed.
 */
export interface MachineYamlWriterLike {
  writeGlobal(partial: Partial<IncrediConfig>): Promise<{ success: boolean; error?: string }>;
}

/**
 * Methods exposed by the enabled-repos service. Mirrors the function names
 * of the pre-Phase-3 module so call sites change minimally.
 */
export interface EnabledReposConfigService {
  readEnabledRepos(): string[];
  writeEnabledRepos(selected: string[]): Promise<void>;
}

/**
 * Factory: capture a `RuntimeStateStore` for legacy-memento cleanup, a
 * `ConfigBridge` for merged reads, and an `IncrediYamlService` (or stub)
 * for machine-tier writes.
 */
export function createEnabledReposConfigService(
  runtimeStore: RuntimeStateStoreLike | RuntimeStateStore,
  configBridge: ConfigBridgeLike | ConfigBridge,
  machineYamlWriter: MachineYamlWriterLike | IncrediYamlService
): EnabledReposConfigService {
  const store = runtimeStore as RuntimeStateStoreLike;
  const bridge = configBridge as ConfigBridgeLike;
  const writer = machineYamlWriter as MachineYamlWriterLike;

  return {
    readEnabledRepos(): string[] {
      const list = bridge.getEffectiveConfig()?.config?.autonomous?.enabled_repos;
      if (!Array.isArray(list)) return [];
      return list.filter((x): x is string => typeof x === "string" && x.trim().length > 0);
    },

    async writeEnabledRepos(selected: string[]): Promise<void> {
      // Write to the machine-tier YAML — authoritative source per #3641.
      // Empty array is a literal "scan all" sentinel, same as the key
      // being absent (the autonomous scheduler treats both equivalently).
      await writer.writeGlobal({
        autonomous: { enabled_repos: selected },
      } as Partial<IncrediConfig>);
      // Best-effort clear of any legacy runtime-memento entries so they
      // can't shadow the new machine value through the merge engine's
      // runtime tier. Pre-#3641 the UI wrote here, and stale entries may
      // still exist on developer machines because the v2 legacy-keys
      // migration is gated by a *globalState* STATE_KEY — it runs only
      // once per VSCode install, not once per workspace. Multi-worktree
      // setups (nightgauge + acme-platform +
      // acme-mobile + acme-dashboard) skip the
      // migration in every workspace except the first one VSCode opened,
      // leaving the other workspaces' `workspaceState` mementos populated
      // with a pre-migration list. When that list omits a repo (e.g.
      // Flutter), the runtime tier overlays the new machine YAML's value
      // every time `ConfigBridge.reload()` runs — the visible symptom is
      // a single-repo checkbox that "fires the activity indicators then
      // unchecks itself" because the post-write re-read produces the
      // pre-write list. Issue #3650 (Part C).
      //
      // We clear BOTH scopes (workspace + global) because legacy code
      // paths historically wrote at either scope depending on the version
      // installed when the workspace was first opened. Each call is
      // idempotent — deleting an absent key is a no-op.
      try {
        await store.delete(ENABLED_REPOS_PATH, { scope: "workspace" });
      } catch {
        // Memento cleanup is best-effort; a failure here doesn't affect
        // the authoritative machine-tier write that already succeeded.
      }
      try {
        await store.delete(ENABLED_REPOS_PATH, { scope: "global" });
      } catch {
        // Same best-effort semantics as the workspace-scope cleanup.
      }
    },
  };
}

/**
 * Compare an entry from `enabled_repos` (short or fully-qualified) against a
 * repo's short name. Matching is case-insensitive; short names compare by
 * their exact value, fully-qualified names compare by the portion after "/".
 *
 * Pure helper — no DI, safe to import directly.
 *
 * Example: `entryMatchesRepo("nightgauge/platform", "platform")` → true.
 */
export function entryMatchesRepo(entry: string, repoShortName: string): boolean {
  const lowerShort = repoShortName.toLowerCase();
  const lowerEntry = entry.trim().toLowerCase();
  if (!lowerEntry) return false;
  if (lowerEntry === lowerShort) return true;
  const slash = lowerEntry.indexOf("/");
  if (slash >= 0 && lowerEntry.slice(slash + 1) === lowerShort) return true;
  return false;
}

/**
 * Returns true when `enabled_repos` is unset/empty (meaning "scan all") or
 * when the given repo is explicitly listed. Pure helper.
 */
export function isRepoEnabledForAutonomous(repoShortName: string, enabledRepos: string[]): boolean {
  if (enabledRepos.length === 0) return true;
  return enabledRepos.some((e) => entryMatchesRepo(e, repoShortName));
}
