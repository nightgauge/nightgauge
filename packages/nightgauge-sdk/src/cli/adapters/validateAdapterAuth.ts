/**
 * validateAdapterAuth — single-adapter auth pre-flight wrapper.
 *
 * Wraps an adapter's `validateAuth()` (which throws AdapterError) into a
 * structured `{ ok, reason? }` result and enforces a per-adapter timeout.
 *
 * @see Issue #3222 - validateAdapterAuth pre-flight checker per adapter
 */

import { defaultRegistry, type AdapterRegistry } from "./AdapterRegistry.js";
import type { IncrediAdapter, ValidateAuthOptions } from "./ICliAdapter.js";
import { AdapterError, type AdapterErrorCategory } from "./errors.js";

export const DEFAULT_AUTH_TIMEOUT_MS = 5_000;

export type AdapterAuthResult =
  { ok: true } | { ok: false; reason: string; category?: AdapterErrorCategory };

export interface ValidateAdapterAuthOptions extends ValidateAuthOptions {
  /** Override the default 5_000ms timeout. */
  timeoutMs?: number;
  /** Override the default adapter registry (used in tests). */
  registry?: Pick<AdapterRegistry, "get" | "has">;
}

/**
 * Probe a single adapter's auth and return a structured result.
 *
 * - Looks up the adapter in the registry (defaults to `defaultRegistry`).
 * - Races the adapter's `validateAuth()` against a `setTimeout`.
 * - Converts thrown `AdapterError` into `{ ok: false, reason, category }`.
 * - Unknown adapters resolve to `{ ok: false, ... }` rather than throwing,
 *   so a misconfigured stage cannot crash the pre-flight runner.
 */
export async function validateAdapterAuth(
  adapter: IncrediAdapter,
  opts: ValidateAdapterAuthOptions = {}
): Promise<AdapterAuthResult> {
  const registry = opts.registry ?? defaultRegistry;
  const timeoutMs = opts.timeoutMs ?? DEFAULT_AUTH_TIMEOUT_MS;

  let instance;
  try {
    instance = registry.get(adapter);
  } catch (err) {
    return {
      ok: false,
      reason:
        err instanceof Error ? err.message : `Unknown adapter '${adapter}' — not found in registry`,
      category: "CONFIG_INVALID",
    };
  }

  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<AdapterAuthResult>((resolve) => {
    timer = setTimeout(() => {
      resolve({
        ok: false,
        reason: `auth probe timed out after ${timeoutMs / 1000}s`,
        category: "TIMEOUT",
      });
    }, timeoutMs);
  });

  const probe = (async (): Promise<AdapterAuthResult> => {
    try {
      await instance.validateAuth({ runner: opts.runner, cwd: opts.cwd });
      return { ok: true };
    } catch (err) {
      if (err instanceof AdapterError) {
        return { ok: false, reason: err.format(), category: err.category };
      }
      const message = err instanceof Error ? err.message : String(err);
      return { ok: false, reason: message };
    }
  })();

  try {
    return await Promise.race([probe, timeout]);
  } finally {
    if (timer) clearTimeout(timer);
  }
}
