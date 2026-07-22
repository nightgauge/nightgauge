/**
 * Mock fixtures for Nightgauge configuration behavior tests
 *
 * Factory functions for creating test configurations with consistent defaults.
 * Used by behavior tests to verify config fields affect runtime behavior.
 *
 * @see Issue #437 - Audit and test project/issue/commands config fields
 * @module tests/mocks/config-fixtures
 */

import type {
  IncrediConfig,
  ProjectConfig,
  IssueConfig,
  CommandsConfig,
  SyncConfig,
  SprintConfig,
  CustomFieldConfig,
  ProjectEntry,
  PullRequestConfig,
  BranchConfig,
  BranchPrefixConfig,
  PipelineConfig,
  SkipChecksConfig,
  PipelineLogsConfig,
  PipelineRetryConfig,
  RoutingConfig,
  EnforcementConfig,
  DependencyEnforcementConfig,
} from "../../src/config/schema";

// ============================================================================
// Project Configuration Fixtures
// ============================================================================

/**
 * Default project configuration for tests
 */
export const DEFAULT_PROJECT_CONFIG: ProjectConfig = {
  number: 10,
  owner: "test-org",
  auto_dates: true,
  sprint: {
    enabled: false,
    auto_assign: false,
    field_name: "Sprint",
  },
  sync: {
    enabled: true,
    direction: "bidirectional",
    conflict_resolution: "warn",
    debounce_ms: 1000,
  },
};

/**
 * Create a mock project configuration with optional overrides
 *
 * @param overrides - Partial overrides for the config
 * @returns Complete project configuration
 *
 * @example
 * const config = createMockProjectConfig({ auto_dates: false });
 * // Returns: { number: 10, owner: 'test-org', auto_dates: false, ... }
 */
export function createMockProjectConfig(overrides?: Partial<ProjectConfig>): ProjectConfig {
  return {
    ...DEFAULT_PROJECT_CONFIG,
    ...overrides,
    // Deep merge nested objects
    sprint: {
      ...DEFAULT_PROJECT_CONFIG.sprint,
      ...overrides?.sprint,
    },
    sync: {
      ...DEFAULT_PROJECT_CONFIG.sync,
      ...overrides?.sync,
    },
  };
}

/**
 * Create a mock sync configuration
 *
 * @param overrides - Partial overrides for sync config
 * @returns Complete sync configuration
 */
export function createMockSyncConfig(overrides?: Partial<SyncConfig>): SyncConfig {
  return {
    enabled: true,
    direction: "bidirectional",
    conflict_resolution: "warn",
    debounce_ms: 1000,
    ...overrides,
  };
}

/**
 * Create a mock sprint configuration
 *
 * @param overrides - Partial overrides for sprint config
 * @returns Complete sprint configuration
 */
export function createMockSprintConfig(overrides?: Partial<SprintConfig>): SprintConfig {
  return {
    enabled: true,
    auto_assign: true,
    field_name: "Sprint",
    current: undefined,
    duration_weeks: 2,
    ...overrides,
  };
}

/**
 * Create a mock custom field configuration
 *
 * @param overrides - Partial overrides for custom field config
 * @returns Complete custom field configuration
 */
export function createMockCustomField(overrides?: Partial<CustomFieldConfig>): CustomFieldConfig {
  return {
    name: "Component",
    field_id: "PVTSSF_test_component",
    label_prefix: "component",
    type: "single_select",
    mappings: {
      frontend: "Frontend",
      backend: "Backend",
      api: "API",
    },
    ...overrides,
  };
}

/**
 * Create a mock project entry for multi-project mode
 *
 * @param overrides - Partial overrides for project entry
 * @returns Complete project entry configuration
 */
export function createMockProjectEntry(overrides?: Partial<ProjectEntry>): ProjectEntry {
  return {
    name: "Engineering Board",
    number: 10,
    id: "PVT_test_id",
    status_field_id: "PVTSSF_test_status",
    priority_field_id: "PVTSSF_test_priority",
    size_field_id: "PVTSSF_test_size",
    sync_filter: undefined,
    default: false,
    ...overrides,
  };
}

// ============================================================================
// Issue Configuration Fixtures
// ============================================================================

/**
 * Default issue configuration for tests
 */
export const DEFAULT_ISSUE_CONFIG: IssueConfig = {
  auto_assign: true,
  default_labels: [],
  default_status: "backlog",
};

/**
 * Create a mock issue configuration with optional overrides
 *
 * @param overrides - Partial overrides for the config
 * @returns Complete issue configuration
 *
 * @example
 * const config = createMockIssueConfig({ auto_assign: false });
 * // Returns: { auto_assign: false, default_labels: [] }
 */
export function createMockIssueConfig(overrides?: Partial<IssueConfig>): IssueConfig {
  return {
    ...DEFAULT_ISSUE_CONFIG,
    ...overrides,
  };
}

// ============================================================================
// Commands Configuration Fixtures
// ============================================================================

/**
 * Default commands configuration for tests (empty = auto-detect)
 */
export const DEFAULT_COMMANDS_CONFIG: CommandsConfig = {};

/**
 * Create a mock commands configuration with optional overrides
 *
 * @param overrides - Partial overrides for the config
 * @returns Complete commands configuration
 *
 * @example
 * const config = createMockCommandsConfig({ test: 'pnpm test' });
 * // Returns: { test: 'pnpm test' }
 */
export function createMockCommandsConfig(overrides?: Partial<CommandsConfig>): CommandsConfig {
  return {
    ...DEFAULT_COMMANDS_CONFIG,
    ...overrides,
  };
}

/**
 * Commands configuration with all fields set (for override testing)
 */
