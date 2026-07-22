/**
 * Global Config Resolver - Utility for resolving global ~/.nightgauge/config.yaml path
 *
 * Provides platform-aware path resolution for the global configuration file.
 * Supports environment variable overrides (NIGHTGAUGE_CONFIG_HOME, XDG_CONFIG_HOME)
 * and platform-specific defaults.
 *
 * Priority order:
 * 1. NIGHTGAUGE_CONFIG_HOME env var → ${NIGHTGAUGE_CONFIG_HOME}/config.yaml
 * 2. XDG_CONFIG_HOME env var → ${XDG_CONFIG_HOME}/nightgauge/config.yaml
 * 3. Platform-specific default:
 *    - macOS: ~/.nightgauge/config.yaml
 *    - Linux: ~/.config/nightgauge/config.yaml (XDG default)
 *    - Windows: %APPDATA%/nightgauge/config.yaml
 *
 * @see Issue #434 - Add Global Config Layer
 * @see docs/CONFIGURATION.md for full documentation
 */

import * as fs from "fs/promises";
import * as path from "path";
import * as os from "os";

/**
 * Config file name (matches project config)
 */
export const GLOBAL_CONFIG_FILE_NAME = "config.yaml";

/**
 * Nightgauge directory name
 */
export const NIGHTGAUGE_DIR_NAME = "nightgauge";

/**
 * Legacy directory name (for backward compatibility)
 */
export const NIGHTGAUGE_LEGACY_DIR_NAME = ".nightgauge";

/**
 * Result of resolving global config path
 */
export interface GlobalConfigPathResult {
  /** Absolute path to the global config file */
  path: string;
  /** Whether the global config file exists */
  exists: boolean;
  /** Source of the path resolution */
  source: "env_nightgauge_config_home" | "env_xdg_config_home" | "platform_default";
  /** The config directory (parent of config.yaml) */
  configDir: string;
}

/**
 * Get the platform type for path resolution
 *
 * @returns Platform identifier: 'darwin' | 'linux' | 'win32' | 'unknown'
 */
export function getPlatform(): NodeJS.Platform {
  return os.platform();
}

/**
 * Get the global config directory based on environment and platform
 *
 * Priority:
 * 1. NIGHTGAUGE_CONFIG_HOME env var
 * 2. XDG_CONFIG_HOME/nightgauge (if XDG_CONFIG_HOME is set)
 * 3. Platform-specific default
 *
 * @param env - Environment variables (defaults to process.env)
 * @param platform - Platform override for testing
 * @returns Object with directory path and source
 */
export function getGlobalConfigDir(
  env: NodeJS.ProcessEnv = process.env,
  platform: NodeJS.Platform = getPlatform()
): { dir: string; source: GlobalConfigPathResult["source"] } {
  const homeDir = os.homedir();

  // 1. Check NIGHTGAUGE_CONFIG_HOME (highest priority)
  if (env.NIGHTGAUGE_CONFIG_HOME) {
    return {
      dir: env.NIGHTGAUGE_CONFIG_HOME,
      source: "env_nightgauge_config_home",
    };
  }

  // 2. Check XDG_CONFIG_HOME (Linux/Unix standard)
  if (env.XDG_CONFIG_HOME) {
    return {
      dir: path.join(env.XDG_CONFIG_HOME, NIGHTGAUGE_DIR_NAME),
      source: "env_xdg_config_home",
    };
  }

  // 3. Platform-specific defaults
  switch (platform) {
    case "darwin":
      // macOS: ~/.nightgauge (follows common conventions like .ssh, .aws)
      return {
        dir: path.join(homeDir, NIGHTGAUGE_LEGACY_DIR_NAME),
        source: "platform_default",
      };

    case "linux":
      // Linux: ~/.config/nightgauge (XDG default when XDG_CONFIG_HOME not set)
      return {
        dir: path.join(homeDir, ".config", NIGHTGAUGE_DIR_NAME),
        source: "platform_default",
      };

    case "win32": {
      // Windows: %APPDATA%/nightgauge
      const appData = env.APPDATA || path.join(homeDir, "AppData", "Roaming");
      return {
        dir: path.join(appData, NIGHTGAUGE_DIR_NAME),
        source: "platform_default",
      };
    }

    default:
      // Unknown platform: fall back to ~/.nightgauge
      return {
        dir: path.join(homeDir, NIGHTGAUGE_LEGACY_DIR_NAME),
        source: "platform_default",
      };
  }
}

