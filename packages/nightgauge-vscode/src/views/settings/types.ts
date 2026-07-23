/**
 * Settings Types - TypeScript interfaces for .nightgauge/config.yaml configuration
 *
 * Types are now derived from Zod schemas in config/schema.ts.
 * This file re-exports types for backward compatibility.
 *
 * @see docs/CONFIGURATION.md for full schema documentation
 * @see config/schema.ts for the Zod schema (source of truth)
 * @see Issue #432 - Comprehensive Zod Schema for Config Fields
 * @see Issue #440 - Multi-tier config GUI support
 */

// ============================================================================
// Re-export types from Zod schema (single source of truth)
// ============================================================================

export type {
  // Enums
  MergeStrategy,
  SyncDirection,
  ConflictResolution,
  EnforcementMode,
  CustomFieldType,
  TrustedStage,

  // Project configuration
  ProjectFieldsConfig,
  SprintConfig,
  SyncConfig,
  CustomFieldConfig,
  ProjectConfig,
  ProjectEntry,

  // PR configuration
  PullRequestConfig,

  // Branch configuration
  BranchPrefixConfig,
  BranchConfig,

  // Issue configuration
  IssueConfig,

  // Pipeline configuration
  SkipChecksConfig,
  PipelineLogsConfig,
  PipelineRetryConfig,
  PipelineConfig,

  // Routing configuration
  RoutingConfig,

  // Enforcement configuration
  DependencyEnforcementConfig,
  EnforcementConfig,

  // Commands configuration
  CommandsConfig,

  // Validation configuration
  ValidationConfig,

  // Sanitization configuration
  SanitizationConfig,

  // Human-in-the-loop configuration
  HumanInTheLoopConfig,

  // Ralph Loop configuration
  RalphLoopLimits,
  RalphLoopConfig,

  // Automations configuration
  AutomationActionType,
  AutomationAction,
  AutomationTrigger,
  AutomationsConfig,

  // Root configuration
  IncrediConfig,
} from "../../config/schema";

// Re-export validation types
export type {
  ConfigValidationError,
  ConfigValidationResult,
  ConfigSource,
  ConfigSourceMap,
} from "../../config/schema";

// Re-export merge engine types for tier-aware UI
export type { ConfigMergeResult, ConfigTiers, TierMetadata } from "../../config/configMergeEngine";

export { SOURCE_LABELS, SOURCE_COLORS } from "../../config/configMergeEngine";

// Re-export runtime tier types (Issue #3335)
export type {
  RuntimeStateStore,
  RuntimeChangeEvent,
  RuntimeKeyOptions,
  RuntimeScope,
} from "../../config/RuntimeStateStore";

// ============================================================================
// Deprecated type aliases for backward compatibility
// ============================================================================

// ============================================================================
// Default configuration
// ============================================================================

import { getDefaultConfig } from "../../config/schema";

/**
 * Default configuration values
 *
 * Used when creating a new .nightgauge/config.yaml or resetting to defaults.
 * Now derived from Zod schema defaults.
 */
export const DEFAULT_CONFIG = getDefaultConfig();

// ============================================================================
// UI-specific types (not derived from Zod schema)
// ============================================================================

/**
 * Section metadata for UI display
 */
export interface SettingsSectionMeta {
  id: string;
  title: string;
  icon: string;
  description: string;
  docLink?: string;
}

/**
 * All settings sections with metadata
 */
/**
 * Section IDs locked during pipeline execution.
 *
 * These sections directly control running pipeline behavior — changing them
 * mid-run would cause inconsistencies. All other sections remain editable.
 *
 * @see Issue #921 - Per-section lock during pipeline execution
 */
export const PIPELINE_LOCKED_SECTIONS: readonly string[] = [
  "core",
  "pipeline",
  "commands",
  "routing",
] as const;

