/**
 * TierGate — role-checking unit tests.
 *
 * Tests all role × action combinations for RBAC logic:
 * - checkRole() returns correct allowed/requiredRole
 * - guardRole() does not throw when role is sufficient
 * - guardRole() throws RoleRequiredError with correct fields
 * - Role hierarchy is transitive
 * - ACTION_ROLE_MAP has expected shape
 *
 * @see Issue #1483 - Add RBAC-aware UI element visibility based on user role
 */

import { describe, it, expect } from "vitest";
import {
  TierGate,
  RoleRequiredError,
  ACTION_ROLE_MAP,
  type ActionName,
} from "../../src/platform/TierGate";
import type { TeamRole } from "../../src/platform/types";

describe("TierGate — role checks", () => {
  const gate = new TierGate();
  const roles: TeamRole[] = ["viewer", "developer", "admin", "owner"];

  // ── checkRole() ──────────────────────────────────────────────────────

  describe("checkRole()", () => {
    it("returns allowed:true when role meets required minimum", () => {
      const result = gate.checkRole("manage-team", "admin");
      expect(result.allowed).toBe(true);
      expect(result.requiredRole).toBe("admin");
    });

    it("returns allowed:true when role exceeds required minimum (transitive)", () => {
      const result = gate.checkRole("manage-team", "owner");
      expect(result.allowed).toBe(true);
      expect(result.requiredRole).toBe("admin");
    });

    it("returns allowed:false when role is below required minimum", () => {
      const result = gate.checkRole("manage-team", "viewer");
      expect(result.allowed).toBe(false);
      expect(result.requiredRole).toBe("admin");
    });

    it("returns correct requiredRole for each action", () => {
      for (const [action, requiredRole] of Object.entries(ACTION_ROLE_MAP)) {
        const result = gate.checkRole(action as ActionName, "owner");
        expect(result.requiredRole).toBe(requiredRole);
      }
    });
  });

  // ── manage-team role combinations ────────────────────────────────────

  describe("manage-team role combinations", () => {
    it("viewer → not allowed", () => {
      expect(gate.checkRole("manage-team", "viewer").allowed).toBe(false);
    });

    it("developer → not allowed", () => {
      expect(gate.checkRole("manage-team", "developer").allowed).toBe(false);
    });

    it("admin → allowed", () => {
      expect(gate.checkRole("manage-team", "admin").allowed).toBe(true);
    });

    it("owner → allowed", () => {
      expect(gate.checkRole("manage-team", "owner").allowed).toBe(true);
    });
  });

  // ── manage-billing role combinations ─────────────────────────────────

  describe("manage-billing role combinations", () => {
    it("viewer → not allowed", () => {
      expect(gate.checkRole("manage-billing", "viewer").allowed).toBe(false);
    });

    it("developer → not allowed", () => {
      expect(gate.checkRole("manage-billing", "developer").allowed).toBe(false);
    });

    it("admin → allowed", () => {
      expect(gate.checkRole("manage-billing", "admin").allowed).toBe(true);
    });

    it("owner → allowed", () => {
      expect(gate.checkRole("manage-billing", "owner").allowed).toBe(true);
    });
  });

  // ── view-team role combinations ───────────────────────────────────────

  describe("view-team role combinations", () => {
    it("viewer → allowed (all roles can view)", () => {
      expect(gate.checkRole("view-team", "viewer").allowed).toBe(true);
    });

    it("developer → allowed", () => {
      expect(gate.checkRole("view-team", "developer").allowed).toBe(true);
    });

    it("admin → allowed", () => {
      expect(gate.checkRole("view-team", "admin").allowed).toBe(true);
    });

    it("owner → allowed", () => {
      expect(gate.checkRole("view-team", "owner").allowed).toBe(true);
    });
  });

  // ── view-analytics role combinations ─────────────────────────────────

  describe("view-analytics role combinations", () => {
    it("viewer → not allowed", () => {
      expect(gate.checkRole("view-analytics", "viewer").allowed).toBe(false);
    });

    it("developer → allowed", () => {
      expect(gate.checkRole("view-analytics", "developer").allowed).toBe(true);
    });

    it("admin → allowed", () => {
      expect(gate.checkRole("view-analytics", "admin").allowed).toBe(true);
    });

    it("owner → allowed", () => {
      expect(gate.checkRole("view-analytics", "owner").allowed).toBe(true);
    });
  });

  // ── Role hierarchy transitivity ───────────────────────────────────────

  describe("role hierarchy transitivity", () => {
    it("owner can perform all actions", () => {
      for (const action of Object.keys(ACTION_ROLE_MAP) as ActionName[]) {
        expect(gate.checkRole(action, "owner").allowed).toBe(true);
      }
    });

    it("viewer can only perform viewer-minimum actions", () => {
      // view-team requires viewer — should be allowed
      expect(gate.checkRole("view-team", "viewer").allowed).toBe(true);
      // manage-team requires admin — should NOT be allowed
      expect(gate.checkRole("manage-team", "viewer").allowed).toBe(false);
      // view-analytics requires developer — should NOT be allowed
      expect(gate.checkRole("view-analytics", "viewer").allowed).toBe(false);
    });

    it("all roles can access view-team (minimum role: viewer)", () => {
      for (const role of roles) {
        expect(gate.checkRole("view-team", role).allowed).toBe(true);
      }
    });
  });

  // ── guardRole() ───────────────────────────────────────────────────────

  describe("guardRole()", () => {
    it("does not throw when role is sufficient", () => {
      expect(() => gate.guardRole("manage-team", "admin")).not.toThrow();
    });

    it("throws RoleRequiredError when role is insufficient", () => {
      expect(() => gate.guardRole("manage-team", "viewer")).toThrow(RoleRequiredError);
    });

    it("thrown error has correct action, requiredRole, currentRole fields", () => {
      try {
        gate.guardRole("manage-team", "developer");
        expect.fail("Expected RoleRequiredError to be thrown");
      } catch (err) {
        expect(err).toBeInstanceOf(RoleRequiredError);
        const roleErr = err as RoleRequiredError;
        expect(roleErr.action).toBe("manage-team");
        expect(roleErr.requiredRole).toBe("admin");
        expect(roleErr.currentRole).toBe("developer");
      }
    });

    it('error name is "RoleRequiredError"', () => {
      try {
        gate.guardRole("manage-billing", "viewer");
        expect.fail("Expected RoleRequiredError to be thrown");
      } catch (err) {
        expect((err as Error).name).toBe("RoleRequiredError");
      }
    });

    it("error message contains action and role names", () => {
      try {
        gate.guardRole("manage-team", "viewer");
        expect.fail("Expected RoleRequiredError to be thrown");
      } catch (err) {
        const message = (err as Error).message;
        expect(message).toContain("manage-team");
        expect(message).toContain("admin");
        expect(message).toContain("viewer");
      }
    });

    it("does not throw for any role at or above required", () => {
      for (const role of roles) {
        for (const [action, requiredRole] of Object.entries(ACTION_ROLE_MAP)) {
          const roleIndex = roles.indexOf(role);
          const requiredIndex = roles.indexOf(requiredRole as TeamRole);
          if (roleIndex >= requiredIndex) {
            expect(() => gate.guardRole(action as ActionName, role)).not.toThrow();
          }
        }
      }
    });
  });

  // ── ACTION_ROLE_MAP ────────────────────────────────────────────────────

  describe("ACTION_ROLE_MAP", () => {
    it("has all expected actions", () => {
      expect(ACTION_ROLE_MAP).toHaveProperty("manage-team");
      expect(ACTION_ROLE_MAP).toHaveProperty("manage-billing");
      expect(ACTION_ROLE_MAP).toHaveProperty("view-team");
      expect(ACTION_ROLE_MAP).toHaveProperty("view-analytics");
      expect(ACTION_ROLE_MAP).toHaveProperty("run-pipeline");
    });

    it("maps actions to valid TeamRole values", () => {
      const validRoles: TeamRole[] = ["owner", "admin", "developer", "viewer"];
      for (const role of Object.values(ACTION_ROLE_MAP)) {
        expect(validRoles).toContain(role);
      }
    });
  });
});
