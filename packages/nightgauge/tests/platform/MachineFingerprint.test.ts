/**
 * MachineFingerprint unit tests.
 *
 * Verifies getMachineId(), singleton lifecycle, and reset behaviour.
 *
 * @see Issue #1471 - Implement Machine Fingerprinting via vscode.env.machineId
 */

import { describe, it, expect, beforeEach } from "vitest";
import { vi } from "vitest";

// ---------------------------------------------------------------------------
// VSCode mock — factory must be self-contained (vi.mock is hoisted)
// ---------------------------------------------------------------------------

vi.mock("vscode", () => ({
  env: {
    machineId: "test-machine-uuid-1234",
  },
}));

import * as vscode from "vscode";
import { MachineFingerprint } from "../../src/platform/MachineFingerprint";

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("MachineFingerprint", () => {
  beforeEach(() => {
    MachineFingerprint.resetInstance();
  });

  // ── getMachineId() ────────────────────────────────────────────────────

  describe("getMachineId()", () => {
    it("returns the value from vscode.env.machineId", () => {
      MachineFingerprint.initialize();
      const fp = MachineFingerprint.getInstance()!;

      expect(fp.getMachineId()).toBe("test-machine-uuid-1234");
    });

    it("returns a non-empty string", () => {
      MachineFingerprint.initialize();
      const fp = MachineFingerprint.getInstance()!;

      expect(fp.getMachineId()).toBeTruthy();
      expect(typeof fp.getMachineId()).toBe("string");
    });

    it("returns a stable value across multiple calls (same instance)", () => {
      MachineFingerprint.initialize();
      const fp = MachineFingerprint.getInstance()!;

      expect(fp.getMachineId()).toBe(fp.getMachineId());
    });

    it("reflects the vscode.env.machineId value", () => {
      MachineFingerprint.initialize();
      const fp = MachineFingerprint.getInstance()!;

      expect(fp.getMachineId()).toBe(vscode.env.machineId);
    });
  });

  // ── Singleton lifecycle ────────────────────────────────────────────────

  describe("singleton lifecycle", () => {
    it("returns null before initialization", () => {
      expect(MachineFingerprint.getInstance()).toBeNull();
    });

    it("returns a non-null instance after initialize()", () => {
      MachineFingerprint.initialize();
      expect(MachineFingerprint.getInstance()).not.toBeNull();
    });

    it("initialize() returns same instance on repeat calls", () => {
      const first = MachineFingerprint.initialize();
      const second = MachineFingerprint.initialize();

      expect(first).toBe(second);
    });

    it("getInstance() returns same reference across calls", () => {
      MachineFingerprint.initialize();
      expect(MachineFingerprint.getInstance()).toBe(MachineFingerprint.getInstance());
    });

    it("returns null after resetInstance()", () => {
      MachineFingerprint.initialize();
      expect(MachineFingerprint.getInstance()).not.toBeNull();

      MachineFingerprint.resetInstance();
      expect(MachineFingerprint.getInstance()).toBeNull();
    });

    it("allows re-initialization after reset", () => {
      MachineFingerprint.initialize();
      MachineFingerprint.resetInstance();

      MachineFingerprint.initialize();
      expect(MachineFingerprint.getInstance()).not.toBeNull();
    });

    it("creates a fresh instance after reset", () => {
      const first = MachineFingerprint.initialize();
      MachineFingerprint.resetInstance();
      const second = MachineFingerprint.initialize();

      expect(first).not.toBe(second);
    });
  });
});