export const FULL_COMMANDS_CONFIG: CommandsConfig = {
  test: "pnpm test",
  lint: "pnpm lint",
  typecheck: "pnpm typecheck",
  format: "pnpm format",
  build: "pnpm build",
};

// ============================================================================
// Complete Nightgauge Configuration Fixtures
// ============================================================================

/**
 * Create a complete mock Nightgauge configuration
 *
 * @param overrides - Partial overrides for any section
 * @returns Complete Nightgauge configuration
 *
 * @example
 * const config = createMockIncrediConfig({
 *   project: { number: 20 },
 *   issue: { auto_assign: false }
 * });
 */
export function createMockIncrediConfig(overrides?: Partial<IncrediConfig>): IncrediConfig {
  return {
    project: overrides?.project ? createMockProjectConfig(overrides.project) : undefined,
    issue: overrides?.issue ? createMockIssueConfig(overrides.issue) : undefined,
    commands: overrides?.commands ? createMockCommandsConfig(overrides.commands) : undefined,
    ...overrides,
  };
}

// ============================================================================
// Environment Variable Helpers
// ============================================================================

/**
 * Environment variable mappings for config fields
 *
 * Maps config paths to their corresponding environment variable names.
 */
export const CONFIG_ENV_MAPPINGS = {
  "project.number": "NIGHTGAUGE_PROJECT_NUMBER",
  "project.auto_dates": "NIGHTGAUGE_PROJECT_AUTO_DATES",
  "issue.auto_assign": "NIGHTGAUGE_ISSUE_AUTO_ASSIGN",
  "issue.default_status": "NIGHTGAUGE_ISSUE_DEFAULT_STATUS",
  "commands.test": "NIGHTGAUGE_COMMANDS_TEST",
  "commands.build": "NIGHTGAUGE_COMMANDS_BUILD",
  "commands.lint": "NIGHTGAUGE_COMMANDS_LINT",
  "commands.typecheck": "NIGHTGAUGE_COMMANDS_TYPECHECK",
  "commands.format": "NIGHTGAUGE_COMMANDS_FORMAT",
} as const;

export type ConfigEnvMapping = typeof CONFIG_ENV_MAPPINGS;

/**
 * Create environment variable overrides object
 *
 * @param overrides - Map of config paths to values
 * @returns Object with environment variable names and values
 *
 * @example
 * const env = createEnvOverrides({
 *   'issue.auto_assign': 'false',
 *   'commands.test': 'npm test'
 * });
 * // Returns: { NIGHTGAUGE_ISSUE_AUTO_ASSIGN: 'false', NIGHTGAUGE_COMMANDS_TEST: 'npm test' }
 */
export function createEnvOverrides(
  overrides: Partial<Record<keyof ConfigEnvMapping, string>>
): Record<string, string> {
  const env: Record<string, string> = {};
  for (const [configPath, value] of Object.entries(overrides)) {
    const envVar = CONFIG_ENV_MAPPINGS[configPath as keyof ConfigEnvMapping];
    if (envVar) {
      env[envVar] = value;
    }
  }
  return env;
}

/**
 * Apply environment overrides for testing
 *
 * Returns a cleanup function to restore original environment.
 *
 * @param overrides - Environment variable overrides
 * @returns Cleanup function to restore original environment
 *
 * @example
 * const cleanup = applyEnvOverrides({ NIGHTGAUGE_ISSUE_AUTO_ASSIGN: 'false' });
 * // ... run tests ...
 * cleanup(); // Restore original environment
 */
export function applyEnvOverrides(overrides: Record<string, string>): () => void {
  const original: Record<string, string | undefined> = {};

  for (const [key, value] of Object.entries(overrides)) {
    original[key] = process.env[key];
    process.env[key] = value;
  }

  return () => {
    for (const key of Object.keys(overrides)) {
      if (original[key] === undefined) {
        delete process.env[key];
      } else {
        process.env[key] = original[key];
      }
    }
  };
}

// ============================================================================
// Config YAML String Fixtures
// ============================================================================

/**
 * Generate a minimal config.yaml content
 */
export function generateMinimalConfigYaml(projectNumber: number): string {
  return `project:
  number: ${projectNumber}
`;
}

/**
 * Generate a full config.yaml content with all sections
 */
export function generateFullConfigYaml(config: Partial<IncrediConfig>): string {
  const lines: string[] = [];

  if (config.project) {
    lines.push("project:");
    if (config.project.number !== undefined) {
      lines.push(`  number: ${config.project.number}`);
    }
    if (config.project.owner !== undefined) {
      lines.push(`  owner: ${config.project.owner}`);
    }
    if (config.project.auto_dates !== undefined) {
      lines.push(`  auto_dates: ${config.project.auto_dates}`);
    }
  }

  if (config.issue) {
    lines.push("issue:");
    if (config.issue.auto_assign !== undefined) {
      lines.push(`  auto_assign: ${config.issue.auto_assign}`);
    }
    if (config.issue.default_labels && config.issue.default_labels.length > 0) {
      lines.push("  default_labels:");
      for (const label of config.issue.default_labels) {
        lines.push(`    - ${label}`);
      }
    }
  }

  if (config.commands) {
    lines.push("commands:");
    if (config.commands.test) {
      lines.push(`  test: ${config.commands.test}`);
    }
    if (config.commands.build) {
      lines.push(`  build: ${config.commands.build}`);
    }
    if (config.commands.lint) {
      lines.push(`  lint: ${config.commands.lint}`);
    }
    if (config.commands.typecheck) {
      lines.push(`  typecheck: ${config.commands.typecheck}`);
    }
    if (config.commands.format) {
      lines.push(`  format: ${config.commands.format}`);
    }
  }

  return lines.join("\n") + "\n";
}

