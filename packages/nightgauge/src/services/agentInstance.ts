import { randomUUID } from "node:crypto";

/**
 * This window's instance id on the platform agent (#2395).
 *
 * Every window of a machine shares one platform agent, and a reloaded window
 * reuses its stored agent id without registering again. Each window
 * advertises its own execution profile onto that agent, so the platform needs
 * to tell one window whose profile changed from two windows that disagree.
 * The instance id is how: the registration, every heartbeat and the
 * deregistration carry it, and the platform keeps one record per instance.
 *
 * A random UUID v4, made once per activation and kept in memory only. It is
 * never persisted, and never derived from a path, host, user or workspace.
 * The platform only compares it for equality and never returns it.
 */

/** What the platform accepts as an instance id: 1–64 of `[A-Za-z0-9_-]`. */
const INSTANCE_ID_PATTERN = /^[A-Za-z0-9_-]{1,64}$/;

let current: string | null = null;

/** This activation's instance id, made on first use. */
export function agentInstanceId(): string {
  current ??= randomUUID();
  return current;
}

/**
 * Start a new instance. Called once at the top of `activate()`, so a new
 * activation is a new instance even where the extension host survives it.
 */
export function beginAgentInstance(): void {
  current = randomUUID();
}

/** Whether a value may be sent as `instance_id`. */
export function isValidAgentInstanceId(value: string): boolean {
  return INSTANCE_ID_PATTERN.test(value);
}
