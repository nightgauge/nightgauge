/**
 * Behavioral tests for platform configuration schema (Issue #1458)
 *
 * Covers: schema validation, default values, tier merging, env var overrides,
 * and kill switch behavior.
 *
 * @see packages/nightgauge-vscode/src/config/schema.ts
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import {
  PlatformConfigSchema,
  PlatformRetryPolicySchema,
  PlatformTelemetrySchema,
  mergeWithDefaults,
  getDefaultConfig,
  validateConfig,
  deepMerge,
} from "../../src/config/schema";
import { resolveEnvVars } from "../../src/config/envVarResolver";

// ============================================================================
// Schema Validation
// ============================================================================

describe("PlatformRetryPolicySchema", () => {
  it("accepts valid retry policy", () => {
    const result = PlatformRetryPolicySchema.safeParse({
      attempts: 3,
      backoff_ms: 1000,
      backoff_multiplier: 2,
    });
    expect(result.success).toBe(true);
  });

  it("accepts partial retry policy", () => {
    const result = PlatformRetryPolicySchema.safeParse({ attempts: 5 });
    expect(result.success).toBe(true);
  });

  it("rejects attempts of 0 (min 1)", () => {
    const result = PlatformRetryPolicySchema.safeParse({ attempts: 0 });
    expect(result.success).toBe(false);
  });

  it("rejects attempts of 11 (max 10)", () => {
    const result = PlatformRetryPolicySchema.safeParse({ attempts: 11 });
    expect(result.success).toBe(false);
  });

  it("rejects backoff_multiplier of 0.5 (min 1)", () => {
    const result = PlatformRetryPolicySchema.safeParse({
      backoff_multiplier: 0.5,
    });
    expect(result.success).toBe(false);
  });

  it("rejects backoff_ms of -1 (min 0)", () => {
    const result = PlatformRetryPolicySchema.safeParse({ backoff_ms: -1 });
    expect(result.success).toBe(false);
  });
});

describe("PlatformTelemetrySchema", () => {
  it("accepts enabled: true", () => {
    const result = PlatformTelemetrySchema.safeParse({ enabled: true });
    expect(result.success).toBe(true);
  });

  it("accepts enabled: false", () => {
    const result = PlatformTelemetrySchema.safeParse({ enabled: false });
    expect(result.success).toBe(true);
  });

  it("accepts empty object", () => {
    const result = PlatformTelemetrySchema.safeParse({});
    expect(result.success).toBe(true);
  });
});

describe("PlatformConfigSchema", () => {
  it("accepts valid full platform config", () => {
    const result = PlatformConfigSchema.safeParse({
      enabled: true,
      api_url: "https://api.nightgauge.dev",
      connection_timeout_ms: 30000,
      retry_policy: {
        attempts: 3,
        backoff_ms: 1000,
        backoff_multiplier: 2,
      },
      telemetry: { enabled: true },
      feature_flags: { new_dashboard: true, beta_models: false },
    });
    expect(result.success).toBe(true);
  });

  it("accepts minimal config with only enabled: false", () => {
    const result = PlatformConfigSchema.safeParse({ enabled: false });
    expect(result.success).toBe(true);
  });

  it("accepts empty object", () => {
    const result = PlatformConfigSchema.safeParse({});
    expect(result.success).toBe(true);
  });

  it("rejects api_url with invalid URL", () => {
    const result = PlatformConfigSchema.safeParse({ api_url: "not-a-url" });
    expect(result.success).toBe(false);
  });

  it("rejects connection_timeout_ms of -1 (min 0)", () => {
    const result = PlatformConfigSchema.safeParse({
      connection_timeout_ms: -1,
    });
    expect(result.success).toBe(false);
  });

  it("accepts connection_timeout_ms of 0", () => {
    const result = PlatformConfigSchema.safeParse({
      connection_timeout_ms: 0,
    });
    expect(result.success).toBe(true);
  });

  it("accepts valid localhost api_url for dev overrides", () => {
    const result = PlatformConfigSchema.safeParse({
      api_url: "http://localhost:8080",
    });
    expect(result.success).toBe(true);
  });

  it("accepts feature_flags record", () => {
    const result = PlatformConfigSchema.safeParse({
      feature_flags: { flag_a: true, flag_b: false },
    });
    expect(result.success).toBe(true);
  });
});

// ============================================================================
// Default Values
// ============================================================================

describe("platform default values", () => {
  it("getDefaultConfig includes platform section", () => {
    const defaults = getDefaultConfig();
    expect(defaults.platform).toBeDefined();
  });

  it("platform.enabled defaults to false", () => {
    const defaults = getDefaultConfig();
    expect(defaults.platform?.enabled).toBe(false);
  });

  it("platform.api_url defaults to production endpoint", () => {
    const defaults = getDefaultConfig();
    expect(defaults.platform?.api_url).toBe("https://api.nightgauge.dev");
  });

  it("platform.connection_timeout_ms defaults to 30000", () => {
    const defaults = getDefaultConfig();
    expect(defaults.platform?.connection_timeout_ms).toBe(30000);
  });

  it("platform.retry_policy.attempts defaults to 3", () => {
    const defaults = getDefaultConfig();
    expect(defaults.platform?.retry_policy?.attempts).toBe(3);
  });

  it("platform.retry_policy.backoff_ms defaults to 1000", () => {
    const defaults = getDefaultConfig();
    expect(defaults.platform?.retry_policy?.backoff_ms).toBe(1000);
  });

  it("platform.retry_policy.backoff_multiplier defaults to 2", () => {
    const defaults = getDefaultConfig();
    expect(defaults.platform?.retry_policy?.backoff_multiplier).toBe(2);
  });

  it("platform.telemetry.enabled defaults to false", () => {
    const defaults = getDefaultConfig();
    expect(defaults.platform?.telemetry?.enabled).toBe(false);
  });

  it("platform.feature_flags defaults to empty object", () => {
    const defaults = getDefaultConfig();
    expect(defaults.platform?.feature_flags).toEqual({});
  });

  it("mergeWithDefaults({}) produces expected platform defaults", () => {
    const config = mergeWithDefaults({});
    expect(config.platform?.enabled).toBe(false);
    expect(config.platform?.api_url).toBe("https://api.nightgauge.dev");
    expect(config.platform?.connection_timeout_ms).toBe(30000);
  });
});

// ============================================================================
// Tier Merging
// ============================================================================

describe("platform tier merging", () => {
  it("project-tier api_url override replaces the default", () => {
    const config = mergeWithDefaults({
      platform: { api_url: "https://staging.api.nightgauge.dev" },
    });
    expect(config.platform?.api_url).toBe("https://staging.api.nightgauge.dev");
    // Other defaults preserved
    expect(config.platform?.enabled).toBe(false);
    expect(config.platform?.connection_timeout_ms).toBe(30000);
  });

  it("local-tier enabled: false overrides project-tier enabled: true", () => {
    // Simulate project tier with enabled: true
    const projectTier = mergeWithDefaults({ platform: { enabled: true } });
    // Then apply local tier with enabled: false
    const localTierOverride = { platform: { enabled: false } };
    const finalConfig = deepMerge(
      projectTier as Record<string, unknown>,
      localTierOverride as Record<string, unknown>
    );
    expect((finalConfig as typeof projectTier).platform?.enabled).toBe(false);
  });

  it("partial retry_policy override deep-merges with defaults", () => {
    // Override only attempts; backoff_ms and backoff_multiplier should remain from defaults
    const config = mergeWithDefaults({
      platform: {
        retry_policy: { attempts: 5 },
      },
    });
    expect(config.platform?.retry_policy?.attempts).toBe(5);
    expect(config.platform?.retry_policy?.backoff_ms).toBe(1000);
    expect(config.platform?.retry_policy?.backoff_multiplier).toBe(2);
  });

  it("feature_flags record is replaced (not merged) when overriding", () => {
    // The deep merge engine replaces records (same as arrays)
    // feature_flags: {} default + override { new_flag: true } = { new_flag: true }
    const config = mergeWithDefaults({
      platform: {
        feature_flags: { new_flag: true },
      },
    });
    // Only the override flags present — default {} was replaced
    expect(config.platform?.feature_flags).toEqual({ new_flag: true });
  });
});

// ============================================================================
// Environment Variable Override
// ============================================================================

describe("platform env var overrides", () => {
  const originalEnv = { ...process.env };

  beforeEach(() => {
    Object.keys(process.env).forEach((key) => {
      if (key.startsWith("NIGHTGAUGE_")) {
        delete process.env[key];
      }
    });
  });

  afterEach(() => {
    process.env = { ...originalEnv };
  });

  it("NIGHTGAUGE_PLATFORM_ENABLED=false produces enabled: false", () => {
    process.env["NIGHTGAUGE_PLATFORM_ENABLED"] = "false";
    const { config } = resolveEnvVars();
    expect(config.platform?.enabled).toBe(false);
  });

  it("NIGHTGAUGE_PLATFORM_API_URL produces correct partial", () => {
    process.env["NIGHTGAUGE_PLATFORM_API_URL"] = "http://localhost:8080";
    const { config } = resolveEnvVars();
    expect(config.platform?.api_url).toBe("http://localhost:8080");
  });

  it("NIGHTGAUGE_PLATFORM_CONNECTION_TIMEOUT_MS produces correct number", () => {
    process.env["NIGHTGAUGE_PLATFORM_CONNECTION_TIMEOUT_MS"] = "5000";
    const { config } = resolveEnvVars();
    expect(config.platform?.connection_timeout_ms).toBe(5000);
  });
});

// ============================================================================
// Kill Switch Behavior
// ============================================================================

describe("platform kill switch (enabled: false)", () => {
  it("config with platform.enabled: false passes schema validation", () => {
    const result = validateConfig({ platform: { enabled: false } });
    expect(result.valid).toBe(true);
  });

  it("platform.enabled defaults to false when omitted", () => {
    const config = mergeWithDefaults({});
    expect(config.platform?.enabled).toBe(false);
  });

  it("platform.enabled: false is preserved after mergeWithDefaults", () => {
    const config = mergeWithDefaults({ platform: { enabled: false } });
    expect(config.platform?.enabled).toBe(false);
  });
});
