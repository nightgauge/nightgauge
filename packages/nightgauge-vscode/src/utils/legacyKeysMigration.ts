/**
 * One-time migration that detects legacy project-tier keys in
 * `.nightgauge/config.yaml` and copies them to their correct new tiers
 * (machine YAML or runtime memento).
 *
 * Triggered on extension activation. Runs at most once per global context
 * per version (the timestamp is recorded under
 * `globalState["nightgauge.legacyKeysMigrationCompletedAt.v<N>"]`).
 * Safe to invoke unconditionally — fast-paths out when already run.
 *
 * ## v2 (Issue #3641)
 *
 * Reclassifies autonomous policy keys from Runtime workspaceState to Machine
 * tier. Workspace-folder-scoped memento was the wrong tier for these settings:
 * every git worktree spawned by the pipeline is a distinct workspace folder
 * URI, so the user's policy would only apply in the parent working tree. The
 * Go binary running inside a worktree reads `~/.nightgauge/config.yaml`
 * — the machine tier is the only tier that propagates correctly across
 * worktrees.
 *
 * The v2 STATE_KEY/DISMISSED_KEY are bumped so users who completed v1 get a
 * second migration pass that:
 *   1. Pulls v1 values out of the runtime memento (if present) into the
 *      machine YAML.
 *   2. Pulls remaining v2 keys out of the project YAML (the v1 migration
 *      might have left them if the user dismissed the cleanup notification).
 *   3. Clears the v1 memento entries to remove dead state.
 *
 * @see Issue #3338 (Phase 5 v1) — original migration
 * @see Issue #3641 — v2 reclassification
 * @see docs/SETTINGS_ARCHITECTURE.md — Migrations sub-table
 */
import * as vscode from "vscode";
import { IncrediYamlService } from "../views/settings/IncrediYamlService";
import type { RuntimeStateStore } from "../config/RuntimeStateStore";
import type { Logger } from "./logger";

// Legacy v1 keys (kept for documentation / forensics).
const STATE_KEY_V1 = "nightgauge.legacyKeysMigrationCompletedAt";
const DISMISSED_KEY_V1 = "nightgauge.legacyKeysMigrationDismissed";

// Active v2 keys.
const STATE_KEY = "nightgauge.legacyKeysMigrationCompletedAt.v2";
const DISMISSED_KEY = "nightgauge.legacyKeysMigrationDismissed.v2";

// Runtime memento key prefix used by RuntimeStateStore. Mirrored here so
// the v1 cleanup pass can read raw memento values directly without going
// through the store abstraction (which would surface them at their
// reconstructed schema location rather than the raw path).
const RUNTIME_PREFIX = "nightgauge.runtime.";

/**
 * Describes a single legacy key that has been reclassified to a new tier.
 */
type LegacyKeyDescriptor =
  | {
      key: string;
      targetTier: "runtime";
      runtimePath: string;
      scope: "global" | "workspace";
      /** Optional v1 runtime path (when v2 reclassified from runtime → machine). */
      v1RuntimePath?: string;
      v1RuntimeScope?: "global" | "workspace";
    }
  | {
      key: string;
      targetTier: "machine";
      machineKeyPath: string;
      /** If set, the v1 migration placed this key at this runtime path —
       *  v2 pulls it out and into machine YAML. */
      v1RuntimePath?: string;
      v1RuntimeScope?: "global" | "workspace";
    };

/**
 * Canonical list of keys that have moved out of the project YAML tier.
 * This is the single source of truth consumed by both migration logic and tests.
 *
 * `autonomous.repositories.<repo>.sequential` and `.max_concurrent` are
 * handled separately via `migrateRepoScopedKeys()` due to their dynamic
 * per-repo structure.
 *
 * Tier classifications match docs/SETTINGS_ARCHITECTURE.md as updated by
 * Issue #3641.
 */
export const LEGACY_KEYS: LegacyKeyDescriptor[] = [
  { key: "github_user", targetTier: "machine", machineKeyPath: "github_user" },
  {
    key: "pipeline.max_concurrent",
    targetTier: "runtime",
    runtimePath: "pipeline.max_concurrent",
    scope: "global",
  },
  {
    // v2: autonomous.enabled_repos moves from Runtime workspaceState to Machine.
    // v1 placed it at the runtime path below — v2 picks it up from there too.
    key: "autonomous.enabled_repos",
    targetTier: "machine",
    machineKeyPath: "autonomous.enabled_repos",
    v1RuntimePath: "autonomous.enabled_repos",
    v1RuntimeScope: "workspace",
  },
  {
    key: "notifications.discord.enabled",
    targetTier: "machine",
    machineKeyPath: "notifications.discord.enabled",
  },
  { key: "lm_studio", targetTier: "machine", machineKeyPath: "lm_studio" },
];

/**
 * Read a value at a dotted path from a nested object. Returns `undefined`
 * if any segment is absent.
 */
