/**
 * The `nightgauge.platform.url` setting (#1474).
 *
 * Before this setting the platform endpoint could only move by exporting
 * NIGHTGAUGE_PLATFORM_URL (which only the Go daemon read) or by hand-editing
 * `platform.api_url`. The setting feeds the existing resolution path: an
 * override becomes `environment: "custom"` with `api_url` set, so
 * `resolvePlatformBaseUrl` (HTTPS enforcement included) and the token host key
 * see it like any other custom endpoint.
 *
 * Precedence, highest first, and the same in the extension and in the daemon
 * it starts:
 *
 * 1. NIGHTGAUGE_PLATFORM_URL in the extension host's environment
 * 2. the `nightgauge.platform.url` setting
 * 3. the config files (`platform.environment` / `platform.api_url`)
 * 4. the production default
 *
 * The setting is machine-scoped, so a repository's committed workspace
 * settings cannot point the extension's credentials at another host.
 */

import * as vscode from "vscode";
import {
  PLATFORM_ENV_PRESETS,
  resolvePlatformBaseUrl,
  type PlatformConfig,
} from "../config/schema";

export const PLATFORM_URL_ENV_VAR = "NIGHTGAUGE_PLATFORM_URL";
export const PLATFORM_URL_SETTING = "nightgauge.platform.url";

export type PlatformUrlSource = typeof PLATFORM_URL_ENV_VAR | typeof PLATFORM_URL_SETTING;

export interface PlatformUrlOverride {
  url: string;
  source: PlatformUrlSource;
}

/** The override the environment or the setting supplies, if either does. */
export function resolvePlatformUrlOverride(
  envValue: string | undefined,
  settingValue: string | undefined
): PlatformUrlOverride | undefined {
  const fromEnv = envValue?.trim();
  if (fromEnv) return { url: fromEnv, source: PLATFORM_URL_ENV_VAR };
  const fromSetting = settingValue?.trim();
  if (fromSetting) return { url: fromSetting, source: PLATFORM_URL_SETTING };
  return undefined;
}

/** Reads the override from `env` and the VS Code setting. */
export function readPlatformUrlOverride(
  env: Record<string, string | undefined> = process.env
): PlatformUrlOverride | undefined {
  let setting: string | undefined;
  try {
    setting = vscode.workspace.getConfiguration("nightgauge.platform").get<string>("url", "");
  } catch {
    // Not in a VS Code host (tests, CLI tooling): no setting to read.
  }
  return resolvePlatformUrlOverride(env[PLATFORM_URL_ENV_VAR], setting);
}

/** The platform config with the override applied as a custom endpoint. */
export function applyPlatformUrlOverride(
  cfg: PlatformConfig | undefined,
  override: PlatformUrlOverride | undefined
): PlatformConfig | undefined {
  if (!override) return cfg;
  return { ...cfg, environment: "custom", api_url: override.url };
}

export interface PlatformEndpoint {
  /** The URL platform calls go to, or null when the config is refused. */
  baseUrl: string | null;
  /** True unless the endpoint is the production default. */
  nonDefault: boolean;
  /** Why the config was refused (non-HTTPS custom URL), if it was. */
  error?: string;
}

/** The effective endpoint, and whether it differs from production. */
export function describePlatformEndpoint(cfg: PlatformConfig | undefined): PlatformEndpoint {
  try {
    const baseUrl = resolvePlatformBaseUrl(cfg);
    return { baseUrl, nonDefault: originOf(baseUrl) !== originOf(PLATFORM_ENV_PRESETS.production) };
  } catch (e) {
    return { baseUrl: null, nonDefault: true, error: e instanceof Error ? e.message : String(e) };
  }
}

function originOf(url: string): string | null {
  try {
    return new URL(url).origin.toLowerCase();
  } catch {
    return null;
  }
}