/**
 * Get the full path to the global config file
 *
 * @param env - Environment variables (defaults to process.env)
 * @param platform - Platform override for testing
 * @returns Full path to config.yaml
 */
export function getGlobalConfigPath(
  env: NodeJS.ProcessEnv = process.env,
  platform: NodeJS.Platform = getPlatform()
): string {
  const { dir } = getGlobalConfigDir(env, platform);
  return path.join(dir, GLOBAL_CONFIG_FILE_NAME);
}

/**
 * Resolve the global config path with existence check
 *
 * @param env - Environment variables (defaults to process.env)
 * @param platform - Platform override for testing
 * @returns Promise with path, existence flag, and source information
 */
export async function resolveGlobalConfigPath(
  env: NodeJS.ProcessEnv = process.env,
  platform: NodeJS.Platform = getPlatform()
): Promise<GlobalConfigPathResult> {
  const { dir, source } = getGlobalConfigDir(env, platform);
  const configPath = path.join(dir, GLOBAL_CONFIG_FILE_NAME);

  let exists = false;
  try {
    await fs.access(configPath);
    exists = true;
  } catch {
    // File doesn't exist, which is fine (global config is optional)
  }

  return {
    path: configPath,
    exists,
    source,
    configDir: dir,
  };
}

/**
 * Synchronous version of resolveGlobalConfigPath
 *
 * @param env - Environment variables (defaults to process.env)
 * @param platform - Platform override for testing
 * @returns Path result with existence flag and source
 */
export function resolveGlobalConfigPathSync(
  env: NodeJS.ProcessEnv = process.env,
  platform: NodeJS.Platform = getPlatform()
): GlobalConfigPathResult {
  const fsSync = require("fs");
  const { dir, source } = getGlobalConfigDir(env, platform);
  const configPath = path.join(dir, GLOBAL_CONFIG_FILE_NAME);

  const exists = fsSync.existsSync(configPath);

  return {
    path: configPath,
    exists,
    source,
    configDir: dir,
  };
}

/**
 * Check if a global config file exists (async)
 *
 * @param env - Environment variables (defaults to process.env)
 * @param platform - Platform override for testing
 * @returns True if global config exists
 */
export async function globalConfigExists(
  env: NodeJS.ProcessEnv = process.env,
  platform: NodeJS.Platform = getPlatform()
): Promise<boolean> {
  const result = await resolveGlobalConfigPath(env, platform);
  return result.exists;
}

/**
 * Check if a global config file exists (sync)
 *
 * @param env - Environment variables (defaults to process.env)
 * @param platform - Platform override for testing
 * @returns True if global config exists
 */
export function globalConfigExistsSync(
  env: NodeJS.ProcessEnv = process.env,
  platform: NodeJS.Platform = getPlatform()
): boolean {
  const result = resolveGlobalConfigPathSync(env, platform);
  return result.exists;
}

/**
 * Get a human-readable description of where global config is located
 *
 * Useful for documentation and error messages.
 *
 * @param source - The source of the path resolution
 * @param configPath - The full config path
 * @returns Human-readable description
 */
export function describeGlobalConfigLocation(
  source: GlobalConfigPathResult["source"],
  configPath: string
): string {
  switch (source) {
    case "env_nightgauge_config_home":
      return `$NIGHTGAUGE_CONFIG_HOME (${configPath})`;
    case "env_xdg_config_home":
      return `$XDG_CONFIG_HOME/nightgauge (${configPath})`;
    case "platform_default":
      return configPath;
  }
}
