/**
 * The stage gate that proves a Claude stage has its safety gates (#2723).
 *
 * The TypeScript twin of internal/execution/claude_plugin_gate.go, and the same
 * rule: a bundled build reads `system/init` as fail-closed, so a missing
 * `plugins` list, no `nightgauge` entry, more than one, or work with no init
 * before it all stop the stage. A build with no bundle warns and runs.
 */

/** The plugin's manifest name, matched exactly. */
const CLAUDE_PLUGIN_NAME = "nightgauge";

/** Ends a stopped stage's stderr; the gate-plugin-missing terminal rule classifies it. */
export const GATE_PLUGIN_MISSING_MARKER = "[gate-plugin-missing]";

/** Deliberately a different literal: a warning must not park a later failure. */
export const GATE_PLUGIN_WARNING_MARKER = "[gate-plugin-warning]";

/** One entry of a `system/init` event's `plugins` list. */
export interface ClaudeInitPlugin {
  name: string;
  source?: string;
  version?: string;
}

export interface ClaudePluginReading {
  verdict: "pass" | "warn" | "stop";
  /** The loaded plugin's version, unset unless exactly one loaded. */
  version?: string;
  /** What was wrong, unset on a pass. */
  reason?: string;
}

/** The stream-parser fields the gate reads. */
interface ObservedStreamMessage {
  type: string;
  subtype?: string;
  initPlugins?: ClaudeInitPlugin[];
}

/** Message types only a running session produces. */
const WORK_MESSAGE_TYPES: ReadonlySet<string> = new Set([
  "assistant",
  "user",
  "result",
  "content_block_start",
  "content_block_delta",
]);

/**
 * Judge a `system/init` event's plugin list. `undefined` is an init with no list,
 * and `null` is work on the stream with no init before it.
 */
export function evaluateClaudePluginInit(
  bundled: boolean,
  plugins: readonly ClaudeInitPlugin[] | undefined | null
): ClaudePluginReading {
  let reason: string;
  if (plugins === null) {
    reason = "the stream carried work before any system/init event";
  } else if (plugins === undefined) {
    reason = "system/init has no plugins list";
  } else {
    const loaded = plugins.filter((p) => p.name === CLAUDE_PLUGIN_NAME);
    if (loaded.length === 1) {
      return { verdict: "pass", version: loaded[0].version };
    }
    reason =
      loaded.length === 0
        ? "system/init lists no nightgauge plugin"
        : `system/init lists ${loaded.length} nightgauge plugins, so each gate would fire more than once`;
  }
  return { verdict: bundled ? "stop" : "warn", reason };
}

/** Applies {@link evaluateClaudePluginInit} to one stage's stream. */
export class ClaudePluginGate {
  private reading: ClaudePluginReading | undefined;

  constructor(private readonly bundled: boolean) {}

  /**
   * Returns true on the one message that stops the stage. Only the first init
   * counts. `hook_started` precedes init on a real stream, so other system
   * events decide nothing.
   */
  observe(message: ObservedStreamMessage | null | undefined): boolean {
    if (this.reading || !message) return false;
    if (message.type === "system" && message.subtype === "init") {
      this.reading = evaluateClaudePluginInit(this.bundled, message.initPlugins);
    } else if (WORK_MESSAGE_TYPES.has(message.type)) {
      this.reading = evaluateClaudePluginInit(this.bundled, null);
    } else {
      return false;
    }
    return this.reading.verdict === "stop";
  }

  get stopped(): boolean {
    return this.reading?.verdict === "stop";
  }

  /** The plugin version to record, undefined when none loaded. */
  get version(): string | undefined {
    return this.reading?.version;
  }

  /** The stderr line for this stage: the stop marker, a warning, or undefined. */
  get notice(): string | undefined {
    switch (this.reading?.verdict) {
      case "stop":
        return (
          `${GATE_PLUGIN_MISSING_MARKER} stopped before any work: ${this.reading.reason}; ` +
          "this build's safety gates did not load"
        );
      case "warn":
        return (
          `${GATE_PLUGIN_WARNING_MARKER} ${this.reading.reason}; ` +
          "this build has no bundled plugin, so the stage continues without confirmed gates"
        );
      default:
        return undefined;
    }
  }
}