// ============================================================================
// Pull Request Configuration Fixtures
// ============================================================================

/**
 * Default pull request configuration for tests
 */
export const DEFAULT_PR_CONFIG: PullRequestConfig = {
  merge_strategy: "squash",
  delete_branch: true,
  draft_by_default: false,
  reviewers: [],
  auto_merge: true,
  auto_fix_ci: true,
  auto_fix_max_attempts: 2,
  ci_check_timeout: 600,
};

/**
 * Create a mock pull request configuration with optional overrides
 *
 * @param overrides - Partial overrides for the config
 * @returns Complete pull request configuration
 *
 * @example
 * const config = createMockPRConfig({ delete_branch: false });
 * // Returns: { delete_branch: false, merge_strategy: 'squash', ... }
 */
export function createMockPRConfig(overrides?: Partial<PullRequestConfig>): PullRequestConfig {
  return {
    ...DEFAULT_PR_CONFIG,
    ...overrides,
  };
}

// ============================================================================
// Branch Configuration Fixtures
// ============================================================================

/**
 * Default branch prefix configuration for tests
 */
export const DEFAULT_BRANCH_PREFIXES: BranchPrefixConfig = {
  feature: "feat/",
  bugfix: "fix/",
  hotfix: "hotfix/",
  release: "release/",
  docs: "docs/",
  refactor: "refactor/",
  chore: "chore/",
  test: "test/",
};

/**
 * Default branch configuration for tests
 */
export const DEFAULT_BRANCH_CONFIG: BranchConfig = {
  base: "main",
  protected: ["main", "master"],
  suggestions: true,
  prefixes: DEFAULT_BRANCH_PREFIXES,
};

/**
 * Create a mock branch configuration with optional overrides
 *
 * @param overrides - Partial overrides for the config
 * @returns Complete branch configuration
 *
 * @example
 * const config = createMockBranchConfig({ base: 'develop' });
 * // Returns: { base: 'develop', protected: ['main', 'master'], ... }
 */
export function createMockBranchConfig(overrides?: Partial<BranchConfig>): BranchConfig {
  return {
    ...DEFAULT_BRANCH_CONFIG,
    ...overrides,
    // Deep merge prefixes
    prefixes: {
      ...DEFAULT_BRANCH_PREFIXES,
      ...overrides?.prefixes,
    },
  };
}

/**
 * Create a mock branch prefix configuration
 *
 * @param overrides - Partial overrides for prefixes
 * @returns Complete branch prefix configuration
 */
export function createMockBranchPrefixes(
  overrides?: Partial<BranchPrefixConfig>
): BranchPrefixConfig {
  return {
    ...DEFAULT_BRANCH_PREFIXES,
    ...overrides,
  };
}

// ============================================================================
// Pipeline Configuration Fixtures
// ============================================================================

/**
 * Default skip checks configuration for tests
 */
export const DEFAULT_SKIP_CHECKS: SkipChecksConfig = {
  tests: false,
  lint: false,
  typecheck: false,
  build: false,
  format: false,
};

/**
 * Default pipeline logs configuration for tests
 */
export const DEFAULT_PIPELINE_LOGS: PipelineLogsConfig = {
  retain: true,
  dir: ".nightgauge/logs",
  max_age_days: 30,
  max_count: 100,
};

/**
 * Default pipeline retry configuration for tests
 */
export const DEFAULT_PIPELINE_RETRY: PipelineRetryConfig = {
  max_auto_attempts: 3,
  backoff_multiplier: 2,
  initial_delay_ms: 1000,
  retryable_api_errors: [500, 502, 503, 504],
  rate_limit_delay_ms: 60000,
};

/**
 * Default pipeline configuration for tests
 */
export const DEFAULT_PIPELINE_CONFIG: PipelineConfig = {
  ci_timeout: 300,
  auto_fix: true,
  skip: DEFAULT_SKIP_CHECKS,
  logs: DEFAULT_PIPELINE_LOGS,
  retry: DEFAULT_PIPELINE_RETRY,
};

/**
 * Create a mock pipeline configuration with optional overrides
 *
 * @param overrides - Partial overrides for the config
 * @returns Complete pipeline configuration
 *
 * @example
 * const config = createMockPipelineConfig({ auto_fix: false });
 * // Returns: { ci_timeout: 300, auto_fix: false, ... }
 */
export function createMockPipelineConfig(overrides?: Partial<PipelineConfig>): PipelineConfig {
  return {
    ...DEFAULT_PIPELINE_CONFIG,
    ...overrides,
    // Deep merge nested objects
    skip: {
      ...DEFAULT_SKIP_CHECKS,
      ...overrides?.skip,
    },
    logs: {
      ...DEFAULT_PIPELINE_LOGS,
      ...overrides?.logs,
    },
    retry: {
      ...DEFAULT_PIPELINE_RETRY,
      ...overrides?.retry,
    },
  };
}

/**
 * Create a mock skip checks configuration
 *
 * @param overrides - Partial overrides for skip checks
 * @returns Complete skip checks configuration
 */
export function createMockSkipChecks(overrides?: Partial<SkipChecksConfig>): SkipChecksConfig {
  return {
    ...DEFAULT_SKIP_CHECKS,
    ...overrides,
  };
}

/**
 * Create a mock pipeline retry configuration
 *
 * @param overrides - Partial overrides for retry config
 * @returns Complete pipeline retry configuration
 */
export function createMockPipelineRetry(
  overrides?: Partial<PipelineRetryConfig>
): PipelineRetryConfig {
  return {
    ...DEFAULT_PIPELINE_RETRY,
    ...overrides,
  };
}