function getAtPath(obj: Record<string, unknown>, dottedPath: string): unknown {
  const parts = dottedPath.split(".");
  let cur: unknown = obj;
  for (const part of parts) {
    if (cur === null || typeof cur !== "object") return undefined;
    cur = (cur as Record<string, unknown>)[part];
  }
  return cur;
}

/**
 * Check if a value at the given path is defined (not undefined).
 */
function hasAtPath(obj: Record<string, unknown>, dottedPath: string): boolean {
  return getAtPath(obj, dottedPath) !== undefined;
}

/**
 * Build a nested object for a dotted path with a leaf value. Used to
 * assemble the partial Partial<IncrediConfig> argument to writeGlobal().
 */
function nestForPath(dottedPath: string, value: unknown): Record<string, unknown> {
  const parts = dottedPath.split(".");
  const root: Record<string, unknown> = {};
  let cur = root;
  for (let i = 0; i < parts.length - 1; i++) {
    const child: Record<string, unknown> = {};
    cur[parts[i]] = child;
    cur = child;
  }
  cur[parts[parts.length - 1]] = value;
  return root;
}

/**
 * Sanitize a repo slug the same way RuntimeStateStore does, so v1 memento
 * keys can be looked up here without re-importing the helper (which is
 * internal to that module).
 */
