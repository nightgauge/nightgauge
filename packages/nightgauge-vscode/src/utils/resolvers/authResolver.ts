/**
 * Auth Resolver - Auth provider and GitHub authentication config reading
 *
 * Extracted from incrediConfig.ts as part of the config module decomposition.
 * Provides utilities for reading auth provider and GitHub auth settings from
 * the nightgauge config file.
 *
 * @see Issue #2742 - Refactor VSCode: extract incrediConfig.ts
 */

import * as fs from "node:fs";
import * as path from "node:path";
import * as vscode from "vscode";
import { resolveConfigPathSync, logDeprecationWarning } from "../configPathResolver";

/**
 * Auth provider type for Claude API backend
 *
 * - 'max': Default Claude Max (Anthropic API)
 * - 'bedrock': AWS Bedrock
 * - 'vertex': Google Vertex AI
 *
 * @see Issue #511 - Add Bedrock and Vertex backend support
 */
export type AuthProvider = "max" | "bedrock" | "vertex";

/**
 * Default auth provider (uses Claude Max/Anthropic API)
 */
export const DEFAULT_AUTH_PROVIDER: AuthProvider = "max";

/**
 * Get the auth provider from config or environment.
 *
 * Priority:
 * 1. Environment variable: NIGHTGAUGE_UI_CORE_AUTH_PROVIDER
 * 2. Config file: ui.core.auth_provider
 * 3. Default: 'max'
 *
 * @param workspaceRoot - Workspace root path (optional, auto-detected if not provided)
 * @returns The auth provider ('max', 'bedrock', or 'vertex')
 *
 * @see Issue #511 - Add Bedrock and Vertex backend support
 * @see docs/CONFIGURATION.md - UI Core configuration
 */
export function getAuthProvider(workspaceRoot?: string): AuthProvider {
  // Check environment variable first
  const envProvider = process.env.NIGHTGAUGE_UI_CORE_AUTH_PROVIDER;
  if (envProvider === "max" || envProvider === "bedrock" || envProvider === "vertex") {
    return envProvider;
  }

  // Get workspace root
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) {
    return DEFAULT_AUTH_PROVIDER;
  }

  try {
    // Resolve config path with fallback to legacy
    const pathResult = resolveConfigPathSync(root);
    if (!pathResult.exists) {
      return DEFAULT_AUTH_PROVIDER;
    }

    // Log deprecation warning if using legacy path
    if (pathResult.isLegacy) {
      logDeprecationWarning(pathResult.path);
    }

    // Read and parse config file (simple line parsing)
    const configContent = fs.readFileSync(pathResult.path, "utf-8");
    const lines = configContent.split("\n");
    let inUi = false;
    let inCore = false;

    for (const line of lines) {
      const trimmed = line.trim();

      // Detect ui: section
      if (trimmed === "ui:") {
        inUi = true;
        continue;
      }

      // Detect core: subsection under ui
      if (inUi && trimmed === "core:") {
        inCore = true;
        continue;
      }

      // Exit sections on new top-level key
      if (trimmed && !trimmed.startsWith("#") && /^[a-z_]+:/.test(trimmed)) {
        if (!line.startsWith(" ")) {
          inUi = false;
          inCore = false;
        } else if (line.match(/^ {2}[a-z_]+:/)) {
          // New ui subsection (not core)
          inCore = false;
        }
      }

      // Parse auth_provider value
      if (inCore) {
        const match = trimmed.match(/^auth_provider:\s*['"]?(max|bedrock|vertex)['"]?(?:\s+#.*)?$/);
        if (match) {
          return match[1] as AuthProvider;
        }
      }
    }

    return DEFAULT_AUTH_PROVIDER;
  } catch (error) {
    console.error("Failed to read auth provider from nightgauge config:", error);
    return DEFAULT_AUTH_PROVIDER;
  }
}

/**
 * Get the GitHub user for a workspace from per-repo config.
 *
 * Reads `github_user` from `.nightgauge/config.yaml` for the given workspace.
 * Also checks `github_auth.users[owner]` in both workspace and global configs.
 *
 * @param workspaceRoot - Workspace root path
 * @returns The GitHub username to use, or null if not configured
 * @see Issue #2487 - Per-repository GitHub user auth
 */
export function getGitHubUser(workspaceRoot?: string): string | null {
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) return null;

  const os = require("os") as typeof import("os");
  const configPaths: string[] = [];

  // Local (.nightgauge/config.local.yaml) first — highest precedence and
  // the gitignored, secret-free place for a per-repo github_user — then the
  // committed workspace config, then the machine-global config. Setting
  // github_user per repo is the recommended per-repo identity knob: the token
  // is resolved via `gh auth token --user <github_user>`, so concurrent
  // workspaces owned by different GitHub users each authenticate as their own
  // keyring account with no PAT stored on disk.
  const localConfig = path.join(root, ".nightgauge", "config.local.yaml");
  if (fs.existsSync(localConfig)) {
    configPaths.push(localConfig);
  }
  const pathResult = resolveConfigPathSync(root);
  if (pathResult.exists) {
    configPaths.push(pathResult.path);
  }
  const globalConfig = path.join(os.homedir(), ".nightgauge", "config.yaml");
  if (fs.existsSync(globalConfig)) {
    configPaths.push(globalConfig);
  }

  let githubUser: string | null = null;
  let owner: string | null = null;
  const authUsers: Record<string, string> = {};

  for (const configPath of configPaths) {
    try {
      const content = fs.readFileSync(configPath, "utf-8");
      const lines = content.split("\n");
      let inGithubAuth = false;
      let inUsers = false;

      for (const line of lines) {
        const trimmed = line.trim();

        // Top-level github_user from any repo-scoped config (local or project,
        // never the machine-global config). config.local.yaml wins over the
        // committed project config because it appears first in configPaths and
        // we keep the first match.
        if (trimmed.startsWith("github_user:") && configPath !== globalConfig) {
          const val = trimmed.replace("github_user:", "").trim();
          if (val && !githubUser) githubUser = val;
        }

        // Capture owner for github_auth fallback
        if (trimmed.startsWith("owner:") && !owner) {
          const val = trimmed.replace("owner:", "").trim();
          if (val) owner = val;
        }

        // github_auth section
        if (trimmed === "github_auth:") {
          inGithubAuth = true;
          continue;
        }
        if (inGithubAuth && trimmed === "users:") {
          inUsers = true;
          continue;
        }
        if (inUsers) {
          // Exit users section when line is not indented (top-level key)
          // or is empty. Entries under users: are indented 4+ spaces.
          if (trimmed === "" || /^\S/.test(line)) {
            inUsers = false;
            inGithubAuth = false;
            continue;
          }
          const match = trimmed.match(/^(\S+):\s*(.+)$/);
          if (match) {
            authUsers[match[1]] = match[2].trim();
          }
        }
      }
    } catch {
      // Config read failure is non-fatal
    }
  }

  // Priority: explicit github_user > github_auth.users[owner]
  if (githubUser) return githubUser;
  if (owner && authUsers[owner]) return authUsers[owner];
  return null;
}

