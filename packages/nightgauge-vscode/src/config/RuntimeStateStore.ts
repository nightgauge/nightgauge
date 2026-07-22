/**
 * RuntimeStateStore - VSCode memento-backed runtime config tier
 *
 * Stores ephemeral configuration values in VSCode's globalState/workspaceState
 * mementos. Plugged into the config merge engine as a new tier between `local`
 * and `env` (see configMergeEngine.ts).
 *
 * Phase 2 of epic #3313 — introduces the mechanism only. Phase 3+ migrates
 * the actual writers (pipeline.max_concurrent, autonomous repo toggles, etc.).
 *
 * Key namespaces:
 *   nightgauge.runtime.<dotted.path>                       — global scope
 *   nightgauge.runtime.repos.<repoSlug>.<dotted.path>      — workspace scope
 *
 * @see Issue #3335
 * @see docs/SETTINGS_ARCHITECTURE.md
 */

import * as vscode from "vscode";
import type { IncrediConfig } from "./schema";
import { setConfigValue } from "../views/settings/configUtils";

/**
 * Memento key prefix for all runtime state values.
 */
const RUNTIME_PREFIX = "nightgauge.runtime.";

/**
 * Infix that marks a key as repo-scoped under workspaceState.
 * Full key shape: nightgauge.runtime.repos.<repoSlug>.<path>
 */
const REPO_SCOPE_INFIX = "repos.";

/**
 * Memento scope a value lives in.
 */
export type RuntimeScope = "global" | "workspace";

/**
 * Optional parameters for runtime store operations.
 */
export interface RuntimeKeyOptions {
  /**
   * When provided, the key is workspace-scoped under
   * `nightgauge.runtime.repos.<repoSlug>.<path>`.
   * Without it, the key is global-scoped (default) unless `scope` overrides.
   */
  repoSlug?: string;
  /**
   * Memento scope override for non-repo workspace-state keys.
   * - `"workspace"` (with no `repoSlug`): writes to `workspaceState` at
   *   `nightgauge.runtime.<path>` (no `repos.` infix). Used by
   *   `autonomous.enabled_repos` so two open workspaces with overlapping
   *   repo sets do not cross-pollute selections.
   * - `"global"` or omitted (with no `repoSlug`): writes to `globalState`
   *   (Phase 2 default).
   * - When `repoSlug` is provided, this option is ignored (repo-scoped keys
   *   always live in `workspaceState`).
   */
  scope?: RuntimeScope;
}

/**
 * Event emitted when a runtime value changes (set or delete).
 */
export interface RuntimeChangeEvent {
  /** Dotted path without the runtime/repo prefix */
  path: string;
  /** Repo slug for workspace-scoped values; undefined for global */
  repoSlug?: string;
  /** Memento the value lives in */
  scope: RuntimeScope;
  /** Value before the change (undefined for first write) */
  oldValue: unknown;
  /** Value after the change (undefined for delete) */
  newValue: unknown;
}

/**
 * Resolved memento + key for a (path, repoSlug?) pair.
 */
interface ResolvedKey {
  memento: vscode.Memento;
  key: string;
  scope: RuntimeScope;
}

/**
 * Sanitize a repoSlug so it is safe to embed inside a dotted memento key.
 * Slugs typically arrive as "owner/repo"; the slash is replaced so it does
 * not collide with the dotted-path walker used in snapshot reconstruction.
 */