// ============================================================================
// Routing Configuration Fixtures
// ============================================================================

/**
 * Default routing configuration for tests
 */
export const DEFAULT_ROUTING_CONFIG: RoutingConfig = {
  trivial_max_complexity: 2,
  extensive_min_complexity: 5,
  force_full_pipeline: false,
};

/**
 * Create a mock routing configuration with optional overrides
 *
 * @param overrides - Partial overrides for the config
 * @returns Complete routing configuration
 *
 * @example
 * const config = createMockRoutingConfig({ force_full_pipeline: true });
 * // Returns: { trivial_max_complexity: 2, extensive_min_complexity: 5, force_full_pipeline: true }
 */
export function createMockRoutingConfig(overrides?: Partial<RoutingConfig>): RoutingConfig {
  return {
    ...DEFAULT_ROUTING_CONFIG,
    ...overrides,
  };
}

// ============================================================================
// Enforcement Configuration Fixtures
// ============================================================================

/**
 * Default dependency enforcement configuration for tests
 */
export const DEFAULT_DEPENDENCY_ENFORCEMENT: DependencyEnforcementConfig = {
  enabled: true,
  mode: "warn",
  check_transitive: false,
};

/**
 * Default enforcement configuration for tests
 */
export const DEFAULT_ENFORCEMENT_CONFIG: EnforcementConfig = {
  dependencies: DEFAULT_DEPENDENCY_ENFORCEMENT,
};

/**
 * Create a mock enforcement configuration with optional overrides
 *
 * @param overrides - Partial overrides for the config
 * @returns Complete enforcement configuration
 *
 * @example
 * const config = createMockEnforcementConfig({ dependencies: { mode: 'block' } });
 */
export function createMockEnforcementConfig(
  overrides?: Partial<EnforcementConfig>
): EnforcementConfig {
  return {
    ...DEFAULT_ENFORCEMENT_CONFIG,
    ...overrides,
    // Deep merge dependencies
    dependencies: {
      ...DEFAULT_DEPENDENCY_ENFORCEMENT,
      ...overrides?.dependencies,
    },
  };
}

/**
 * Create a mock dependency enforcement configuration
 *
 * @param overrides - Partial overrides for dependency enforcement
 * @returns Complete dependency enforcement configuration
 */
export function createMockDependencyEnforcement(
  overrides?: Partial<DependencyEnforcementConfig>
): DependencyEnforcementConfig {
  return {
    ...DEFAULT_DEPENDENCY_ENFORCEMENT,
    ...overrides,
  };
}

// ============================================================================
// Extended Environment Variable Mappings
// ============================================================================

/**
 * Extended environment variable mappings for all config fields
 *
 * Maps config paths to their corresponding environment variable names.
 */
export const EXTENDED_CONFIG_ENV_MAPPINGS = {
  // Existing mappings
  ...CONFIG_ENV_MAPPINGS,

  // PR config
  "pr.merge_strategy": "NIGHTGAUGE_PR_MERGE_STRATEGY",
  "pr.delete_branch": "NIGHTGAUGE_PR_DELETE_BRANCH",
  "pr.draft_by_default": "NIGHTGAUGE_PR_DRAFT_BY_DEFAULT",
  "pr.auto_merge": "NIGHTGAUGE_PR_AUTO_MERGE",
  "pr.auto_fix_ci": "NIGHTGAUGE_PR_AUTO_FIX_CI",
  "pr.auto_fix_max_attempts": "NIGHTGAUGE_PR_AUTO_FIX_MAX_ATTEMPTS",
  "pr.ci_check_timeout": "NIGHTGAUGE_PR_CI_CHECK_TIMEOUT",

  // Branch config
  "branch.base": "NIGHTGAUGE_BRANCH_BASE",
  "branch.suggestions": "NIGHTGAUGE_BRANCH_SUGGESTIONS",

  // Pipeline config
  "pipeline.ci_timeout": "NIGHTGAUGE_PIPELINE_CI_TIMEOUT",
  "pipeline.auto_fix": "NIGHTGAUGE_PIPELINE_AUTO_FIX",
  "pipeline.skip.tests": "NIGHTGAUGE_PIPELINE_SKIP_TESTS",
  "pipeline.skip.lint": "NIGHTGAUGE_PIPELINE_SKIP_LINT",
  "pipeline.skip.typecheck": "NIGHTGAUGE_PIPELINE_SKIP_TYPECHECK",
  "pipeline.skip.build": "NIGHTGAUGE_PIPELINE_SKIP_BUILD",
  "pipeline.skip.format": "NIGHTGAUGE_PIPELINE_SKIP_FORMAT",
  "pipeline.retry.max_auto_attempts": "NIGHTGAUGE_PIPELINE_RETRY_MAX_ATTEMPTS",
  "pipeline.retry.initial_delay_ms": "NIGHTGAUGE_PIPELINE_RETRY_INITIAL_DELAY",
  "pipeline.logs.retain": "NIGHTGAUGE_PIPELINE_LOGS_RETAIN",
  "pipeline.logs.dir": "NIGHTGAUGE_PIPELINE_LOGS_DIR",

  // Routing config
  "routing.trivial_max_complexity": "NIGHTGAUGE_ROUTING_TRIVIAL_MAX",
  "routing.extensive_min_complexity": "NIGHTGAUGE_ROUTING_EXTENSIVE_MIN",
  "routing.force_full_pipeline": "NIGHTGAUGE_ROUTING_FORCE_FULL_PIPELINE",

  // Enforcement config
  "enforcement.dependencies.enabled": "NIGHTGAUGE_ENFORCEMENT_DEPS_ENABLED",
  "enforcement.dependencies.mode": "NIGHTGAUGE_ENFORCEMENT_DEPS_MODE",
  "enforcement.dependencies.check_transitive": "NIGHTGAUGE_ENFORCEMENT_DEPS_TRANSITIVE",
} as const;

