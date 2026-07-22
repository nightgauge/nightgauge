/**
 * Workspace configuration schema for multi-repository Nightgauge workflows
 *
 * This module defines the TypeScript interfaces for workspace configuration,
 * which enables Nightgauge to coordinate operations across multiple related repositories.
 *
 * Configuration file: .vscode/nightgauge-workspace.yaml
 *
 * @see docs/CONFIGURATION.md for complete schema documentation
 */

/**
 * Repository definition within a workspace
 */
export interface WorkspaceRepository {
  /** Repository name (must be unique within workspace) */
  name: string;

  /** Relative path from workspace root to repository */
  path: string;

  /** Optional role classification for routing decisions */
  role?: "primary" | "secondary" | "shared";

  /**
   * Optional GitHub project number for this repository.
   * When all repositories share the same project number the Repositories
   * view appends `· Project #N` to its title. Also used for N:1 topology
   * where multiple repos feed one shared project board.
   */
  project_number?: number;
}

/**
 * Routing pattern for epic decomposition
 *
 * Defines keyword-to-repository mappings for suggesting where
 * child issues should be created when decomposing an epic.
 *
 * @see Issue #325 - AI-Powered Epic Decomposition
 */
export interface RoutingPattern {
  /** Unique identifier for the pattern */
  id: string;

  /** Keywords that trigger this pattern (case-insensitive) */
  keywords: string[];

  /** Repository to assign when pattern matches */
  preferred_repo: string;

  /** Optional: Minimum confidence threshold (0-1, default 0.3) */
  min_confidence?: number;

  /** Optional: Description for display in preview */
  description?: string;
}

/**
 * Routing configuration for cross-repository workflows
 */
export interface WorkspaceRoutingConfig {
  /** Keyword-based routing patterns for epic decomposition */
  patterns?: RoutingPattern[];

  /** Default repository when no pattern matches */
  default_repository?: string;

  /** Enable AI fallback for items that don't match patterns (default: true) */
  ai_fallback?: boolean;
}

/**
 * Knowledge aggregation configuration for multi-repository workspaces
 */
export interface WorkspaceKnowledgeConfig {
  /** Root directory for aggregated knowledge files, relative to workspace root (default: .nightgauge/knowledge/) */
  workspace_root?: string;

  /** Aggregate knowledge files from all repositories into workspace_root (default: true) */
  aggregate?: boolean;

  /** Resolve and follow wiki-links across repositories (default: true) */
  cross_repo_links?: boolean;
}

/**
 * Epic tracking configuration for cross-repository features
 */
export interface WorkspaceEpicConfig {
  /** Enable tracking epics across multiple repositories */
  cross_repo_tracking?: boolean;

  /** Share milestone tracking across repositories */
  shared_milestones?: boolean;
}

/**
 * Complete workspace configuration schema
 */
export interface WorkspaceConfig {
  /** Workspace metadata */
  workspace: {
    /** Workspace display name */
    name: string;

    /** Optional workspace description */
    description?: string;

    /**
     * Shared GitHub project number for N:1 topology (multiple repos → one project).
     * When set and `repositories` is empty, the extension derives the repo list
     * from `ProjectV2.repositories` via the Go binary `workspace repos-from-project`.
     * When set alongside an explicit `repositories` list the project number is used
     * for the view title but the explicit list takes precedence.
     */
    shared_project_number?: number;
  };

  /** Array of repositories in this workspace */
  repositories: WorkspaceRepository[];

  /** Optional routing configuration */
  routing?: WorkspaceRoutingConfig;

  /** Optional epic tracking configuration */
  epic?: WorkspaceEpicConfig;

  /** Optional knowledge aggregation configuration */
  knowledge?: WorkspaceKnowledgeConfig;
}

/**
 * Workspace detection result
 */
export interface WorkspaceDetectionResult {
  /** Type of workspace detected */
  type: "single" | "multi-workspace";

  /** Loaded configuration (null for single-repo mode) */
  config: WorkspaceConfig | null;

  /** Method used for detection */
  detection_method: "explicit" | "auto-detected" | "single-repo";
}

/**
 * Validation result with structured errors
 */
export interface ValidationResult {
  /** Whether validation passed */
  valid: boolean;

  /** Array of validation errors (empty if valid) */
  errors: ValidationError[];
}

/**
 * Validation error details
 */
export interface ValidationError {
  /** JSON path to the invalid field */
  path: string;

  /** Human-readable error message */
  message: string;
}

/**
 * Default workspace configuration values
 */
export const DEFAULT_WORKSPACE_CONFIG: Partial<WorkspaceConfig> = {
  routing: {
    patterns: [],
    default_repository: undefined,
    ai_fallback: true,
  },
  epic: {
    cross_repo_tracking: false,
    shared_milestones: false,
  },
  knowledge: {
    workspace_root: ".nightgauge/knowledge/",
    aggregate: true,
    cross_repo_links: true,
  },
};
