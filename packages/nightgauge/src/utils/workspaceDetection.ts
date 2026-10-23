/**
 * Workspace Detection Utility
 *
 * Detects whether Nightgauge is running in a multi-repository workspace and loads
 * workspace configuration from .vscode/nightgauge-workspace.yaml.
 *
 * Detection Priority:
 * 1. Explicit: .vscode/nightgauge-workspace.yaml exists → multi-workspace
 * 2. Auto-detect: Multiple VSCode workspace folders with .nightgauge/config.yaml → multi-workspace
 * 3. Fallback: Single repository mode (existing behavior)
 *
 * @see docs/CONFIGURATION.md for workspace configuration schema
 */

import * as vscode from "vscode";
import * as fs from "fs/promises";
import * as path from "path";
import * as yaml from "yaml";
import type {
  WorkspaceConfig,
  WorkspaceDetectionResult,
  ValidationResult,
  ValidationError,
} from "../types/WorkspaceConfig";
import { CONFIG_DIR, CONFIG_FILE_NAME, LEGACY_CONFIG_FILE_NAME } from "./configPathResolver";

const WORKSPACE_CONFIG_FILE = ".vscode/nightgauge-workspace.yaml";
/** Primary config file path (new) */
const NIGHTGAUGE_CONFIG_FILE = `${CONFIG_DIR}/${CONFIG_FILE_NAME}`;
/** Legacy config file path (deprecated) */
const NIGHTGAUGE_LEGACY_CONFIG_FILE = `${CONFIG_DIR}/${LEGACY_CONFIG_FILE_NAME}`;

/**
 * Detect workspace type and load configuration
 *
 * @param workspaceRoot - The workspace root path (typically from getWorkspaceRoot())
 * @returns Workspace detection result with type, config, and detection method
 */
export async function detectWorkspaceType(
  workspaceRoot: string
): Promise<WorkspaceDetectionResult> {
  try {
    // Priority 1: Check for explicit workspace configuration
    const explicitConfig = await loadWorkspaceConfig(workspaceRoot);
    if (explicitConfig) {
      return {
        type: "multi-workspace",
        config: explicitConfig,
        detection_method: "explicit",
      };
    }

    // Priority 2: Auto-detect multi-workspace from VSCode workspace folders
    const autoDetected = await autoDetectMultiWorkspace();
    if (autoDetected) {
      return {
        type: "multi-workspace",
        config: null, // No explicit config, auto-detected
        detection_method: "auto-detected",
      };
    }

    // Priority 3: Fallback to single repository mode
    return {
      type: "single",
      config: null,
      detection_method: "single-repo",
    };
  } catch (error) {
    console.error(
      `[Nightgauge] Workspace detection error: ${error instanceof Error ? error.message : String(error)}`
    );
    // Safe fallback to single-repo mode on errors
    return {
      type: "single",
      config: null,
      detection_method: "single-repo",
    };
  }
}

/**
 * Load and validate workspace configuration from .vscode/nightgauge-workspace.yaml
 *
 * @param workspaceRoot - The workspace root path
 * @returns Parsed and validated workspace config, or null if file doesn't exist
 * @throws Error if file exists but contains invalid YAML or fails validation
 */
export async function loadWorkspaceConfig(workspaceRoot: string): Promise<WorkspaceConfig | null> {
  const configPath = path.join(workspaceRoot, WORKSPACE_CONFIG_FILE);

  try {
    // Check if file exists
    await fs.access(configPath);
  } catch {
    // File doesn't exist - not an error, just no explicit config
    return null;
  }

  try {
    // Read and parse YAML
    const fileContent = await fs.readFile(configPath, "utf-8");
    const parsed = yaml.parse(fileContent);

    // Validate configuration
    const validationResult = validateWorkspaceConfig(parsed);
    if (!validationResult.valid) {
      const errorMessages = validationResult.errors
        .map((e) => `  - ${e.path}: ${e.message}`)
        .join("\n");
      throw new Error(
        `Invalid workspace configuration in ${WORKSPACE_CONFIG_FILE}:\n${errorMessages}`
      );
    }

    return parsed as WorkspaceConfig;
  } catch (error) {
    if (error instanceof yaml.YAMLParseError) {
      throw new Error(`Failed to parse ${WORKSPACE_CONFIG_FILE}: ${error.message}`, {
        cause: error,
      });
    }
    throw error; // Re-throw validation errors and other errors
  }
}

/**
 * Auto-detect multi-workspace from VSCode workspace folders
 *
 * Returns true if multiple workspace folders exist and all contain nightgauge config
 * (either .nightgauge/config.yaml or legacy .nightgauge/nightgauge.yaml)
 *
 * @returns True if multi-workspace detected, false otherwise
 */