export const SETTINGS_SECTIONS: SettingsSectionMeta[] = [
  {
    id: "core",
    title: "Core",
    icon: "server-environment",
    description: "Execution adapter, auth provider, and core pipeline paths",
    docLink: "docs/CONFIGURATION.md#uicore",
  },
  {
    id: "platform",
    title: "Platform",
    icon: "key",
    description: "License key, API connection, and platform feature settings",
    docLink: "docs/CONFIGURATION.md#platform-configuration",
  },
  {
    id: "project",
    title: "Project Board",
    icon: "project",
    description: "GitHub Project board integration settings",
    docLink: "docs/CONFIGURATION.md#project",
  },
  {
    id: "pull_request",
    title: "Pull Request",
    icon: "git-pull-request",
    description: "PR creation and merge defaults",
    docLink: "docs/CONFIGURATION.md#pr",
  },
  {
    id: "branch",
    title: "Branch",
    icon: "git-branch",
    description: "Branch naming and protection rules",
    docLink: "docs/CONFIGURATION.md#branch",
  },
  {
    id: "issue",
    title: "Issue",
    icon: "issues",
    description: "Issue pickup and assignment settings",
    docLink: "docs/CONFIGURATION.md#issue",
  },
  {
    id: "pipeline",
    title: "Pipeline",
    icon: "play",
    description: "Pipeline execution and automation settings",
    docLink: "docs/CONFIGURATION.md#pipeline",
  },
  {
    id: "routing",
    title: "Routing",
    icon: "git-compare",
    description: "Complexity-based stage routing settings",
    docLink: "docs/CONFIGURATION.md#routing",
  },
  {
    id: "enforcement",
    title: "Enforcement",
    icon: "lock",
    description: "Dependency enforcement and quality gates",
    docLink: "docs/CONFIGURATION.md#enforcement",
  },
  {
    id: "commands",
    title: "Commands",
    icon: "terminal",
    description: "Custom command overrides",
    docLink: "docs/CONFIGURATION.md#commands",
  },
  {
    id: "validation",
    title: "Validation",
    icon: "check",
    description: "Pre-PR validation rules",
    docLink: "docs/CONFIGURATION.md#validation",
  },
  {
    id: "sanitization",
    title: "Sanitization",
    icon: "shield",
    description: "Prompt injection protection",
    docLink: "docs/SECURITY.md",
  },
  {
    id: "human_in_the_loop",
    title: "Human-in-the-Loop",
    icon: "zap",
    description: "Auto-accept pipeline prompts and permissions",
    docLink: "docs/CONFIGURATION.md#human_in_the_loop",
  },
  {
    id: "ralph_loop",
    title: "Ralph Loop",
    icon: "sync",
    description: "Self-healing build and test configuration",
    docLink: "docs/RALPH_LOOP.md",
  },
  {
    id: "automations",
    title: "Automations",
    icon: "workflow",
    description: "Workflow automation triggers and actions",
    docLink: "docs/AUTOMATIONS.md",
  },
  {
    id: "autonomous",
    title: "Autonomous",
    icon: "robot",
    description: "Autonomous scheduler, issue refinement, and auto-actionable settings",
    docLink: "docs/AUTONOMOUS_ORCHESTRATOR.md",
  },
  {
    id: "forges",
    title: "Forge Instances",
    icon: "server",
    description: "Manage GitLab and GitHub forge connections",
    docLink: "docs/CONFIGURATION.md#forge-configuration-schema_version-2",
  },
];

// ============================================================================
// Legacy validation types (for backward compatibility)
// ============================================================================

/**
 * Validation error for a specific field
 * @deprecated Use ConfigValidationError from config/schema.ts
 */
export interface ValidationError {
  field: string;
  message: string;
}

/**
 * Result of config validation
 * @deprecated Use ConfigValidationResult from config/schema.ts
 */
export interface ValidationResult {
  valid: boolean;
  errors: ValidationError[];
}

// ============================================================================
// Multi-Tier UI Types (Issue #440)
// ============================================================================

/**
 * View tier for settings panel
 *
 * - merged: Shows effective config with source badges
 * - default: Shows built-in defaults (read-only)
 * - global: Shows global config (~/.nightgauge/config.yaml) - read-only in project GUI
 * - project: Shows project config (.nightgauge/config.yaml) - editable
 * - local: Shows local config (.nightgauge/config.local.yaml) - editable
 * - env: Shows environment variable overrides - read-only
 */
export type ViewTier = "merged" | "default" | "global" | "project" | "local" | "env";

/**
 * Editing target tier (subset of ViewTier that can be edited).
 * "global" maps to the machine tier (~/.nightgauge/config.yaml) and is
 * editable from the Global tab so machine-tier keys (e.g. the platform license
 * key) can be saved through the Settings UI (#3997).
 */
export type EditableTier = "project" | "local" | "global";

/**
 * Tier tab configuration for UI
 */
export interface TierTabConfig {
  id: ViewTier;
  label: string;
  icon: string;
  description: string;
  editable: boolean;
  /** File path hint for tooltip (undefined for merged/default/env) */
  filePath?: string;
}

/**
 * All tier tabs for the settings panel
 */
export const TIER_TABS: TierTabConfig[] = [
  {
    id: "merged",
    label: "Merged",
    icon: "layers",
    description: "Effective configuration after all tiers are merged",
    editable: false,
  },
  {
    id: "project",
    label: "Project",
    icon: "folder",
    description: "Project-specific settings (.nightgauge/config.yaml)",
    editable: true,
    filePath: ".nightgauge/config.yaml",
  },
  {
    id: "local",
    label: "Local",
    icon: "person",
    description: "Developer overrides (.nightgauge/config.local.yaml) - gitignored",
    editable: true,
    filePath: ".nightgauge/config.local.yaml",
  },
  {
    id: "global",
    label: "Global",
    icon: "home",
    description:
      "Machine-tier settings (~/.nightgauge/config.yaml) - applies to all your workspaces",
    editable: true,
    filePath: "~/.nightgauge/config.yaml",
  },
  {
    id: "env",
    label: "Environment",
    icon: "terminal",
    description: "Environment variable overrides (NIGHTGAUGE_*) - read-only",
    editable: false,
  },
];

/**
 * Setting with tier source information
 */
export interface TieredSettingValue {
  /** The effective value after merging */
  value: unknown;
  /** Source tier where this value came from */
  source: ViewTier | "cli";
  /** Environment variable name if source is 'env' */
  envVarName?: string;
  /** Whether this value differs from the default */
  isModified: boolean;
}

/**
 * State for the settings panel tier view
 */
export interface TierViewState {
  /** Currently selected tier tab */
  currentTier: ViewTier;
  /** Default tier for editing when viewing merged */
  defaultEditTier: EditableTier;
  /** Whether global config file exists */
  hasGlobalConfig: boolean;
  /** Whether local config file exists */
  hasLocalConfig: boolean;
  /** Whether project config file exists */
  hasProjectConfig: boolean;
  /** Environment variables that are set */
  activeEnvVars: string[];
}