function sanitizeRepoSlug(slug: string): string {
  return slug.replace(/\//g, "__");
}

/**
 * Migrate per-repo keys (`autonomous.repositories.<repo>.sequential` and
 * `.max_concurrent`) from the project YAML and from any v1 runtime memento
 * entries into the machine YAML.
 *
 * v1 placed these under workspaceState `nightgauge.runtime.repos.<slug>.<field>`.
 * v2 places them under machine YAML at `autonomous.repositories.<slug>.<field>`.
 *
 * Returns the list of repo-scoped keys that were migrated.
 */
async function migrateRepoScopedKeys(
  projectCfg: Record<string, unknown>,
  context: vscode.ExtensionContext,
  yaml: IncrediYamlService
): Promise<string[]> {
  const migrated: string[] = [];
  const accumulated: Record<string, Record<string, unknown>> = {};

  const recordRepoField = (slug: string, field: string, value: unknown) => {
    if (!accumulated[slug]) accumulated[slug] = {};
    accumulated[slug][field] = value;
    migrated.push(`autonomous.repositories.${slug}.${field}`);
  };

  // Project YAML — same shape v1 expected.
  const autonomous = projectCfg.autonomous as
    { repositories?: Record<string, unknown> } | undefined;
  if (autonomous?.repositories) {
    for (const [slug, repoCfg] of Object.entries(autonomous.repositories)) {
      if (repoCfg === null || typeof repoCfg !== "object") continue;
      const repoCfgObj = repoCfg as Record<string, unknown>;
      if (repoCfgObj.sequential !== undefined) {
        recordRepoField(slug, "sequential", repoCfgObj.sequential);
      }
      if (repoCfgObj.max_concurrent !== undefined) {
        recordRepoField(slug, "max_concurrent", repoCfgObj.max_concurrent);
      }
    }
  }

  // v1 workspace-state memento entries: `nightgauge.runtime.repos.<sanitizedSlug>.<field>`.
  const workspaceState = context.workspaceState;
  if (workspaceState) {
    for (const memKey of workspaceState.keys()) {
      const reposPrefix = `${RUNTIME_PREFIX}repos.`;
      if (!memKey.startsWith(reposPrefix)) continue;
      const rest = memKey.slice(reposPrefix.length); // "<sanitizedSlug>.<field>"
      const dotIdx = rest.lastIndexOf(".");
      if (dotIdx <= 0) continue;
      const sanitized = rest.slice(0, dotIdx);
      const field = rest.slice(dotIdx + 1);
      if (field !== "sequential" && field !== "max_concurrent") continue;
      const value = workspaceState.get(memKey);
      if (value === undefined) continue;
      const slug = sanitized.replace(/__/g, "/");
      // Only record if the project YAML didn't already win for this (slug,field).
      const already = accumulated[slug]?.[field] !== undefined;
      if (!already) {
        recordRepoField(slug, field, value);
      }
      // Either way, clear the v1 memento entry so it doesn't continue
      // to confuse the merge engine.
      await workspaceState.update(memKey, undefined);
    }
  }

  if (Object.keys(accumulated).length === 0) {
    return migrated;
  }

  await yaml.writeGlobal({
    autonomous: { repositories: accumulated },
  } as Parameters<typeof yaml.writeGlobal>[0]);

  return migrated;
}

/**
 * Pull v1 runtime memento values for keys whose v2 target is "machine"
 * into the machine YAML. v1 memento values are cleared after copying.
 *
 * Top-level keys only — repo-scoped keys are handled by
 * migrateRepoScopedKeys().
 */
async function promoteV1MementoToMachine(
  context: vscode.ExtensionContext,
  yaml: IncrediYamlService
): Promise<string[]> {
  const migrated: string[] = [];
  for (const descriptor of LEGACY_KEYS) {
    if (descriptor.targetTier !== "machine") continue;
    if (!descriptor.v1RuntimePath) continue;
    const memento =
      descriptor.v1RuntimeScope === "workspace" ? context.workspaceState : context.globalState;
    const fullKey = `${RUNTIME_PREFIX}${descriptor.v1RuntimePath}`;
    const value = memento.get(fullKey);
    if (value === undefined) continue;
    await yaml.writeGlobal(
      nestForPath(descriptor.machineKeyPath, value) as Parameters<typeof yaml.writeGlobal>[0]
    );
    migrated.push(descriptor.key);
    // Clear the v1 memento entry — stale state shadows the new machine value
    // through the merge engine's runtime tier.
    await memento.update(fullKey, undefined);
  }
  return migrated;
}

/**
 * Run the one-time legacy-keys migration for a single workspace root.
 *
 * For multi-repo workspaces the caller iterates over workspace folders and
 * calls this function once per `workspaceRoot`. Each call is independent.
 *
 * @param context - Extension context for globalState idempotency key
 * @param workspaceRoot - Absolute path to the workspace root
 * @param runtimeStore - Initialized RuntimeStateStore instance
 * @param logger - Extension logger
 * @param forceRun - When true, skip the idempotency STATE_KEY check (palette command)
 */
export async function runLegacyKeysMigration(
  context: vscode.ExtensionContext,
  workspaceRoot: string,
  runtimeStore: RuntimeStateStore,
  logger: Logger,
  forceRun = false
): Promise<void> {
  if (!forceRun && context.globalState.get<string>(STATE_KEY)) {
    return;
  }

  const yaml = new IncrediYamlService(workspaceRoot);
  try {
    const projectRead = await yaml.read();
    const projectCfg = (projectRead.success ? projectRead.config : null) ?? {};
    const projectCfgRecord = projectCfg as Record<string, unknown>;

    const migratedKeys: string[] = [];

    // 1) v1 → v2 promotion for top-level machine-target keys (e.g.
    //    autonomous.enabled_repos previously placed in workspaceState).
    const promoted = await promoteV1MementoToMachine(context, yaml);
    migratedKeys.push(...promoted);

    // 2) Project YAML → new target tier. Always run, even when v1
    //    promotion already wrote a value from memento — project YAML is
    //    authoritative (it's what the developer maintains; memento was
    //    a v1 intermediate). writeGlobal deep-merges, so the project
    //    value overwrites the memento value in machine YAML.
    for (const descriptor of LEGACY_KEYS) {
      if (!hasAtPath(projectCfgRecord, descriptor.key)) continue;

      const value = getAtPath(projectCfgRecord, descriptor.key);

      if (descriptor.targetTier === "runtime") {
        await runtimeStore.set(descriptor.runtimePath, value, {
          scope: descriptor.scope,
        });
      } else {
        // machine tier — write to ~/.nightgauge/config.yaml
        await yaml.writeGlobal(
          nestForPath(descriptor.machineKeyPath, value) as Parameters<typeof yaml.writeGlobal>[0]
        );
      }
      if (!migratedKeys.includes(descriptor.key)) {
        migratedKeys.push(descriptor.key);
      }
    }

    // 3) Per-repo keys (project YAML + v1 memento).
    const repoMigrated = await migrateRepoScopedKeys(projectCfgRecord, context, yaml);
    migratedKeys.push(...repoMigrated);

    // Always mark v2 complete (idempotency).
    await context.globalState.update(STATE_KEY, new Date().toISOString());

    if (migratedKeys.length === 0) {
      return;
    }

    logger.info("[legacy-keys-migration] v2 migrated keys", { keys: migratedKeys });

    // Non-blocking notification — only show if user hasn't permanently dismissed v2.
    if (!forceRun && context.globalState.get<boolean>(DISMISSED_KEY)) {
      return;
    }

    const N = migratedKeys.length;
    const action = await vscode.window.showInformationMessage(
      `Nightgauge: Migrated ${N} setting${N === 1 ? "" : "s"} to your per-developer machine config (~/.nightgauge/config.yaml). ` +
        `Remove ${N === 1 ? "it" : "them"} from .nightgauge/config.yaml so your machine setting applies across worktrees.`,
      "Review Settings",
      "Dismiss"
    );

    if (action === "Dismiss") {
      await context.globalState.update(DISMISSED_KEY, true);
    } else if (action === "Review Settings") {
      await vscode.commands.executeCommand("nightgauge.showSettings");
    }
  } catch (err) {
    // Best-effort — never block activation.
    logger.warn("[legacy-keys-migration] failed", {
      error: err instanceof Error ? err.message : String(err),
    });
  } finally {
    yaml.dispose();
  }
}

// Re-export v1 key constants for tests that need to assert on prior state.
export const _internal = { STATE_KEY_V1, DISMISSED_KEY_V1, STATE_KEY, DISMISSED_KEY };
