/**
 * Regression Trigger Evaluator — determine whether a set of changed files
 * requires a full regression run rather than selective test execution.
 *
 * Seven named trigger types are evaluated in priority order:
 *   1. manual-override   — `forceFullRegression: true` in config
 *   2. dependency-change — package.json, go.mod, lock files
 *   3. build-config      — tsconfig, vitest/vite/webpack configs
 *   4. shared-types      — shared-types/ directories, .d.ts files
 *   5. test-infrastructure — __mocks__/, test-utils/, setup files
 *   6. ci-config         — .github/workflows/, CI config files
 *   7. low-confidence    — post-graph check (use evaluateLowConfidenceTrigger)
 *
 * Two named exports:
 * - `evaluateRegressionTriggers` — file-based triggers (1–6), call before graph walk
 * - `evaluateLowConfidenceTrigger` — confidence trigger (7), call after graph walk
 * - `matchesPattern` — re-exported for use by ChangeImpactAnalyzer
 *
 * @see Issue #1974 — Full Regression Trigger Rules
 */

import type {
  DiffEntry,
  ImpactAnalysisResult,
  RegressionTriggerEvaluation,
  RegressionTriggerType,
} from "./change-impact-types.js";
import {
  DEFAULT_BUILD_CONFIG_PATTERNS,
  DEFAULT_CI_CONFIG_PATTERNS,
  DEFAULT_DEPENDENCY_PATTERNS,
  DEFAULT_SHARED_TYPES_PATTERNS,
  DEFAULT_TEST_INFRASTRUCTURE_PATTERNS,
  RegressionTriggerConfigSchema,
  type ParsedChangeImpactAnalyzerConfig,
} from "./change-impact-types.js";

// ---------------------------------------------------------------------------
// Pattern matching (extracted from ChangeImpactAnalyzer for shared use)
// ---------------------------------------------------------------------------

/**
 * Check whether a file path matches any of the given patterns.
 *
 * Pattern syntax:
 * - double-star suffix: directory prefix match (.github/** matches any path under .github/)
 * - double-star segment: **\/segment\/** matches any path containing that segment
 * - double-star glob: **\/*.ext matches basename against the ext pattern
 * - single-star wildcard: glob in filename (tsconfig* matches tsconfig.json)
 * - literal: exact basename match (package.json)
 */
export function matchesPattern(filePath: string, patterns: string[]): boolean {
  const normalized = filePath.replace(/\\/g, "/");
  const basename = normalized.split("/").pop() ?? "";

  return patterns.some((pattern) => {
    // Double-star prefix: `**/__mocks__/**` or `**/shared-types/**`
    if (pattern.startsWith("**/") && pattern.endsWith("/**")) {
      const segment = pattern.slice(3, -3);
      return normalized.includes(`/${segment}/`) || normalized.startsWith(`${segment}/`);
    }

    // Double-star glob in basename: `**/*.d.ts`
    if (pattern.startsWith("**/")) {
      const rest = pattern.slice(3); // e.g. `*.d.ts`
      return matchesGlob(basename, rest);
    }

    // Directory prefix: `.github/**`
    if (pattern.endsWith("/**")) {
      const prefix = pattern.slice(0, -3);
      return normalized.startsWith(`${prefix}/`) || normalized === prefix;
    }

    // Wildcard in basename: `tsconfig*`, `.babelrc*`
    if (pattern.includes("*")) {
      return matchesGlob(basename, pattern);
    }

    // Literal: exact basename match
    return basename === pattern;
  });
}

/** Simple single-star glob matching against a string (no path separators). */
function matchesGlob(value: string, pattern: string): boolean {
  const escaped = pattern.replace(/[.+^${}()|[\]\\]/g, "\\$&").replace(/\*/g, ".*");
  return new RegExp(`^${escaped}$`).test(value);
}

// ---------------------------------------------------------------------------
// Default trigger rule configuration
// ---------------------------------------------------------------------------

/** Default parsed trigger configuration (all triggers enabled, no extra patterns). */
export const DEFAULT_REGRESSION_TRIGGER_CONFIG = RegressionTriggerConfigSchema.parse({});

// ---------------------------------------------------------------------------
// File-based trigger table
// ---------------------------------------------------------------------------

type FileTriggerKey =
  "dependencyChange" | "buildConfig" | "sharedTypes" | "testInfrastructure" | "ciConfig";

interface FileTriggerRule {
  type: Extract<
    RegressionTriggerType,
    "dependency-change" | "build-config" | "shared-types" | "test-infrastructure" | "ci-config"
  >;
  configKey: FileTriggerKey;
  defaultPatterns: string[];
  messageTemplate: (matchedFile: string) => string;
}