async function autoDetectMultiWorkspace(): Promise<boolean> {
  const workspaceFolders = vscode.workspace.workspaceFolders;

  // Need at least 2 folders for multi-workspace
  if (!workspaceFolders || workspaceFolders.length < 2) {
    return false;
  }

  // Check if all folders contain nightgauge config (primary or legacy)
  const checks = workspaceFolders.map(async (folder) => {
    const primaryPath = path.join(folder.uri.fsPath, NIGHTGAUGE_CONFIG_FILE);
    const legacyPath = path.join(folder.uri.fsPath, NIGHTGAUGE_LEGACY_CONFIG_FILE);
    try {
      await fs.access(primaryPath);
      return true;
    } catch {
      // Try legacy path
      try {
        await fs.access(legacyPath);
        return true;
      } catch {
        return false;
      }
    }
  });

  const results = await Promise.all(checks);
  return results.every((hasConfig) => hasConfig);
}

/**
 * Validate workspace configuration object
 *
 * @param config - Configuration object to validate (unknown type for safety)
 * @returns Validation result with all accumulated errors
 */
export function validateWorkspaceConfig(config: unknown): ValidationResult {
  const errors: ValidationError[] = [];

  // Type guard: must be an object
  if (typeof config !== "object" || config === null) {
    return {
      valid: false,
      errors: [{ path: "$", message: "Configuration must be an object" }],
    };
  }

  const cfg = config as Record<string, unknown>;

  // Validate workspace section (required)
  if (!cfg.workspace) {
    errors.push({
      path: "workspace",
      message: 'Required field "workspace" is missing',
    });
  } else if (typeof cfg.workspace !== "object" || cfg.workspace === null) {
    errors.push({
      path: "workspace",
      message: 'Field "workspace" must be an object',
    });
  } else {
    const workspace = cfg.workspace as Record<string, unknown>;

    // Validate workspace.name (required)
    if (!workspace.name) {
      errors.push({
        path: "workspace.name",
        message: 'Required field "workspace.name" is missing',
      });
    } else if (typeof workspace.name !== "string") {
      errors.push({
        path: "workspace.name",
        message: 'Field "workspace.name" must be a string',
      });
    } else if (workspace.name.trim() === "") {
      errors.push({
        path: "workspace.name",
        message: 'Field "workspace.name" cannot be empty',
      });
    }

    // Validate workspace.description (optional)
    if (workspace.description !== undefined && typeof workspace.description !== "string") {
      errors.push({
        path: "workspace.description",
        message: 'Field "workspace.description" must be a string',
      });
    }

    // Validate workspace.shared_project_number (optional)
    if (workspace.shared_project_number !== undefined) {
      if (
        typeof workspace.shared_project_number !== "number" ||
        !Number.isInteger(workspace.shared_project_number) ||
        workspace.shared_project_number <= 0
      ) {
        errors.push({
          path: "workspace.shared_project_number",
          message: 'Field "workspace.shared_project_number" must be a positive integer',
        });
      }
    }
  }

  // Determine if shared_project_number is set (allows empty repositories list).
  const hasSharedProjectNumber =
    typeof cfg.workspace === "object" &&
    cfg.workspace !== null &&
    typeof (cfg.workspace as Record<string, unknown>).shared_project_number === "number";

  // Validate repositories array (required)
  if (!cfg.repositories) {
    errors.push({
      path: "repositories",
      message: 'Required field "repositories" is missing',
    });
  } else if (!Array.isArray(cfg.repositories)) {
    errors.push({
      path: "repositories",
      message: 'Field "repositories" must be an array',
    });
  } else if (cfg.repositories.length === 0 && !hasSharedProjectNumber) {
    // Allow empty repositories when shared_project_number is set — the list
    // will be derived from the GitHub project at runtime (N:1 topology).
    errors.push({
      path: "repositories",
      message:
        'Field "repositories" cannot be empty (or set workspace.shared_project_number for N:1 auto-derivation)',
    });
  } else {
    // Validate each repository object
    const repositories = cfg.repositories as unknown[];
    const seenNames = new Set<string>();

    repositories.forEach((repo, index) => {
      if (typeof repo !== "object" || repo === null) {
        errors.push({
          path: `repositories[${index}]`,
          message: "Each repository must be an object",
        });
        return;
      }

      const r = repo as Record<string, unknown>;

      // Validate name (required)
      if (!r.name) {
        errors.push({
          path: `repositories[${index}].name`,
          message: 'Required field "name" is missing',
        });
      } else if (typeof r.name !== "string") {
        errors.push({
          path: `repositories[${index}].name`,
          message: 'Field "name" must be a string',
        });
      } else {
        // Check for duplicate names
        if (seenNames.has(r.name)) {
          errors.push({
            path: `repositories[${index}].name`,
            message: `Duplicate repository name: "${r.name}"`,
          });
        }
        seenNames.add(r.name);
      }

      // Validate path (required)
      if (!r.path) {
        errors.push({
          path: `repositories[${index}].path`,
          message: 'Required field "path" is missing',
        });
      } else if (typeof r.path !== "string") {
        errors.push({
          path: `repositories[${index}].path`,
          message: 'Field "path" must be a string',
        });
      }

      // Validate role (optional)
      if (r.role !== undefined) {
        const validRoles = ["primary", "secondary", "shared"];
        if (!validRoles.includes(r.role as string)) {
          errors.push({
            path: `repositories[${index}].role`,
            message: `Field "role" must be one of: ${validRoles.join(", ")}`,
          });
        }
      }

      // Validate project_number (optional)
      if (r.project_number !== undefined) {
        if (
          typeof r.project_number !== "number" ||
          !Number.isInteger(r.project_number) ||
          r.project_number <= 0
        ) {
          errors.push({
            path: `repositories[${index}].project_number`,
            message: 'Field "project_number" must be a positive integer',
          });
        }
      }
    });
  }

  // Validate routing section (optional)
  if (cfg.routing !== undefined) {
    if (typeof cfg.routing !== "object" || cfg.routing === null) {
      errors.push({
        path: "routing",
        message: 'Field "routing" must be an object',
      });
    } else {
      const routing = cfg.routing as Record<string, unknown>;

      // Validate routing.patterns (optional)
      if (routing.patterns !== undefined) {
        if (typeof routing.patterns !== "object" || routing.patterns === null) {
          errors.push({
            path: "routing.patterns",
            message: 'Field "routing.patterns" must be an object',
          });
        }
      }

      // Validate routing.default_repository (optional)
      if (
        routing.default_repository !== undefined &&
        typeof routing.default_repository !== "string"
      ) {
        errors.push({
          path: "routing.default_repository",
          message: 'Field "routing.default_repository" must be a string',
        });
      }
    }
  }

  // Validate epic section (optional)
  if (cfg.epic !== undefined) {
    if (typeof cfg.epic !== "object" || cfg.epic === null) {
      errors.push({
        path: "epic",
        message: 'Field "epic" must be an object',
      });
    } else {
      const epic = cfg.epic as Record<string, unknown>;

      // Validate epic.cross_repo_tracking (optional)
      if (epic.cross_repo_tracking !== undefined && typeof epic.cross_repo_tracking !== "boolean") {
        errors.push({
          path: "epic.cross_repo_tracking",
          message: 'Field "epic.cross_repo_tracking" must be a boolean',
        });
      }

      // Validate epic.shared_milestones (optional)
      if (epic.shared_milestones !== undefined && typeof epic.shared_milestones !== "boolean") {
        errors.push({
          path: "epic.shared_milestones",
          message: 'Field "epic.shared_milestones" must be a boolean',
        });
      }
    }
  }

  // Validate knowledge section (optional)
  if (cfg.knowledge !== undefined) {
    if (typeof cfg.knowledge !== "object" || cfg.knowledge === null) {
      errors.push({
        path: "knowledge",
        message: 'Field "knowledge" must be an object',
      });
    } else {
      const knowledge = cfg.knowledge as Record<string, unknown>;

      // Validate knowledge.workspace_root (optional)
      if (knowledge.workspace_root !== undefined && typeof knowledge.workspace_root !== "string") {
        errors.push({
          path: "knowledge.workspace_root",
          message: 'Field "knowledge.workspace_root" must be a string',
        });
      }

      // Validate knowledge.aggregate (optional)
      if (knowledge.aggregate !== undefined && typeof knowledge.aggregate !== "boolean") {
        errors.push({
          path: "knowledge.aggregate",
          message: 'Field "knowledge.aggregate" must be a boolean',
        });
      }

      // Validate knowledge.cross_repo_links (optional)
      if (
        knowledge.cross_repo_links !== undefined &&
        typeof knowledge.cross_repo_links !== "boolean"
      ) {
        errors.push({
          path: "knowledge.cross_repo_links",
          message: 'Field "knowledge.cross_repo_links" must be a boolean',
        });
      }
    }
  }

  return {
    valid: errors.length === 0,
    errors,
  };
}

/**
 * Check if workspace is multi-workspace (convenience function)
 *
 * @param workspaceRoot - The workspace root path
 * @returns True if multi-workspace detected, false otherwise
 */
export async function isMultiWorkspace(workspaceRoot: string): Promise<boolean> {
  const result = await detectWorkspaceType(workspaceRoot);
  return result.type === "multi-workspace";
}
