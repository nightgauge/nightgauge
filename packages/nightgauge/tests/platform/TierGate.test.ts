/**
 * TierGate unit tests.
 *
 * Tests all tier/feature combinations for gating logic:
 * - check() returns correct allowed/requiredTier/upgradeUrl
 * - guard() does not throw when allowed
 * - guard() throws TierRequiredError with correct fields when not allowed
 * - Tier hierarchy is transitive
 * - Local features are free (community floor); cloud features stay gated
 *
 * @see Issue #1472 - Add tier-aware feature gating throughout extension UI
 * @see feat/free-local-cloud-off - Local product is free; cloud not offered yet
 */

import { describe, it, expect } from "vitest";
import {
  TierGate,
  TierRequiredError,
  FEATURE_TIER_MAP,
  type FeatureName,
} from "../../src/platform/TierGate";
import type { Tier } from "../../src/platform/types";

/**
 * Genuinely-local features that are free for everyone (community floor). These
 * run entirely on the user's machine and require no account. See
 * FEATURE_TIER_MAP's doc comment for the local-vs-cloud split.
 */
const LOCAL_FREE_FEATURES: FeatureName[] = [
  "batch-processing",
  "concurrent-pipelines",
  "ciKeys",
  "customSkills",
];

/** Cloud features that still require a higher tier (and are command-hidden). */
const CLOUD_GATED_FEATURES: FeatureName[] = [
  "team-dashboard",
  "advanced-analytics",
  "sso",
  "auditLogs",
  "mobile",
  "web",
];

