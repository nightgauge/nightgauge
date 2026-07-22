import type { WorkspaceSyncStatus } from "./WorkspaceSyncStatusItem";

/**
 * Persistence/restore rules for the workspace sync indicator state.
 *
 * The indicator has one transient state — "syncing" — shown only while an
 * agent-registration request is in flight. It must NOT be treated as durable:
 * persisting it (or restoring it on activation) produces a spinner that nothing
 * clears, because on the next activation registration may short-circuit (an
 * agent id is already stored) and never run the code that would resolve it to
 * "synced"/"failed". See the activation handler in extension.ts.
 */

/** True when the status is a terminal state worth persisting to globalState. */
export function shouldPersistWorkspaceSyncState(status: WorkspaceSyncStatus): boolean {
  return status !== "syncing";
}

/**
 * True when a persisted status should be re-displayed on activation. "hidden"
 * means no indicator; "syncing" is transient (no sync is actually in progress
 * on a fresh activation) and is never restored so a stale spinner can't stick.
 */
export function shouldRestoreWorkspaceSyncState(status: WorkspaceSyncStatus): boolean {
  return status !== "hidden" && status !== "syncing";
}