const FILE_TRIGGER_RULES: FileTriggerRule[] = [
  {
    type: "dependency-change",
    configKey: "dependencyChange",
    defaultPatterns: DEFAULT_DEPENDENCY_PATTERNS,
    messageTemplate: (f) =>
      `Dependency file changed (${f}) — full regression required to validate all consumers`,
  },
  {
    type: "build-config",
    configKey: "buildConfig",
    defaultPatterns: DEFAULT_BUILD_CONFIG_PATTERNS,
    messageTemplate: (f) => `Build configuration changed (${f}) — full regression required`,
  },
  {
    type: "shared-types",
    configKey: "sharedTypes",
    defaultPatterns: DEFAULT_SHARED_TYPES_PATTERNS,
    messageTemplate: (f) => `Shared type definitions changed (${f}) — full regression required`,
  },
  {
    type: "test-infrastructure",
    configKey: "testInfrastructure",
    defaultPatterns: DEFAULT_TEST_INFRASTRUCTURE_PATTERNS,
    messageTemplate: (f) => `Test infrastructure changed (${f}) — full regression required`,
  },
  {
    type: "ci-config",
    configKey: "ciConfig",
    defaultPatterns: DEFAULT_CI_CONFIG_PATTERNS,
    messageTemplate: (f) => `CI configuration changed (${f}) — full regression required`,
  },
];

// ---------------------------------------------------------------------------
// evaluateRegressionTriggers
// ---------------------------------------------------------------------------

/**
 * Evaluate file-based regression triggers (types 1–6, excluding low-confidence).
 *
 * Call this BEFORE the dependency graph walk to enable early short-circuit.
 * Returns the first matching trigger in priority order, or `{ triggered: false }`.
 *
 * The `manual-override` trigger (via `config.forceFullRegression`) is checked
 * first and has highest priority.
 *
 * @param changedFiles - Parsed diff entries from parseDiff()
 * @param config - Parsed ChangeImpactAnalyzer config (after schema defaults applied)
 */
export function evaluateRegressionTriggers(
  changedFiles: DiffEntry[],
  config: ParsedChangeImpactAnalyzerConfig
): RegressionTriggerEvaluation {
  if (changedFiles.length === 0) {
    return { triggered: false };
  }

  // Priority 1: manual override
  const triggerCfg = config.regressionTriggers
    ? config.regressionTriggers
    : DEFAULT_REGRESSION_TRIGGER_CONFIG;

  if (config.forceFullRegression && triggerCfg.manualOverride.enabled) {
    return {
      triggered: true,
      type: "manual-override",
      reason: "Manual override — full regression explicitly requested",
    };
  }

  // Priority 2–6: file-based triggers in table order
  for (const rule of FILE_TRIGGER_RULES) {
    const ruleCfg = triggerCfg[rule.configKey];
    if (!ruleCfg.enabled) continue;

    const patterns = [...rule.defaultPatterns, ...(ruleCfg.additionalPatterns ?? [])];

    for (const entry of changedFiles) {
      if (matchesPattern(entry.path, patterns)) {
        return {
          triggered: true,
          type: rule.type,
          reason: rule.messageTemplate(entry.path),
          matchedFile: entry.path,
        };
      }
    }
  }

  return { triggered: false };
}

// ---------------------------------------------------------------------------
// evaluateLowConfidenceTrigger
// ---------------------------------------------------------------------------

/**
 * Evaluate the low-confidence trigger (type 7) against graph walk results.
 *
 * Call this AFTER the dependency graph walk, passing the partially-built result.
 * Triggers when the ratio of low-confidence affected tests meets or exceeds the
 * configured threshold (default 1.0 — all tests must be low confidence).
 *
 * Returns `{ triggered: false }` when:
 * - The trigger is disabled
 * - There are no affected tests (nothing to evaluate confidence against)
 * - The low-confidence ratio is below the threshold
 *
 * @param result - Partial impact analysis result (affectedTests must be populated)
 * @param config - Parsed ChangeImpactAnalyzer config (after schema defaults applied)
 */
export function evaluateLowConfidenceTrigger(
  result: Pick<ImpactAnalysisResult, "affectedTests" | "summary">,
  config: ParsedChangeImpactAnalyzerConfig
): RegressionTriggerEvaluation {
  const triggerCfg = config.regressionTriggers
    ? config.regressionTriggers
    : DEFAULT_REGRESSION_TRIGGER_CONFIG;

  if (!triggerCfg.lowConfidence.enabled) {
    return { triggered: false };
  }

  const total = result.affectedTests.length;
  if (total === 0) {
    return { triggered: false };
  }

  const lowCount = result.affectedTests.filter((t) => t.confidence === "low").length;

  const ratio = lowCount / total;

  if (ratio >= triggerCfg.lowConfidence.threshold) {
    return {
      triggered: true,
      type: "low-confidence",
      reason: `Impact analysis produced low-confidence results (${lowCount}/${total} tests at low confidence) — full regression required`,
    };
  }

  return { triggered: false };
}
