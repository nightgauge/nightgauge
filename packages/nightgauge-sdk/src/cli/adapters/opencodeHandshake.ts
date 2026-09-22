/**
 * The Nightgauge OpenCode plugin handshake on the SDK spawn path (#1804 AC3,
 * #1648): the checks the Go manager makes (internal/execution/manager.go, with
 * `opencodeplugin.VerifyLoaded` / `VerifyNotLate`) made on an SDK-spawned run.
 *
 * `nightgauge opencode config` mints the handshake and prints it: the nonce and
 * the sentinel path in `env` (NIGHTGAUGE_OPENCODE_PLUGIN_NONCE / _SENTINEL) and
 * the version the plugin writes in `plugin_version`. Nothing here derives any
 * of them; the plugin (internal/execution/opencodeplugin/plugin/nightgauge.js)
 * writes the sentinel at init, and this module only reads it back.
 *
 * - At the run's first `step_start` event, the earliest moment a tool call
 *   could exist, the sentinel must exist and carry this run's nonce and the
 *   plugin version the verb installed; otherwise the run is killed at once.
 * - When the run has ended, if it made a tool call, the sentinel's mtime must
 *   not be after that call's start: a sentinel written late means a tool ran
 *   ungated.
 *
 * A failure is an `adapter_incompatible` marker, the word the Go failure
 * classification keys on.
 *
 * @see docs/decisions/022-opencode-multi-provider-adapter.md
 */

import { readFileSync, statSync } from "node:fs";

import {
  OPENCODE_PLUGIN_NONCE_ENV,
  OPENCODE_PLUGIN_PATH_ENV,
  OPENCODE_PLUGIN_SENTINEL_ENV,
} from "./childEnv.js";

/** The prefix of a failed handshake's marker, as manager.go writes it. */
export const OPENCODE_PLUGIN_HANDSHAKE_MARKER = "[nightgauge-opencode-plugin]";

/** What one run's handshake is verified against (`opencodeplugin.HandshakeConfig`). */
export interface OpenCodeHandshake {
  readonly nonce: string;
  readonly sentinelPath: string;
  readonly pluginPath?: string;
  /** The `plugin_version` the verb installed and the sentinel must carry. */
  readonly pluginVersion: string;
}

/**
 * The handshake a run's environment carries, or `undefined` when it carries
 * none (`HandshakeConfigFromEnv`: no nonce or no sentinel path).
 */
export function openCodeHandshakeFromEnv(
  env: Readonly<Record<string, string>>,
  pluginVersion: string
): OpenCodeHandshake | undefined {
  const nonce = env[OPENCODE_PLUGIN_NONCE_ENV];
  const sentinelPath = env[OPENCODE_PLUGIN_SENTINEL_ENV];
  if (!nonce || !sentinelPath) return undefined;
  return { nonce, sentinelPath, pluginPath: env[OPENCODE_PLUGIN_PATH_ENV], pluginVersion };
}

function incompatible(h: OpenCodeHandshake, reason: string): string {
  const plugin = h.pluginPath ? ` (plugin: ${h.pluginPath})` : "";
  return `${OPENCODE_PLUGIN_HANDSHAKE_MARKER} adapter_incompatible: ${reason}${plugin}`;
}

/**
 * The step_start check (`VerifyLoaded`): the failure marker, or `undefined`
 * when the sentinel carries this run's nonce and the installed plugin version.
 */
export function verifyOpenCodePluginLoaded(h: OpenCodeHandshake): string | undefined {
  let data: string;
  try {
    data = readFileSync(h.sentinelPath, "utf-8");
  } catch (err) {
    return incompatible(
      h,
      `no plugin handshake sentinel at ${h.sentinelPath}: the Nightgauge OpenCode plugin did not load (${(err as Error).message})`
    );
  }
  let sentinel: { nonce?: unknown; plugin_version?: unknown };
  try {
    sentinel = JSON.parse(data);
  } catch (err) {
    return incompatible(
      h,
      `the plugin handshake sentinel at ${h.sentinelPath} did not parse as JSON: ${(err as Error).message}`
    );
  }
  if (sentinel === null || typeof sentinel !== "object" || sentinel.nonce !== h.nonce) {
    return incompatible(
      h,
      `the plugin handshake sentinel at ${h.sentinelPath} carries the wrong nonce: a stale or foreign sentinel was read`
    );
  }
  if (sentinel.plugin_version !== h.pluginVersion) {
    return incompatible(
      h,
      `the plugin handshake sentinel at ${h.sentinelPath} names plugin_version ${JSON.stringify(sentinel.plugin_version)}, not the ${JSON.stringify(h.pluginVersion)} nightgauge installed`
    );
  }
  return undefined;
}

/**
 * The exit check (`VerifyNotLate`): with a tool call observed, the sentinel
 * must still exist and its mtime must not be after the call's start
 * (`firstToolUseMs`, epoch milliseconds). No tool call always passes.
 */
export function verifyOpenCodePluginNotLate(
  h: OpenCodeHandshake,
  firstToolUseMs: number | undefined
): string | undefined {
  if (firstToolUseMs === undefined) return undefined;
  let mtimeMs: number;
  try {
    mtimeMs = statSync(h.sentinelPath).mtimeMs;
  } catch (err) {
    return incompatible(
      h,
      `the plugin handshake sentinel at ${h.sentinelPath} is gone at exit: ${(err as Error).message}`
    );
  }
  if (mtimeMs > firstToolUseMs) {
    return incompatible(
      h,
      `the plugin handshake sentinel at ${h.sentinelPath} was written after the run's first tool call started`
    );
  }
  return undefined;
}

/**
 * Watches one run's stdout, line by line, for the two events the handshake
 * keys on, as manager.go's stdout reader does.
 */
export class OpenCodeHandshakeWatch {
  private checked = false;
  private firstToolUseMs: number | undefined;
  private failure: string | undefined;

  constructor(
    private readonly handshake: OpenCodeHandshake,
    private readonly now: () => number = Date.now
  ) {}

  /**
   * Observe one stdout line. Returns true exactly once, when the step_start
   * check fails: the caller kills the run's process group at once.
   */
  observe(line: string): boolean {
    if (this.failure !== undefined) return false;
    let event: { type?: unknown; part?: { state?: { time?: { start?: unknown } } } };
    try {
      event = JSON.parse(line);
    } catch {
      return false;
    }
    if (event === null || typeof event !== "object") return false;
    if (event.type === "step_start" && !this.checked) {
      this.checked = true;
      this.failure = verifyOpenCodePluginLoaded(this.handshake);
      return this.failure !== undefined;
    }
    if (event.type === "tool_use" && this.firstToolUseMs === undefined) {
      // The tool's own start time: opencode emits tool_use only once a call
      // has ended, so "now" is later than when the tool ran.
      const start = event.part?.state?.time?.start;
      this.firstToolUseMs = typeof start === "number" && start > 0 ? start : this.now();
    }
    return false;
  }

  /** After the run has exited: the failure marker, or `undefined` when the handshake held. */
  finish(): string | undefined {
    if (this.failure === undefined) {
      this.failure = verifyOpenCodePluginNotLate(this.handshake, this.firstToolUseMs);
    }
    return this.failure;
  }
}
