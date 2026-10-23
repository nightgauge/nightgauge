import { describe, it, expect } from "vitest";
import {
  shouldPersistWorkspaceSyncState,
  shouldRestoreWorkspaceSyncState,
} from "../../src/views/workspaceSyncState";

describe("workspaceSyncState", () => {
  describe("shouldPersistWorkspaceSyncState", () => {
    it("persists terminal states", () => {
      expect(shouldPersistWorkspaceSyncState("synced")).toBe(true);
      expect(shouldPersistWorkspaceSyncState("failed")).toBe(true);
      expect(shouldPersistWorkspaceSyncState("hidden")).toBe(true);
    });

    it("never persists the transient 'syncing' state", () => {
      // Persisting "syncing" would restore a stuck spinner on the next reload.
      expect(shouldPersistWorkspaceSyncState("syncing")).toBe(false);
    });
  });

  describe("shouldRestoreWorkspaceSyncState", () => {
    it("restores terminal, visible states", () => {
      expect(shouldRestoreWorkspaceSyncState("synced")).toBe(true);
      expect(shouldRestoreWorkspaceSyncState("failed")).toBe(true);
    });

    it("does not restore 'hidden' (no indicator)", () => {
      expect(shouldRestoreWorkspaceSyncState("hidden")).toBe(false);
    });

    it("does not restore the transient 'syncing' state", () => {
      // Defends against a stale spinner persisted by an older build, and the
      // fact that no sync is actually in progress on a fresh activation.
      expect(shouldRestoreWorkspaceSyncState("syncing")).toBe(false);
    });
  });
});
