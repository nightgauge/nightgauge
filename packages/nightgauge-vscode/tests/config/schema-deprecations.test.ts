/**
 * Tests for Zod deprecation warning markers in the Nightgauge config schema.
 *
 * @see Issue #3646 — Zod deprecation markers for migrated-tier keys
 * @see packages/nightgauge-vscode/src/config/schema.ts
 */

import { describe, it, expect } from "vitest";
import {
  validateConfig,
  type ConfigValidationResult,
  type ConfigValidationWarning,
} from "../../src/config/schema";

// ── helpers ──────────────────────────────────────────────────────────────────

function expectNoWarnings(result: ConfigValidationResult) {
  expect(result.warnings).toHaveLength(0);
}

function expectWarningForField(warnings: ConfigValidationWarning[], field: string) {
  const match = warnings.find((w) => w.field === field);
  expect(
    match,
    `expected warning for field "${field}" but got: ${JSON.stringify(warnings)}`
  ).toBeDefined();
}

// ── suite ─────────────────────────────────────────────────────────────────────

describe("validateConfig — deprecation warnings", () => {
  // ── baseline ───────────────────────────────────────────────────────────────

  it("returns empty warnings for a clean config", () => {
    const result = validateConfig({ project: { number: 42 } });
    expect(result.valid).toBe(true);
    expect(result.errors).toHaveLength(0);
    expectNoWarnings(result);
  });

  it("returns empty warnings for an empty config", () => {
    const result = validateConfig({});
    expect(result.valid).toBe(true);
    expectNoWarnings(result);
  });

  // ── github_user ────────────────────────────────────────────────────────────

  it("warns on github_user and keeps valid: true", () => {
    const result = validateConfig({ github_user: "octocat" });
    expect(result.valid).toBe(true);
    expect(result.errors).toHaveLength(0);
    expectWarningForField(result.warnings, "github_user");
  });

  it("github_user warning message references machine tier", () => {
    const result = validateConfig({ github_user: "octocat" });
    const w = result.warnings.find((w) => w.field === "github_user")!;
    expect(w.message).toMatch(/machine tier/i);
    expect(w.message).toMatch(/~\/.nightgauge\/config\.yaml/);
  });

  // ── lm_studio ──────────────────────────────────────────────────────────────

  it("warns on lm_studio and keeps valid: true", () => {
    const result = validateConfig({ lm_studio: { base_url: "http://localhost:1234" } });
    expect(result.valid).toBe(true);
    expect(result.errors).toHaveLength(0);
    expectWarningForField(result.warnings, "lm_studio");
  });

  it("lm_studio warning message references machine tier", () => {
    const result = validateConfig({ lm_studio: { base_url: "http://localhost:1234" } });
    const w = result.warnings.find((w) => w.field === "lm_studio")!;
    expect(w.message).toMatch(/machine tier/i);
  });

  // ── autonomous.enabled_repos ───────────────────────────────────────────────

  it("warns on autonomous.enabled_repos and keeps valid: true", () => {
    const result = validateConfig({ autonomous: { enabled_repos: ["nightgauge"] } });
    expect(result.valid).toBe(true);
    expect(result.errors).toHaveLength(0);
    expectWarningForField(result.warnings, "autonomous.enabled_repos");
  });

  it("autonomous.enabled_repos warning message references machine tier / #3643", () => {
    const result = validateConfig({ autonomous: { enabled_repos: ["nightgauge"] } });
    const w = result.warnings.find((w) => w.field === "autonomous.enabled_repos")!;
    expect(w.message).toMatch(/#3643/);
  });

  // ── autonomous.max_concurrent ──────────────────────────────────────────────

  it("warns on autonomous.max_concurrent and keeps valid: true", () => {
    const result = validateConfig({ autonomous: { max_concurrent: 2 } });
    expect(result.valid).toBe(true);
    expect(result.errors).toHaveLength(0);
    expectWarningForField(result.warnings, "autonomous.max_concurrent");
  });

  it("autonomous.max_concurrent warning message references pipeline.max_concurrent", () => {
    const result = validateConfig({ autonomous: { max_concurrent: 2 } });
    const w = result.warnings.find((w) => w.field === "autonomous.max_concurrent")!;
    expect(w.message).toMatch(/pipeline\.max_concurrent/);
  });

  // ── autonomous.repositories per-repo keys ─────────────────────────────────

  it("warns on autonomous.repositories.<repo>.sequential", () => {
    const result = validateConfig({
      autonomous: {
        repositories: {
          nightgauge: { sequential: true },
        },
      },
    });
    expect(result.valid).toBe(true);
    expect(result.errors).toHaveLength(0);
    expectWarningForField(result.warnings, "autonomous.repositories.nightgauge.sequential");
  });

  it("warns on autonomous.repositories.<repo>.max_concurrent", () => {
    const result = validateConfig({
      autonomous: {
        repositories: {
          nightgauge: { max_concurrent: 3 },
        },
      },
    });
    expect(result.valid).toBe(true);
    expect(result.errors).toHaveLength(0);
    expectWarningForField(result.warnings, "autonomous.repositories.nightgauge.max_concurrent");
  });

  it("warns for both deprecated per-repo keys in the same repo", () => {
    const result = validateConfig({
      autonomous: {
        repositories: {
          "nightgauge/nightgauge": { sequential: false, max_concurrent: 1 },
        },
      },
    });
    expect(result.valid).toBe(true);
    expectWarningForField(
      result.warnings,
      "autonomous.repositories.nightgauge/nightgauge.sequential"
    );
    expectWarningForField(
      result.warnings,
      "autonomous.repositories.nightgauge/nightgauge.max_concurrent"
    );
  });

  it("warns for deprecated keys across multiple repos independently", () => {
    const result = validateConfig({
      autonomous: {
        repositories: {
          "repo-a": { sequential: true },
          "repo-b": { max_concurrent: 2 },
        },
      },
    });
    expect(result.valid).toBe(true);
    expectWarningForField(result.warnings, "autonomous.repositories.repo-a.sequential");
    expectWarningForField(result.warnings, "autonomous.repositories.repo-b.max_concurrent");
    // repo-a should NOT have a max_concurrent warning (key absent)
    const spurious = result.warnings.find(
      (w) => w.field === "autonomous.repositories.repo-a.max_concurrent"
    );
    expect(spurious).toBeUndefined();
  });

  // ── all six keys together ─────────────────────────────────────────────────

  it("emits one warning per deprecated key when all six are present", () => {
    const result = validateConfig({
      github_user: "octocat",
      lm_studio: { base_url: "http://localhost:1234" },
      autonomous: {
        max_concurrent: 1,
        enabled_repos: ["nightgauge"],
        repositories: {
          nightgauge: { sequential: true, max_concurrent: 2 },
        },
      },
    });
    expect(result.valid).toBe(true);
    expect(result.errors).toHaveLength(0);
    expect(result.warnings.length).toBeGreaterThanOrEqual(6);

    const fields = result.warnings.map((w) => w.field);
    expect(fields).toContain("github_user");
    expect(fields).toContain("lm_studio");
    expect(fields).toContain("autonomous.enabled_repos");
    expect(fields).toContain("autonomous.max_concurrent");
    expect(fields).toContain("autonomous.repositories.nightgauge.sequential");
    expect(fields).toContain("autonomous.repositories.nightgauge.max_concurrent");
  });

  // ── non-deprecated autonomous keys produce no warnings ────────────────────

  it("does not warn for non-deprecated autonomous fields", () => {
    const result = validateConfig({
      autonomous: {
        scan_interval: "30s",
        dry_run: true,
        pickup_backlog: false,
        on_failure_status: "ready",
      },
    });
    expect(result.valid).toBe(true);
    expectNoWarnings(result);
  });

  // ── valid: false when real structural errors exist ────────────────────────

  it("returns valid: false when there are real validation errors (not just warnings)", () => {
    // project.number must be a number; passing a string is a structural type error
    const result = validateConfig({
      project: { number: "not-a-number" as unknown as number },
    });
    expect(result.valid).toBe(false);
    expect(result.errors.length).toBeGreaterThan(0);
  });

  it("returns valid: true with warnings when only deprecated keys are present (no structural errors)", () => {
    // Zod superRefine callbacks only fire when the base schema passes; a config
    // with only deprecated-but-accepted keys (no type errors) must be valid with warnings.
    const result = validateConfig({
      github_user: "octocat",
      lm_studio: { base_url: "http://localhost:1234" },
    });
    expect(result.valid).toBe(true);
    expect(result.errors).toHaveLength(0);
    expectWarningForField(result.warnings, "github_user");
    expectWarningForField(result.warnings, "lm_studio");
  });
});