export type ExtendedConfigEnvMapping = typeof EXTENDED_CONFIG_ENV_MAPPINGS;

// ============================================================================
// Batch Configuration Fixtures
// ============================================================================

/**
 * Default batch resource limits for tests
 */
export const DEFAULT_BATCH_RESOURCE_LIMITS: import("../../src/config/schema").BatchResourceLimits =
  {
    token_budget: 0,
    cost_budget: 0,
    time_budget: 0,
  };

/**
 * Default batch history configuration for tests
 */
export const DEFAULT_BATCH_HISTORY: import("../../src/config/schema").BatchHistoryConfig = {
  save_history: true,
  history_limit: 50,
};

/**
 * Default batch configuration for tests
 */
export const DEFAULT_BATCH_CONFIG: import("../../src/config/schema").BatchConfig = {
  max_issues: 50,
  pause_between_issues: false,
  concurrency: 1,
  stop_on_error: false,
  retry_failed_issues: false,
  max_retries: 1,
  show_summary: true,
  notify_on_complete: true,
  notify_on_each_issue: false,
  show_progress_estimate: true,
  resource_limits: DEFAULT_BATCH_RESOURCE_LIMITS,
  history: DEFAULT_BATCH_HISTORY,
};

/**
 * Create a mock batch configuration with optional overrides
 *
 * @param overrides - Partial overrides for the config
 * @returns Complete batch configuration
 *
 * @example
 * const config = createMockBatchConfig({ max_issues: 5 });
 */
export function createMockBatchConfig(
  overrides?: Partial<import("../../src/config/schema").BatchConfig>
): import("../../src/config/schema").BatchConfig {
  return {
    ...DEFAULT_BATCH_CONFIG,
    ...overrides,
    // Deep merge nested objects
    resource_limits: {
      ...DEFAULT_BATCH_RESOURCE_LIMITS,
      ...overrides?.resource_limits,
    },
    history: {
      ...DEFAULT_BATCH_HISTORY,
      ...overrides?.history,
    },
  };
}

/**
 * Create a mock batch resource limits configuration
 */
export function createMockBatchResourceLimits(
  overrides?: Partial<import("../../src/config/schema").BatchResourceLimits>
): import("../../src/config/schema").BatchResourceLimits {
  return {
    ...DEFAULT_BATCH_RESOURCE_LIMITS,
    ...overrides,
  };
}

// ============================================================================
// Validation Configuration Fixtures
// ============================================================================

/**
 * Default validation configuration for tests
 */
export const DEFAULT_VALIDATION_CONFIG: import("../../src/config/schema").ValidationConfig = {
  require_tests: true,
  require_changelog: false,
  max_files_changed: 50,
  max_lines_changed: 2000,
};

/**
 * Create a mock validation configuration with optional overrides
 *
 * @param overrides - Partial overrides for the config
 * @returns Complete validation configuration
 *
 * @example
 * const config = createMockValidationConfig({ require_tests: false });
 */
export function createMockValidationConfig(
  overrides?: Partial<import("../../src/config/schema").ValidationConfig>
): import("../../src/config/schema").ValidationConfig {
  return {
    ...DEFAULT_VALIDATION_CONFIG,
    ...overrides,
  };
}

// ============================================================================
// Sanitization Configuration Fixtures
// ============================================================================

/**
 * Default sanitization configuration for tests
 */
export const DEFAULT_SANITIZATION_CONFIG: import("../../src/config/schema").SanitizationConfig = {
  enabled: true,
  sanitize_input: false,
  logging: true,
  mode: "warn",
  warn_only: false,
  allowlist: [],
  blocklist: [],
  safe_directories: [
    "./dist",
    "./build",
    "./node_modules",
    "./.next",
    "./coverage",
    "./out",
    "./.cache",
  ],
};

/**
 * Create a mock sanitization configuration with optional overrides
 *
 * @param overrides - Partial overrides for the config
 * @returns Complete sanitization configuration
 *
 * @example
 * const config = createMockSanitizationConfig({ warn_only: true });
 */
export function createMockSanitizationConfig(
  overrides?: Partial<import("../../src/config/schema").SanitizationConfig>
): import("../../src/config/schema").SanitizationConfig {
  return {
    ...DEFAULT_SANITIZATION_CONFIG,
    ...overrides,
  };
}

// ============================================================================
// Human-in-the-Loop Configuration Fixtures
// ============================================================================

/**
 * Default human-in-the-loop configuration for tests
 */
export const DEFAULT_HITL_CONFIG: import("../../src/config/schema").HumanInTheLoopConfig = {
  auto_accept_stages: true,
  auto_accept_permissions: false,
  trusted_stages: [],
};

/**
 * Create a mock human-in-the-loop configuration with optional overrides
 *
 * @param overrides - Partial overrides for the config
 * @returns Complete HITL configuration
 *
 * @example
 * const config = createMockHITLConfig({ auto_accept_stages: true });
 */
export function createMockHITLConfig(
  overrides?: Partial<import("../../src/config/schema").HumanInTheLoopConfig>
): import("../../src/config/schema").HumanInTheLoopConfig {
  return {
    ...DEFAULT_HITL_CONFIG,
    ...overrides,
  };
}

// ============================================================================
// Ralph Loop Configuration Fixtures
// ============================================================================

/**
 * Default ralph loop limits for tests
 */
