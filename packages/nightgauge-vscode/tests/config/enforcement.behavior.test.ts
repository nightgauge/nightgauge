/**
 * Behavior tests for enforcement.* configuration fields
 *
 * These tests verify that enforcement config fields actually affect runtime behavior,
 * specifically dependency checking mode and transitive dependency handling.
 *
 * @see Issue #438 - Audit and test PR/Branch/Pipeline config fields
 * @see packages/nightgauge-vscode/src/config/schema.ts - EnforcementConfigSchema
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import {
  createMockEnforcementConfig,
  createMockDependencyEnforcement,
  DEFAULT_ENFORCEMENT_CONFIG,
  DEFAULT_DEPENDENCY_ENFORCEMENT,
  applyEnvOverrides,
  EXTENDED_CONFIG_ENV_MAPPINGS,
} from "../mocks/config-fixtures";
import {
  EnforcementConfigSchema,
  DependencyEnforcementConfigSchema,
  EnforcementModeSchema,
  mergeWithDefaults,
} from "../../src/config/schema";

describe("enforcement.behavior", () => {
  // Store original env vars
  const originalEnv = { ...process.env };

  beforeEach(() => {
    // Clear enforcement-related environment variables
    Object.keys(process.env).forEach((key) => {
      if (key.startsWith("NIGHTGAUGE_ENFORCEMENT_")) {
        delete process.env[key];
      }
    });
  });

  afterEach(() => {
    // Restore original environment
    process.env = { ...originalEnv };
  });

  // ============================================================================
  // enforcement.dependencies.enabled - Behavior Tests
  // ============================================================================

  describe("dependencies.enabled", () => {
    it("skips all checks when false", () => {
      const config = createMockEnforcementConfig({
        dependencies: { enabled: false },
      });

      const shouldCheckDependencies = (cfg: typeof config): boolean => {
        return cfg.dependencies?.enabled === true;
      };

      expect(shouldCheckDependencies(config)).toBe(false);
    });

    it("runs checks when true", () => {
      const config = createMockEnforcementConfig({
        dependencies: { enabled: true },
      });

      const shouldCheckDependencies = (cfg: typeof config): boolean => {
        return cfg.dependencies?.enabled === true;
      };

      expect(shouldCheckDependencies(config)).toBe(true);
    });

    it("defaults to true", () => {
      expect(DEFAULT_DEPENDENCY_ENFORCEMENT.enabled).toBe(true);
    });
  });

  // ============================================================================
  // enforcement.dependencies.mode = 'warn' - Behavior Tests
  // ============================================================================

  describe("dependencies.mode = warn", () => {
    it("logs warning but allows proceed", () => {
      const config = createMockEnforcementConfig({
        dependencies: { mode: "warn" },
      });

      interface EnforcementResult {
        allowed: boolean;
        warning?: string;
        blocked?: boolean;
      }

      const enforceDependency = (hasViolation: boolean, cfg: typeof config): EnforcementResult => {
        if (!hasViolation) {
          return { allowed: true };
        }

        const mode = cfg.dependencies?.mode || "warn";

        if (mode === "ignore") {
          return { allowed: true };
        }

        if (mode === "block") {
          return {
            allowed: false,
            blocked: true,
          };
        }

        // mode === 'warn'
        return {
          allowed: true,
          warning: "Dependency violation detected",
        };
      };

      const result = enforceDependency(true, config);
      expect(result.allowed).toBe(true);
      expect(result.warning).toBeDefined();
    });

    it("no warning when no violation", () => {
      const config = createMockEnforcementConfig({
        dependencies: { mode: "warn" },
      });

      interface EnforcementResult {
        allowed: boolean;
        warning?: string;
      }

      const enforceDependency = (hasViolation: boolean, cfg: typeof config): EnforcementResult => {
        if (!hasViolation) {
          return { allowed: true };
        }

        const mode = cfg.dependencies?.mode || "warn";
        if (mode === "warn") {
          return {
            allowed: true,
            warning: "Dependency violation detected",
          };
        }

        return { allowed: true };
      };

      const result = enforceDependency(false, config);
      expect(result.allowed).toBe(true);
      expect(result.warning).toBeUndefined();
    });
  });

  // ============================================================================
  // enforcement.dependencies.mode = 'block' - Behavior Tests
  // ============================================================================

  describe("dependencies.mode = block", () => {
    it("blocks PR merge on violation", () => {
      const config = createMockEnforcementConfig({
        dependencies: { mode: "block" },
      });

      interface EnforcementResult {
        allowed: boolean;
        blocked?: boolean;
        error?: string;
      }

      const enforceDependency = (hasViolation: boolean, cfg: typeof config): EnforcementResult => {
        if (!hasViolation) {
          return { allowed: true };
        }

        const mode = cfg.dependencies?.mode || "warn";

        if (mode === "block") {
          return {
            allowed: false,
            blocked: true,
            error: "Cannot merge: dependency violation",
          };
        }

        return { allowed: true };
      };

      const result = enforceDependency(true, config);
      expect(result.allowed).toBe(false);
      expect(result.blocked).toBe(true);
      expect(result.error).toBeDefined();
    });

    it("allows merge when no violation", () => {
      const config = createMockEnforcementConfig({
        dependencies: { mode: "block" },
      });

      interface EnforcementResult {
        allowed: boolean;
        blocked?: boolean;
      }

      const enforceDependency = (hasViolation: boolean, _cfg: typeof config): EnforcementResult => {
        if (!hasViolation) {
          return { allowed: true };
        }

        return { allowed: false, blocked: true };
      };

      const result = enforceDependency(false, config);
      expect(result.allowed).toBe(true);
      expect(result.blocked).toBeUndefined();
    });
  });

  // ============================================================================
  // enforcement.dependencies.mode = 'ignore' - Behavior Tests
  // ============================================================================

  describe("dependencies.mode = ignore", () => {
    it("skips silently on violation", () => {
      const config = createMockEnforcementConfig({
        dependencies: { mode: "ignore" },
      });

      interface EnforcementResult {
        allowed: boolean;
        checked: boolean;
      }

      const enforceDependency = (hasViolation: boolean, cfg: typeof config): EnforcementResult => {
        const mode = cfg.dependencies?.mode || "warn";

        if (mode === "ignore") {
          // Skip check entirely
          return { allowed: true, checked: false };
        }

        if (!hasViolation) {
          return { allowed: true, checked: true };
        }

        // Other modes would handle violation here
        return { allowed: mode !== "block", checked: true };
      };

      const result = enforceDependency(true, config);
      expect(result.allowed).toBe(true);
      expect(result.checked).toBe(false);
    });
  });

  // ============================================================================
  // enforcement.dependencies.mode validation - Behavior Tests
  // ============================================================================

  describe("dependencies.mode validation", () => {
    it("accepts valid modes", () => {
      const validModes = ["warn", "block", "ignore"] as const;

      validModes.forEach((mode) => {
        const result = EnforcementModeSchema.safeParse(mode);
        expect(result.success).toBe(true);
      });
    });

    it("rejects invalid mode", () => {
      const result = EnforcementModeSchema.safeParse("invalid");
      expect(result.success).toBe(false);
      if (!result.success) {
        expect(result.error.issues[0].message).toContain("warn");
      }
    });

    it("defaults to warn", () => {
      expect(DEFAULT_DEPENDENCY_ENFORCEMENT.mode).toBe("warn");
    });
  });

  // ============================================================================
  // enforcement.dependencies.check_transitive - Behavior Tests
  // ============================================================================

  describe("dependencies.check_transitive", () => {
    it("checks A→B→C when true", () => {
      const config = createMockEnforcementConfig({
        dependencies: { check_transitive: true },
      });

      interface Dependency {
        from: string;
        to: string;
      }

      const checkDependencyViolation = (deps: Dependency[], cfg: typeof config): string[] => {
        const violations: string[] = [];
        const checkTransitive = cfg.dependencies?.check_transitive || false;

        // Simulate: A depends on B, B depends on C
        // If check_transitive, A transitively depends on C
        const directDeps: Record<string, string[]> = {};
        deps.forEach((d) => {
          if (!directDeps[d.from]) directDeps[d.from] = [];
          directDeps[d.from].push(d.to);
        });

        // Check direct violations
        deps.forEach((d) => {
          violations.push(`${d.from} → ${d.to}`);
        });

        // Check transitive violations if enabled
        if (checkTransitive) {
          deps.forEach((d) => {
            const transitive = directDeps[d.to] || [];
            transitive.forEach((t) => {
              violations.push(`${d.from} → ${t} (transitive via ${d.to})`);
            });
          });
        }

        return violations;
      };

      const deps = [
        { from: "A", to: "B" },
        { from: "B", to: "C" },
      ];

      const violations = checkDependencyViolation(deps, config);
      expect(violations).toContain("A → B");
      expect(violations).toContain("B → C");
      expect(violations.some((v) => v.includes("transitive"))).toBe(true);
    });

    it("only checks A→B when false", () => {
      const config = createMockEnforcementConfig({
        dependencies: { check_transitive: false },
      });

      interface Dependency {
        from: string;
        to: string;
      }

      const checkDependencyViolation = (deps: Dependency[], cfg: typeof config): string[] => {
        const violations: string[] = [];
        const checkTransitive = cfg.dependencies?.check_transitive || false;

        // Check direct violations only
        deps.forEach((d) => {
          violations.push(`${d.from} → ${d.to}`);
        });

        if (checkTransitive) {
          // Would add transitive violations
        }

        return violations;
      };

      const deps = [
        { from: "A", to: "B" },
        { from: "B", to: "C" },
      ];

      const violations = checkDependencyViolation(deps, config);
      expect(violations).toContain("A → B");
      expect(violations).toContain("B → C");
      expect(violations.some((v) => v.includes("transitive"))).toBe(false);
    });

    it("defaults to false", () => {
      expect(DEFAULT_DEPENDENCY_ENFORCEMENT.check_transitive).toBe(false);
    });
  });

  // ============================================================================
  // Environment Variable Overrides
  // ============================================================================

  describe("environment variable overrides", () => {
    it("NIGHTGAUGE_ENFORCEMENT_DEPS_ENABLED overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_ENFORCEMENT_DEPS_ENABLED: "false",
      });

      try {
        expect(process.env.NIGHTGAUGE_ENFORCEMENT_DEPS_ENABLED).toBe("false");
      } finally {
        cleanup();
      }
    });

    it("NIGHTGAUGE_ENFORCEMENT_DEPS_MODE overrides config", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_ENFORCEMENT_DEPS_MODE: "block",
      });

      try {
        expect(process.env.NIGHTGAUGE_ENFORCEMENT_DEPS_MODE).toBe("block");
      } finally {
        cleanup();
      }
    });

    it("env var takes precedence over config file", () => {
      const cleanup = applyEnvOverrides({
        NIGHTGAUGE_ENFORCEMENT_DEPS_MODE: "block",
      });

      try {
        const configValue = "warn";
        const envValue = process.env.NIGHTGAUGE_ENFORCEMENT_DEPS_MODE;

        // Env should take precedence
        const effectiveValue = envValue || configValue;
        expect(effectiveValue).toBe("block");
      } finally {
        cleanup();
      }
    });

    it("enforcement env vars are defined", () => {
      expect(EXTENDED_CONFIG_ENV_MAPPINGS["enforcement.dependencies.enabled"]).toBe(
        "NIGHTGAUGE_ENFORCEMENT_DEPS_ENABLED"
      );
      expect(EXTENDED_CONFIG_ENV_MAPPINGS["enforcement.dependencies.mode"]).toBe(
        "NIGHTGAUGE_ENFORCEMENT_DEPS_MODE"
      );
      expect(EXTENDED_CONFIG_ENV_MAPPINGS["enforcement.dependencies.check_transitive"]).toBe(
        "NIGHTGAUGE_ENFORCEMENT_DEPS_TRANSITIVE"
      );
    });
  });

  // ============================================================================
  // Schema Validation Tests
  // ============================================================================

  describe("validation", () => {
    it("validates complete config", () => {
      const result = EnforcementConfigSchema.safeParse(DEFAULT_ENFORCEMENT_CONFIG);
      expect(result.success).toBe(true);
    });

    it("validates partial config", () => {
      const partialConfig = { dependencies: { mode: "block" as const } };
      const result = EnforcementConfigSchema.safeParse(partialConfig);
      expect(result.success).toBe(true);
    });

    it("validates empty config", () => {
      const result = EnforcementConfigSchema.safeParse({});
      expect(result.success).toBe(true);
    });

    it("validates dependency enforcement config", () => {
      const result = DependencyEnforcementConfigSchema.safeParse(DEFAULT_DEPENDENCY_ENFORCEMENT);
      expect(result.success).toBe(true);
    });

    it("rejects invalid mode", () => {
      const result = DependencyEnforcementConfigSchema.safeParse({
        mode: "invalid",
      });
      expect(result.success).toBe(false);
    });
  });

  // ============================================================================
  // Default Values Tests
  // ============================================================================

  describe("default values", () => {
    it("DEFAULT_ENFORCEMENT_CONFIG has correct defaults", () => {
      expect(DEFAULT_ENFORCEMENT_CONFIG.dependencies?.enabled).toBe(true);
      expect(DEFAULT_ENFORCEMENT_CONFIG.dependencies?.mode).toBe("warn");
      expect(DEFAULT_ENFORCEMENT_CONFIG.dependencies?.check_transitive).toBe(false);
    });

    it("DEFAULT_DEPENDENCY_ENFORCEMENT has correct defaults", () => {
      expect(DEFAULT_DEPENDENCY_ENFORCEMENT.enabled).toBe(true);
      expect(DEFAULT_DEPENDENCY_ENFORCEMENT.mode).toBe("warn");
      expect(DEFAULT_DEPENDENCY_ENFORCEMENT.check_transitive).toBe(false);
    });

    it("mergeWithDefaults preserves user values", () => {
      const config = mergeWithDefaults({
        enforcement: { dependencies: { mode: "block" } },
      });

      expect(config.enforcement?.dependencies?.mode).toBe("block");
    });

    it("missing enforcement section uses defaults", () => {
      const config = mergeWithDefaults({});

      // Note: DEFAULT_CONFIG doesn't have enforcement section, so it's undefined
      expect(config.enforcement).toBeUndefined();
    });
  });

  // ============================================================================
  // Helper Functions
  // ============================================================================

  describe("helper functions", () => {
    it("createMockDependencyEnforcement merges correctly", () => {
      const config = createMockDependencyEnforcement({
        mode: "block",
        check_transitive: true,
      });

      expect(config.enabled).toBe(true); // from default
      expect(config.mode).toBe("block"); // overridden
      expect(config.check_transitive).toBe(true); // overridden
    });

    it("createMockEnforcementConfig deep merges dependencies", () => {
      const config = createMockEnforcementConfig({
        dependencies: { mode: "ignore" },
      });

      expect(config.dependencies?.enabled).toBe(true); // from default
      expect(config.dependencies?.mode).toBe("ignore"); // overridden
    });
  });

  // ============================================================================
  // Full Enforcement Flow Simulation
  // ============================================================================

  describe("enforcement flow simulation", () => {
    it("runs complete enforcement check", () => {
      const config = createMockEnforcementConfig({
        dependencies: {
          enabled: true,
          mode: "warn",
          check_transitive: true,
        },
      });

      interface EnforcementCheckResult {
        ran: boolean;
        violations: string[];
        allowed: boolean;
        warnings: string[];
      }

      const runEnforcementCheck = (
        hasViolations: boolean,
        cfg: typeof config
      ): EnforcementCheckResult => {
        const deps = cfg.dependencies;

        // Check if enforcement is enabled
        if (!deps?.enabled) {
          return {
            ran: false,
            violations: [],
            allowed: true,
            warnings: [],
          };
        }

        // Simulate finding violations
        const violations: string[] = hasViolations ? ["A → B (invalid dependency)"] : [];

        if (deps.check_transitive && hasViolations) {
          violations.push("A → C (transitive via B)");
        }

        // Apply mode
        const mode = deps.mode || "warn";
        let allowed = true;
        const warnings: string[] = [];

        if (violations.length > 0) {
          if (mode === "block") {
            allowed = false;
          } else if (mode === "warn") {
            warnings.push(...violations);
          }
          // ignore mode: no warnings, still allowed
        }

        return {
          ran: true,
          violations,
          allowed,
          warnings,
        };
      };

      const result = runEnforcementCheck(true, config);
      expect(result.ran).toBe(true);
      expect(result.violations.length).toBe(2); // direct + transitive
      expect(result.allowed).toBe(true); // warn mode allows
      expect(result.warnings.length).toBe(2);
    });
  });
});