describe("TierGate", () => {
  const gate = new TierGate();
  const tiers: Tier[] = ["community", "pro", "team", "enterprise"];

  // ── check() ──────────────────────────────────────────────────────────

  describe("check()", () => {
    it("returns allowed: true when currentTier meets requiredTier", () => {
      const result = gate.check("advanced-analytics", "pro");
      expect(result.allowed).toBe(true);
      expect(result.requiredTier).toBe("pro");
      expect(result.upgradeUrl).toBe("https://nightgauge.dev/pricing");
    });

    it("returns allowed: true when currentTier exceeds requiredTier", () => {
      const result = gate.check("advanced-analytics", "enterprise");
      expect(result.allowed).toBe(true);
      expect(result.requiredTier).toBe("pro");
    });

    it("returns allowed: false when currentTier is below requiredTier", () => {
      const result = gate.check("advanced-analytics", "community");
      expect(result.allowed).toBe(false);
      expect(result.requiredTier).toBe("pro");
      expect(result.upgradeUrl).toBe("https://nightgauge.dev/pricing");
    });

    it("returns correct requiredTier for each feature", () => {
      for (const [feature, requiredTier] of Object.entries(FEATURE_TIER_MAP)) {
        const result = gate.check(feature as FeatureName, "community");
        expect(result.requiredTier).toBe(requiredTier);
      }
    });
  });

  // ── Local free features (community floor) ────────────────────────────
  // Product decision: the local product is free. batch-processing,
  // concurrent-pipelines, ciKeys, and customSkills all run locally, so they
  // are allowed for EVERY tier including community/unauthenticated.

  describe("local free features (community floor)", () => {
    it("all local features map to the community tier", () => {
      for (const feature of LOCAL_FREE_FEATURES) {
        expect(FEATURE_TIER_MAP[feature]).toBe("community");
      }
    });

    it("community tier can access every local feature", () => {
      for (const feature of LOCAL_FREE_FEATURES) {
        expect(gate.check(feature, "community").allowed).toBe(true);
      }
    });

    it("every tier can access every local feature", () => {
      for (const tier of tiers) {
        for (const feature of LOCAL_FREE_FEATURES) {
          expect(gate.check(feature, tier).allowed).toBe(true);
        }
      }
    });

    it("guard() never throws for a local feature (no upgrade wall)", () => {
      for (const tier of tiers) {
        for (const feature of LOCAL_FREE_FEATURES) {
          expect(() => gate.guard(feature, tier)).not.toThrow();
        }
      }
    });
  });

  // ── team-dashboard tier combinations ─────────────────────────────────

  describe("team-dashboard tier combinations", () => {
    it("community → not allowed", () => {
      expect(gate.check("team-dashboard", "community").allowed).toBe(false);
    });

    it("pro → not allowed", () => {
      expect(gate.check("team-dashboard", "pro").allowed).toBe(false);
    });

    it("team → allowed", () => {
      expect(gate.check("team-dashboard", "team").allowed).toBe(true);
    });

    it("enterprise → allowed", () => {
      expect(gate.check("team-dashboard", "enterprise").allowed).toBe(true);
    });
  });

  // ── sso tier combinations ────────────────────────────────────────────

  describe("sso tier combinations", () => {
    it("community → not allowed", () => {
      expect(gate.check("sso", "community").allowed).toBe(false);
    });

    it("pro → not allowed", () => {
      expect(gate.check("sso", "pro").allowed).toBe(false);
    });

    it("team → not allowed", () => {
      expect(gate.check("sso", "team").allowed).toBe(false);
    });

    it("enterprise → allowed", () => {
      expect(gate.check("sso", "enterprise").allowed).toBe(true);
    });
  });

  // ── Tier hierarchy transitivity ──────────────────────────────────────

  describe("tier hierarchy transitivity", () => {
    it("higher tiers can access all lower-tier features", () => {
      // Enterprise can access all features
      for (const feature of Object.keys(FEATURE_TIER_MAP) as FeatureName[]) {
        expect(gate.check(feature, "enterprise").allowed).toBe(true);
      }
    });

    it("team can access community + pro features", () => {
      expect(gate.check("batch-processing", "team").allowed).toBe(true);
      expect(gate.check("concurrent-pipelines", "team").allowed).toBe(true);
      expect(gate.check("advanced-analytics", "team").allowed).toBe(true);
      expect(gate.check("mobile", "team").allowed).toBe(true);
    });

    it("community cannot access any cloud-gated feature", () => {
      for (const feature of CLOUD_GATED_FEATURES) {
        expect(gate.check(feature, "community").allowed).toBe(false);
      }
    });
  });

  // ── guard() ──────────────────────────────────────────────────────────

  describe("guard()", () => {
    it("does not throw when feature is allowed", () => {
      expect(() => gate.guard("advanced-analytics", "pro")).not.toThrow();
    });

    it("throws TierRequiredError when feature is not allowed", () => {
      expect(() => gate.guard("advanced-analytics", "community")).toThrow(TierRequiredError);
    });

    it("thrown error has correct fields", () => {
      try {
        gate.guard("advanced-analytics", "community");
        expect.fail("Expected TierRequiredError to be thrown");
      } catch (err) {
        expect(err).toBeInstanceOf(TierRequiredError);
        const tierErr = err as TierRequiredError;
        expect(tierErr.feature).toBe("advanced-analytics");
        expect(tierErr.requiredTier).toBe("pro");
        expect(tierErr.currentTier).toBe("community");
        expect(tierErr.upgradeUrl).toBe("https://nightgauge.dev/pricing");
        expect(tierErr.name).toBe("TierRequiredError");
        expect(tierErr.message).toContain("advanced-analytics");
        expect(tierErr.message).toContain("pro");
        expect(tierErr.message).toContain("community");
      }
    });

    it("does not throw for any tier at or above required", () => {
      for (const tier of tiers) {
        for (const [feature, requiredTier] of Object.entries(FEATURE_TIER_MAP)) {
          const tierIndex = tiers.indexOf(tier);
          const requiredIndex = tiers.indexOf(requiredTier);
          if (tierIndex >= requiredIndex) {
            expect(() => gate.guard(feature as FeatureName, tier)).not.toThrow();
          }
        }
      }
    });
  });

  // ── FEATURE_TIER_MAP ─────────────────────────────────────────────────

  describe("FEATURE_TIER_MAP", () => {
    it("has all expected features", () => {
      expect(FEATURE_TIER_MAP).toHaveProperty("batch-processing");
      expect(FEATURE_TIER_MAP).toHaveProperty("concurrent-pipelines");
      expect(FEATURE_TIER_MAP).toHaveProperty("team-dashboard");
      expect(FEATURE_TIER_MAP).toHaveProperty("advanced-analytics");
      expect(FEATURE_TIER_MAP).toHaveProperty("sso");
    });

    it("has all completion entries (ciKeys, customSkills, auditLogs, mobile, web)", () => {
      expect(FEATURE_TIER_MAP).toHaveProperty("ciKeys");
      expect(FEATURE_TIER_MAP).toHaveProperty("customSkills");
      expect(FEATURE_TIER_MAP).toHaveProperty("auditLogs");
      expect(FEATURE_TIER_MAP).toHaveProperty("mobile");
      expect(FEATURE_TIER_MAP).toHaveProperty("web");
    });

    it("maps features to valid tiers", () => {
      for (const tier of Object.values(FEATURE_TIER_MAP)) {
        expect(tiers).toContain(tier);
      }
    });

    it("splits along local-free vs cloud-gated (no local feature is gated)", () => {
      // Guardrail: if someone re-gates a local feature they must also revisit
      // whether that feature is truly cloud-only. Local features are free.
      for (const feature of LOCAL_FREE_FEATURES) {
        expect(FEATURE_TIER_MAP[feature]).toBe("community");
      }
      for (const feature of CLOUD_GATED_FEATURES) {
        expect(FEATURE_TIER_MAP[feature]).not.toBe("community");
      }
    });
  });

  // ── Cloud-gated feature tier combinations ────────────────────────────

  describe("mobile tier combinations", () => {
    it("community → not allowed", () => {
      expect(gate.check("mobile", "community").allowed).toBe(false);
    });
    it("pro → allowed", () => {
      expect(gate.check("mobile", "pro").allowed).toBe(true);
    });
  });

  describe("web tier combinations", () => {
    it("community → not allowed", () => {
      expect(gate.check("web", "community").allowed).toBe(false);
    });
    it("pro → not allowed", () => {
      expect(gate.check("web", "pro").allowed).toBe(false);
    });
    it("team → allowed", () => {
      expect(gate.check("web", "team").allowed).toBe(true);
    });
  });

  describe("auditLogs tier combinations", () => {
    it("community → not allowed", () => {
      expect(gate.check("auditLogs", "community").allowed).toBe(false);
    });
    it("team → not allowed", () => {
      expect(gate.check("auditLogs", "team").allowed).toBe(false);
    });
    it("enterprise → allowed", () => {
      expect(gate.check("auditLogs", "enterprise").allowed).toBe(true);
    });
  });
});