export const DEFAULT_RALPH_LOOP_LIMITS: import("../../src/config/schema").RalphLoopLimits = {
  max_iterations: 5,
  token_budget_per_iteration: 50000,
  total_token_budget: 200000,
  iteration_timeout_ms: 300000,
  total_timeout_ms: 1800000,
};

/**
 * Default ralph loop configuration for tests
 */
export const DEFAULT_RALPH_LOOP_CONFIG: import("../../src/config/schema").RalphLoopConfig = {
  enabled: true,
  build: true,
  tests: true,
  lint: false,
  limits: DEFAULT_RALPH_LOOP_LIMITS,
  abort_patterns: [],
};

/**
 * Create a mock ralph loop configuration with optional overrides
 *
 * @param overrides - Partial overrides for the config
 * @returns Complete ralph loop configuration
 *
 * @example
 * const config = createMockRalphLoopConfig({ enabled: false });
 */
export function createMockRalphLoopConfig(
  overrides?: Partial<import("../../src/config/schema").RalphLoopConfig>
): import("../../src/config/schema").RalphLoopConfig {
  return {
    ...DEFAULT_RALPH_LOOP_CONFIG,
    ...overrides,
    // Deep merge limits
    limits: {
      ...DEFAULT_RALPH_LOOP_LIMITS,
      ...overrides?.limits,
    },
  };
}

/**
 * Create a mock ralph loop limits configuration
 */
export function createMockRalphLoopLimits(
  overrides?: Partial<import("../../src/config/schema").RalphLoopLimits>
): import("../../src/config/schema").RalphLoopLimits {
  return {
    ...DEFAULT_RALPH_LOOP_LIMITS,
    ...overrides,
  };
}

// ============================================================================
// Automations Configuration Fixtures
// ============================================================================

/**
 * Default automation action for tests
 */
export const DEFAULT_AUTOMATION_ACTION: import("../../src/config/schema").AutomationAction = {
  type: "notify",
  message: "Test notification",
};

/**
 * Default automation trigger for tests
 */
export const DEFAULT_AUTOMATION_TRIGGER: import("../../src/config/schema").AutomationTrigger = {
  name: "test-trigger",
  trigger: "pr-merged",
  actions: [DEFAULT_AUTOMATION_ACTION],
};

/**
 * Default automations configuration for tests
 */
export const DEFAULT_AUTOMATIONS_CONFIG: import("../../src/config/schema").AutomationsConfig = {
  enabled: true,
  dry_run: false,
  log_file: ".nightgauge/automations.log",
  triggers: [],
};

/**
 * Create a mock automations configuration with optional overrides
 *
 * @param overrides - Partial overrides for the config
 * @returns Complete automations configuration
 *
 * @example
 * const config = createMockAutomationsConfig({ dry_run: true });
 */
export function createMockAutomationsConfig(
  overrides?: Partial<import("../../src/config/schema").AutomationsConfig>
): import("../../src/config/schema").AutomationsConfig {
  return {
    ...DEFAULT_AUTOMATIONS_CONFIG,
    ...overrides,
  };
}

/**
 * Create a mock automation trigger
 */
export function createMockAutomationTrigger(
  overrides?: Partial<import("../../src/config/schema").AutomationTrigger>
): import("../../src/config/schema").AutomationTrigger {
  return {
    ...DEFAULT_AUTOMATION_TRIGGER,
    ...overrides,
    // Merge actions if provided
    actions: overrides?.actions || [DEFAULT_AUTOMATION_ACTION],
  };
}

/**
 * Create a mock automation action
 */
export function createMockAutomationAction(
  overrides?: Partial<import("../../src/config/schema").AutomationAction>
): import("../../src/config/schema").AutomationAction {
  return {
    ...DEFAULT_AUTOMATION_ACTION,
    ...overrides,
  };
}

// ============================================================================
// Extended Environment Variable Mappings for New Sections
// ============================================================================

/**
 * Additional environment variable mappings for new config fields
 */