/**
 * Expand `env:VAR_NAME` syntax in a config token value.
 *
 * When a token value starts with `env:`, the remainder is treated as an
 * environment variable name. The value is resolved from `process.env` at
 * read time. Returns null if the env var is not set or is empty.
 *
 * Examples:
 *   "env:MY_PAT"     → process.env.MY_PAT (or null if not set)
 *   "ghp_abc123"     → "ghp_abc123" (returned as-is)
 *
 * @param value - Raw config token value to expand
 * @returns Expanded value, or null if expansion fails
 *
 * @see Issue #2670 - Config-based token resolution
 */
export function expandEnvVar(value: string): string | null {
  if (!value.startsWith("env:")) return value;
  const varName = value.slice(4).trim();
  if (!varName) return null;
  const expanded = process.env[varName];
  return expanded || null;
}

/**
 * Read the project-level GitHub auth token from config.yaml.
 *
 * Reads `github_auth.token` from workspace config first, then global config.
 * Supports `env:VAR_NAME` expansion — the value is resolved from process.env
 * at read time.
 *
 * Example config.yaml:
 * ```yaml
 * github_auth:
 *   token: ghp_abc123       # literal PAT
 *   # or:
 *   token: env:MY_PAT       # resolved from process.env.MY_PAT
 * ```
 *
 * @param workspaceRoot - Workspace root path (optional, auto-detected)
 * @returns Resolved token string, or null if not configured
 *
 * @see Issue #2670 - Config-based token resolution
 * @see Issue #2663 - Per-project and per-org GitHub token config
 */