function sanitizeRepoSlug(repoSlug: string): string {
  return repoSlug.replace(/\//g, "__");
}

/**
 * Reverse of `sanitizeRepoSlug`. Used by the snapshot remap to recover the
 * "owner/repo" form from a dotted memento key segment.
 */
function unsanitizeRepoSlug(safeSlug: string): string {
  return safeSlug.replace(/__/g, "/");
}

/**
 * Repo-scoped key shape used inside the runtime tier:
 *   `repos.<safeSlug>.<rest>`  where `<safeSlug>` is `owner__repo` or `repo`.
 *
 * Captures (1) the safeSlug and (2) the remainder so callers can decide
 * whether to remap the key into a concrete schema location. Tested in
 * `RuntimeStateStoreScope.test.ts`.
 */
const REPO_SCOPED_PATH_PATTERN = /^repos\.([^.]+)\.(.+)$/;

/**
 * Per-repo runtime keys that the merge engine should surface at their
 * concrete schema location instead of the generic `repos.<slug>.*` overlay.
 *
 * `RepositoriesTreeProvider`, `resolveRepoConcurrencyCap`, and other consumers
 * read these from `autonomous.repositories.<slug>.<key>` via the merged
 * config — keep this set in sync with that schema. Any per-repo key not
 * listed here remains under `repos.<slug>.*` until a later phase routes it.
 */
const REMAPPED_REPO_KEYS = new Set(["sequential", "max_concurrent"]);

/**
 * Translate the stripped form of a memento key into the path the snapshot
 * should populate. Non-repo-scoped paths and unsupported per-repo keys are
 * returned unchanged.
 */
function remapRepoScopedKey(strippedKey: string): string {
  const match = REPO_SCOPED_PATH_PATTERN.exec(strippedKey);
  if (!match) {
    return strippedKey;
  }
  const [, safeSlug, rest] = match;
  if (!REMAPPED_REPO_KEYS.has(rest)) {
    return strippedKey;
  }
  const slug = unsanitizeRepoSlug(safeSlug);
  return `autonomous.repositories.${slug}.${rest}`;
}

/**
 * RuntimeStateStore — get/set/delete runtime config values backed by mementos.
 *
 * @example
 * ```ts
 * const store = new RuntimeStateStore(context.globalState, context.workspaceState);
 * await store.set("pipeline.max_concurrent", 4);
 * store.get("pipeline.max_concurrent"); // => 4
 *
 * // Repo-scoped (lives in workspaceState):
 * await store.set("autonomous.enabled", true, { repoSlug: "nightgauge/nightgauge" });
 *
 * // Snapshot for the merge engine:
 * const tiers: ConfigTiers = { ..., runtime: store.snapshot() };
 * ```
 */
export class RuntimeStateStore implements vscode.Disposable {
  private readonly _onDidChange = new vscode.EventEmitter<RuntimeChangeEvent>();
  readonly onDidChange = this._onDidChange.event;

  constructor(
    private readonly globalState: vscode.Memento,
    private readonly workspaceState: vscode.Memento
  ) {}

  /**
   * Read a runtime value. Returns undefined when unset.
   */
  get(path: string, opts?: RuntimeKeyOptions): unknown {
    const { memento, key } = this.resolveKey(path, opts?.repoSlug, opts?.scope);
    return memento.get(key);
  }

  /**
   * Write a runtime value. Fires onDidChange with the old and new values.
   */
  async set(path: string, value: unknown, opts?: RuntimeKeyOptions): Promise<void> {
    const { memento, key, scope } = this.resolveKey(path, opts?.repoSlug, opts?.scope);
    const oldValue = memento.get(key);
    await memento.update(key, value);
    this._onDidChange.fire({
      path,
      repoSlug: opts?.repoSlug,
      scope,
      oldValue,
      newValue: value,
    });
  }

  /**
   * Delete a runtime value so the merge engine falls back to lower tiers.
   * Fires onDidChange with the prior value and `newValue: undefined`.
   */
  async delete(path: string, opts?: RuntimeKeyOptions): Promise<void> {
    const { memento, key, scope } = this.resolveKey(path, opts?.repoSlug, opts?.scope);
    const oldValue = memento.get(key);
    await memento.update(key, undefined);
    this._onDidChange.fire({
      path,
      repoSlug: opts?.repoSlug,
      scope,
      oldValue,
      newValue: undefined,
    });
  }

  /**
   * Reconstruct a nested Partial<IncrediConfig> snapshot for merge-engine
   * consumption.
   *
   * - Global keys (`nightgauge.runtime.<path>`) become `<path>` directly.
   * - Workspace-scoped non-repo keys (`nightgauge.runtime.<path>` in
   *   workspaceState) become `<path>`.
   * - Repo-scoped keys (`nightgauge.runtime.repos.<slug>.<rest>`):
   *   - When `<rest>` matches `sequential` or `max_concurrent`, the key is
   *     remapped to `autonomous.repositories.<slug>.<rest>` so the merge
   *     engine surfaces the value at the schema location consumers already
   *     read (Phase 3 of #3313 — see ADR-001 in the #3336 knowledge base).
   *     The slug is reverse-sanitized (`__` → `/`).
   *   - All other repo-scoped keys remain at `repos.<slug>.<rest>` until
   *     later phases route them into concrete schema fields.
   *
   * Workspace-scoped values win over global-scoped values when both set the
   * same path — workspaceState is read after globalState.
   */
  snapshot(): Partial<IncrediConfig> {
    const result: Record<string, unknown> = {};

    for (const memento of [this.globalState, this.workspaceState]) {
      for (const fullKey of memento.keys()) {
        if (!fullKey.startsWith(RUNTIME_PREFIX)) {
          continue;
        }
        const value = memento.get(fullKey);
        if (value === undefined) {
          continue;
        }
        const stripped = fullKey.slice(RUNTIME_PREFIX.length);
        const targetPath = remapRepoScopedKey(stripped);
        // Note: setConfigValue is typed for IncrediConfig but operates on a
        // plain Record — passing `result` (cast to IncrediConfig) is safe.
        setConfigValue(result as IncrediConfig, targetPath, value);
      }
    }

    return result as Partial<IncrediConfig>;
  }

  /**
   * Dispose the change-event emitter. Mementos themselves are owned by the
   * ExtensionContext and are not disposed here.
   */
  dispose(): void {
    this._onDidChange.dispose();
  }

  /**
   * Build the memento + full key for a (path, repoSlug?, scope?) tuple.
   *
   * Precedence:
   *   1. `repoSlug` set → workspaceState, key shape `repos.<safeSlug>.<path>`.
   *      `scope` is ignored (repo-scoped keys are always per-workspace).
   *   2. `scope === "workspace"` (no `repoSlug`) → workspaceState, key shape
   *      `<path>` (no `repos.` infix). Used for workspace-state values that
   *      apply to the whole workspace, e.g. `autonomous.enabled_repos`.
   *   3. Otherwise (default) → globalState, key shape `<path>` (Phase 2 default).
   */
  private resolveKey(path: string, repoSlug?: string, scope?: RuntimeScope): ResolvedKey {
    if (repoSlug) {
      const safeSlug = sanitizeRepoSlug(repoSlug);
      return {
        memento: this.workspaceState,
        key: `${RUNTIME_PREFIX}${REPO_SCOPE_INFIX}${safeSlug}.${path}`,
        scope: "workspace",
      };
    }
    if (scope === "workspace") {
      return {
        memento: this.workspaceState,
        key: `${RUNTIME_PREFIX}${path}`,
        scope: "workspace",
      };
    }
    return {
      memento: this.globalState,
      key: `${RUNTIME_PREFIX}${path}`,
      scope: "global",
    };
  }
}

// Export prefix constants so tests and future callers can reference them
// without re-declaring magic strings.
export const RUNTIME_KEY_PREFIX = RUNTIME_PREFIX;
export const RUNTIME_REPO_SCOPE_INFIX = REPO_SCOPE_INFIX;