export const BEHAVIOR_CONFIG_ENV_MAPPINGS = {
  // Batch config
  "batch.max_issues": "NIGHTGAUGE_BATCH_MAX_ISSUES",
  "batch.pause_between_issues": "NIGHTGAUGE_BATCH_PAUSE_BETWEEN",
  "batch.concurrency": "NIGHTGAUGE_BATCH_CONCURRENCY",
  "batch.stop_on_error": "NIGHTGAUGE_BATCH_STOP_ON_ERROR",
  "batch.retry_failed_issues": "NIGHTGAUGE_BATCH_RETRY_FAILED",
  "batch.max_retries": "NIGHTGAUGE_BATCH_MAX_RETRIES",
  "batch.show_summary": "NIGHTGAUGE_BATCH_SHOW_SUMMARY",
  "batch.notify_on_complete": "NIGHTGAUGE_BATCH_NOTIFY_COMPLETE",
  "batch.resource_limits.token_budget": "NIGHTGAUGE_BATCH_TOKEN_BUDGET",
  "batch.resource_limits.cost_budget": "NIGHTGAUGE_BATCH_COST_BUDGET",
  "batch.resource_limits.time_budget": "NIGHTGAUGE_BATCH_TIME_BUDGET",

  // Validation config
  "validation.require_tests": "NIGHTGAUGE_VALIDATION_REQUIRE_TESTS",
  "validation.require_changelog": "NIGHTGAUGE_VALIDATION_REQUIRE_CHANGELOG",
  "validation.max_files_changed": "NIGHTGAUGE_VALIDATION_MAX_FILES",
  "validation.max_lines_changed": "NIGHTGAUGE_VALIDATION_MAX_LINES",

  // Sanitization config
  "sanitization.enabled": "NIGHTGAUGE_SANITIZATION_ENABLED",
  "sanitization.sanitize_input": "NIGHTGAUGE_SANITIZATION_INPUT",
  "sanitization.logging": "NIGHTGAUGE_SANITIZATION_LOGGING",
  "sanitization.warn_only": "NIGHTGAUGE_SANITIZATION_WARN_ONLY",

  // Human-in-the-loop config
  "human_in_the_loop.auto_accept_stages": "NIGHTGAUGE_HITL_AUTO_ACCEPT_STAGES",
  "human_in_the_loop.auto_accept_permissions": "NIGHTGAUGE_HITL_AUTO_ACCEPT_PERMISSIONS",
  // Note: NIGHTGAUGE_AUTO_APPROVE is the canonical env var for auto-accept
  "human_in_the_loop.trusted_stages": "NIGHTGAUGE_HITL_TRUSTED_STAGES",

  // Ralph loop config
  "ralph_loop.enabled": "NIGHTGAUGE_RALPH_LOOP_ENABLED",
  "ralph_loop.build": "NIGHTGAUGE_RALPH_LOOP_BUILD",
  "ralph_loop.tests": "NIGHTGAUGE_RALPH_LOOP_TESTS",
  "ralph_loop.lint": "NIGHTGAUGE_RALPH_LOOP_LINT",
  "ralph_loop.limits.max_iterations": "NIGHTGAUGE_RALPH_LOOP_MAX_ITERATIONS",
  "ralph_loop.limits.token_budget_per_iteration": "NIGHTGAUGE_RALPH_LOOP_TOKEN_BUDGET_ITER",
  "ralph_loop.limits.total_token_budget": "NIGHTGAUGE_RALPH_LOOP_TOTAL_TOKENS",
  "ralph_loop.limits.iteration_timeout_ms": "NIGHTGAUGE_RALPH_LOOP_ITER_TIMEOUT",
  "ralph_loop.limits.total_timeout_ms": "NIGHTGAUGE_RALPH_LOOP_TOTAL_TIMEOUT",

  // Automations config
  "automations.enabled": "NIGHTGAUGE_AUTOMATIONS_ENABLED",
  "automations.dry_run": "NIGHTGAUGE_AUTOMATIONS_DRY_RUN",
  "automations.log_file": "NIGHTGAUGE_AUTOMATIONS_LOG_FILE",
} as const;

export type BehaviorConfigEnvMapping = typeof BEHAVIOR_CONFIG_ENV_MAPPINGS;

// ============================================================================
// UI Configuration Fixtures
// ============================================================================

/**
 * Default UI core configuration for tests
 */
export const DEFAULT_UI_CORE_CONFIG = {
  adapter: "claude" as const,
  auth_provider: "max" as const,
  default_model: "sonnet" as const,
  context_path: ".nightgauge/pipeline",
  plans_path: ".nightgauge/plans",
};

/**
 * Create a mock UI core configuration with optional overrides
 */
export function createMockUICoreConfig(overrides?: Partial<typeof DEFAULT_UI_CORE_CONFIG>) {
  return {
    ...DEFAULT_UI_CORE_CONFIG,
    ...overrides,
  };
}

/**
 * Default UI dashboard time savings configuration for tests
 */
export const DEFAULT_UI_TIME_SAVINGS_CONFIG = {
  issue_pickup: 5,
  feature_planning: 30,
  feature_dev: 120,
  pr_create: 10,
  pr_merge: 5,
};

/**
 * Default UI output window configuration for tests
 */
export const DEFAULT_UI_OUTPUT_WINDOW_CONFIG = {
  auto_open: true,
  auto_scroll: true,
  verbose_level: "normal" as const,
  show_token_usage: true,
  word_wrap: true,
};

/**
 * Default UI notification sounds configuration for tests
 */
export const DEFAULT_UI_NOTIFICATION_SOUNDS_CONFIG = {
  enabled: true,
  alert: "Glass" as const,
  success: "Hero" as const,
  error: "Basso" as const,
  volume: 0.5,
};

/**
 * Default UI notifications configuration for tests
 */
export const DEFAULT_UI_NOTIFICATIONS_CONFIG = {
  enabled: true,
  sounds: DEFAULT_UI_NOTIFICATION_SOUNDS_CONFIG,
  banner_enabled: true,
  dock_bounce_enabled: true,
  respect_do_not_disturb: true,
};

/**
 * Default UI ready items filters configuration for tests
 */
export const DEFAULT_UI_READY_ITEMS_FILTERS_CONFIG = {
  priority: "all" as const,
  size: "all" as const,
  component: "all",
};

/**
 * Default UI ready items configuration for tests
 */
export const DEFAULT_UI_READY_ITEMS_CONFIG = {
  auto_refresh: false,
  refresh_interval: 300,
  sort_by: "smart" as const,
  sort_direction: "asc" as const,
  filters: DEFAULT_UI_READY_ITEMS_FILTERS_CONFIG,
  search_text: "",
  show_dependencies: true,
};

/**
 * UI environment variable mappings
 */