export function getGitHubAuthToken(workspaceRoot?: string): string | null {
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) return null;

  const os = require("os") as typeof import("os");
  const configPaths: string[] = [];

  // Tier 4 (highest precedence): local developer overrides at
  // .nightgauge/config.local.yaml — gitignored (see ensureGitignore.ts),
  // the secret-safe place for a per-repo GitHub token. Checked BEFORE the
  // committed project config.yaml and the machine config so a per-repo token
  // wins. This is what lets concurrent sessions/workspaces owned by different
  // GitHub users share one machine, each gh call using that repo's own token.
  const localConfig = path.join(root, ".nightgauge", "config.local.yaml");
  if (fs.existsSync(localConfig)) {
    configPaths.push(localConfig);
  }

  const pathResult = resolveConfigPathSync(root);
  if (pathResult.exists) {
    configPaths.push(pathResult.path);
  }
  const globalConfig = path.join(os.homedir(), ".nightgauge", "config.yaml");
  if (fs.existsSync(globalConfig)) {
    configPaths.push(globalConfig);
  }

  for (const configPath of configPaths) {
    try {
      const content = fs.readFileSync(configPath, "utf-8");
      const lines = content.split("\n");
      let inGithubAuth = false;

      for (const line of lines) {
        const trimmed = line.trim();

        if (trimmed === "github_auth:") {
          inGithubAuth = true;
          continue;
        }

        // Exit github_auth section on next top-level key (not indented)
        if (inGithubAuth && trimmed && !trimmed.startsWith("#") && /^\S/.test(line)) {
          if (
            !trimmed.startsWith("token:") &&
            !trimmed.startsWith("tokens:") &&
            !trimmed.startsWith("users:")
          ) {
            inGithubAuth = false;
            continue;
          }
        }

        if (inGithubAuth && trimmed.startsWith("token:")) {
          const rawValue = trimmed.replace("token:", "").trim();
          if (rawValue) {
            return expandEnvVar(rawValue);
          }
        }
      }
    } catch {
      // Config read failure is non-fatal
    }
  }

  return null;
}

/**
 * Read the per-org GitHub auth token mapping from config.yaml.
 *
 * Reads `github_auth.tokens` from workspace config first, then global config.
 * Supports `env:VAR_NAME` expansion in token values.
 *
 * Example config.yaml:
 * ```yaml
 * github_auth:
 *   tokens:
 *     acme: ghp_abc123        # literal PAT for org "acme"
 *     myorg: env:MYORG_PAT    # resolved from process.env.MYORG_PAT
 * ```
 *
 * @param workspaceRoot - Workspace root path (optional, auto-detected)
 * @returns Record mapping org/owner names to resolved tokens (empty if none configured)
 *
 * @see Issue #2670 - Config-based token resolution
 * @see Issue #2663 - Per-project and per-org GitHub token config
 */
export function getGitHubAuthTokens(workspaceRoot?: string): Record<string, string> {
  const root = workspaceRoot ?? vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
  if (!root) return {};

  const os = require("os") as typeof import("os");
  const configPaths: string[] = [];

  // Tier 4 (highest precedence): local developer overrides at
  // .nightgauge/config.local.yaml — gitignored (see ensureGitignore.ts),
  // the secret-safe place for a per-repo GitHub token. Checked BEFORE the
  // committed project config.yaml and the machine config so a per-repo token
  // wins. This is what lets concurrent sessions/workspaces owned by different
  // GitHub users share one machine, each gh call using that repo's own token.
  const localConfig = path.join(root, ".nightgauge", "config.local.yaml");
  if (fs.existsSync(localConfig)) {
    configPaths.push(localConfig);
  }

  const pathResult = resolveConfigPathSync(root);
  if (pathResult.exists) {
    configPaths.push(pathResult.path);
  }
  const globalConfig = path.join(os.homedir(), ".nightgauge", "config.yaml");
  if (fs.existsSync(globalConfig)) {
    configPaths.push(globalConfig);
  }

  const result: Record<string, string> = {};

  for (const configPath of configPaths) {
    try {
      const content = fs.readFileSync(configPath, "utf-8");
      const lines = content.split("\n");
      let inGithubAuth = false;
      let inTokens = false;

      for (const line of lines) {
        const trimmed = line.trim();

        if (trimmed === "github_auth:") {
          inGithubAuth = true;
          inTokens = false;
          continue;
        }

        // Exit github_auth section on next top-level key
        if (inGithubAuth && trimmed && !trimmed.startsWith("#") && /^\S/.test(line)) {
          if (
            !trimmed.startsWith("token:") &&
            !trimmed.startsWith("tokens:") &&
            !trimmed.startsWith("users:")
          ) {
            inGithubAuth = false;
            inTokens = false;
            continue;
          }
        }

        if (inGithubAuth && trimmed === "tokens:") {
          inTokens = true;
          continue;
        }

        if (inTokens) {
          // Exit tokens sub-section when line is not indented or is empty
          if (trimmed === "" || /^\S/.test(line)) {
            inTokens = false;
            inGithubAuth = false;
            continue;
          }
          const match = trimmed.match(/^(\S+):\s*(.+)$/);
          if (match) {
            const owner = match[1];
            const rawValue = match[2].trim();
            const resolved = expandEnvVar(rawValue);
            if (resolved && !result[owner]) {
              result[owner] = resolved;
            }
          }
        }
      }
    } catch {
      // Config read failure is non-fatal
    }
  }

  return result;
}
