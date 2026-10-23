/**
 * One-time migration from `.nightgauge/supercharge.yaml` to the checkout's
 * `performance-mode.yaml` in `.git/nightgauge-worktree/` (Issue #3009,
 * ADR-024 § 7).
 *
 * Mapping (per ADR-003):
 *   - legacy `active: true`  → `mode: maximum`
 *   - legacy `active: false` → `mode: elevated`
 *
 * After migration the legacy file is renamed to `supercharge.yaml.migrated`
 * so subsequent activations short-circuit. First-time users (no legacy
 * file) silently default to `elevated` without a toast.
 *
 * @see Issue #3009 - Replace Supercharge with explicit performance modes
 */
import * as fs from "node:fs";
import * as path from "node:path";
import {
  writePerformanceModeStateFile,
  getLegacySuperchargeStatePath,
  getPerformanceModeStatePath,
} from "./resolvers/monitoringResolver";
import { isUsableWorkspaceRoot } from "./cloneLayout";
import type { PerformanceMode } from "./modeProfiles";

const LEGACY_MIGRATED_FILENAME = "supercharge.yaml.migrated";

export interface MigrationResult {
  migrated: boolean;
  /** The mode written when migrated, undefined when no migration ran */
  mode?: PerformanceMode;
  /** Absolute path of the renamed legacy file, when applicable */
  legacyBackupPath?: string;
  /** Error encountered during migration (non-blocking — read path falls back to default) */
  error?: string;
}

/**
 * Run the one-time migration. Idempotent — safe to call on every activation.
 *
 * Returns `{ migrated: false }` when:
 *   - `workspaceRoot` is not in a git checkout, OR
 *   - the new state file already exists, OR
 *   - the legacy file is absent.
 *
 * Returns `{ migrated: true, mode }` when the legacy file was found and
 * converted. The caller (extension activation) is responsible for
 * surfacing the one-time toast.
 */
export function migrateSuperchargeToPerformanceMode(workspaceRoot: string): MigrationResult {
  // Outside a git checkout there is nowhere to write the new state file.
  if (!isUsableWorkspaceRoot(workspaceRoot)) {
    return { migrated: false };
  }
  try {
    const newStatePath = getPerformanceModeStatePath(workspaceRoot);
    if (fs.existsSync(newStatePath)) {
      return { migrated: false };
    }

    const legacyPath = getLegacySuperchargeStatePath(workspaceRoot);
    if (!fs.existsSync(legacyPath)) {
      return { migrated: false };
    }

    const legacyContent = fs.readFileSync(legacyPath, "utf-8");
    const wasActive = /^active:\s*true\s*$/m.test(legacyContent);
    const mode: PerformanceMode = wasActive ? "maximum" : "elevated";

    writePerformanceModeStateFile(workspaceRoot, mode);

    const backupPath = path.join(workspaceRoot, ".nightgauge", LEGACY_MIGRATED_FILENAME);
    try {
      fs.renameSync(legacyPath, backupPath);
    } catch {
      // Rename failed (rare — read-only fs?). Leave the legacy file in
      // place; the new file existing already prevents re-migration.
    }

    return { migrated: true, mode, legacyBackupPath: backupPath };
  } catch (error) {
    return {
      migrated: false,
      error: error instanceof Error ? error.message : String(error),
    };
  }
}