export const UI_CONFIG_ENV_MAPPINGS = {
  // Core
  "ui.core.adapter": "NIGHTGAUGE_UI_CORE_ADAPTER",
  "ui.core.auth_provider": "NIGHTGAUGE_UI_CORE_AUTH_PROVIDER",
  "ui.core.default_model": "NIGHTGAUGE_UI_CORE_DEFAULT_MODEL",
  "ui.core.context_path": "NIGHTGAUGE_UI_CORE_CONTEXT_PATH",
  "ui.core.plans_path": "NIGHTGAUGE_UI_CORE_PLANS_PATH",
  // Dashboard
  "ui.dashboard.time_savings.issue_pickup": "NIGHTGAUGE_UI_DASHBOARD_TIME_SAVINGS_ISSUE_PICKUP",
  "ui.dashboard.time_savings.feature_planning":
    "NIGHTGAUGE_UI_DASHBOARD_TIME_SAVINGS_FEATURE_PLANNING",
  "ui.dashboard.time_savings.feature_dev": "NIGHTGAUGE_UI_DASHBOARD_TIME_SAVINGS_FEATURE_DEV",
  "ui.dashboard.time_savings.pr_create": "NIGHTGAUGE_UI_DASHBOARD_TIME_SAVINGS_PR_CREATE",
  "ui.dashboard.time_savings.pr_merge": "NIGHTGAUGE_UI_DASHBOARD_TIME_SAVINGS_PR_MERGE",
  // Output Window
  "ui.output_window.auto_open": "NIGHTGAUGE_UI_OUTPUT_WINDOW_AUTO_OPEN",
  "ui.output_window.auto_scroll": "NIGHTGAUGE_UI_OUTPUT_WINDOW_AUTO_SCROLL",
  "ui.output_window.verbose_level": "NIGHTGAUGE_UI_OUTPUT_WINDOW_VERBOSE_LEVEL",
  "ui.output_window.show_token_usage": "NIGHTGAUGE_UI_OUTPUT_WINDOW_SHOW_TOKEN_USAGE",
  "ui.output_window.word_wrap": "NIGHTGAUGE_UI_OUTPUT_WINDOW_WORD_WRAP",
  // Notifications
  "ui.notifications.enabled": "NIGHTGAUGE_UI_NOTIFICATIONS_ENABLED",
  "ui.notifications.sounds.enabled": "NIGHTGAUGE_UI_NOTIFICATIONS_SOUNDS_ENABLED",
  "ui.notifications.sounds.alert": "NIGHTGAUGE_UI_NOTIFICATIONS_SOUNDS_ALERT",
  "ui.notifications.sounds.success": "NIGHTGAUGE_UI_NOTIFICATIONS_SOUNDS_SUCCESS",
  "ui.notifications.sounds.error": "NIGHTGAUGE_UI_NOTIFICATIONS_SOUNDS_ERROR",
  "ui.notifications.sounds.volume": "NIGHTGAUGE_UI_NOTIFICATIONS_SOUNDS_VOLUME",
  "ui.notifications.banner_enabled": "NIGHTGAUGE_UI_NOTIFICATIONS_BANNER_ENABLED",
  "ui.notifications.dock_bounce_enabled": "NIGHTGAUGE_UI_NOTIFICATIONS_DOCK_BOUNCE_ENABLED",
  "ui.notifications.respect_do_not_disturb": "NIGHTGAUGE_UI_NOTIFICATIONS_RESPECT_DO_NOT_DISTURB",
  // Ready Items
  "ui.ready_items.auto_refresh": "NIGHTGAUGE_UI_READY_ITEMS_AUTO_REFRESH",
  "ui.ready_items.refresh_interval": "NIGHTGAUGE_UI_READY_ITEMS_REFRESH_INTERVAL",
  "ui.ready_items.sort_by": "NIGHTGAUGE_UI_READY_ITEMS_SORT_BY",
  "ui.ready_items.sort_direction": "NIGHTGAUGE_UI_READY_ITEMS_SORT_DIRECTION",
  "ui.ready_items.filters.priority": "NIGHTGAUGE_UI_READY_ITEMS_FILTERS_PRIORITY",
  "ui.ready_items.filters.size": "NIGHTGAUGE_UI_READY_ITEMS_FILTERS_SIZE",
  "ui.ready_items.filters.component": "NIGHTGAUGE_UI_READY_ITEMS_FILTERS_COMPONENT",
  "ui.ready_items.search_text": "NIGHTGAUGE_UI_READY_ITEMS_SEARCH_TEXT",
  "ui.ready_items.show_dependencies": "NIGHTGAUGE_UI_READY_ITEMS_SHOW_DEPENDENCIES",
  // Sidebar
  "ui.sidebar.hide_empty_sections": "NIGHTGAUGE_UI_SIDEBAR_HIDE_EMPTY_SECTIONS",
  // Pipeline
  "ui.pipeline.auto_continue": "NIGHTGAUGE_UI_PIPELINE_AUTO_CONTINUE",
  "ui.pipeline.auto_continue_delay": "NIGHTGAUGE_UI_PIPELINE_AUTO_CONTINUE_DELAY",
  // Project Board
  "ui.project_board.group_by_epic": "NIGHTGAUGE_UI_PROJECT_BOARD_GROUP_BY_EPIC",
  "ui.project_board.default_epic_collapsed": "NIGHTGAUGE_UI_PROJECT_BOARD_DEFAULT_EPIC_COLLAPSED",
  // Warnings
  "ui.warnings.enabled": "NIGHTGAUGE_UI_WARNINGS_ENABLED",
  "ui.warnings.warn_on_in_progress": "NIGHTGAUGE_UI_WARNINGS_WARN_ON_IN_PROGRESS",
  "ui.warnings.warn_on_in_review": "NIGHTGAUGE_UI_WARNINGS_WARN_ON_IN_REVIEW",
  // Plugins
  "ui.plugins.auto_prompt": "NIGHTGAUGE_UI_PLUGINS_AUTO_PROMPT",
  "ui.plugins.marketplace_url": "NIGHTGAUGE_UI_PLUGINS_MARKETPLACE_URL",
} as const;

export type UIConfigEnvMapping = typeof UI_CONFIG_ENV_MAPPINGS;
