/**
 * The stage gate that proves a Claude stage has its safety gates (#2723).
 *
 * The TypeScript twin of internal/execution/claude_plugin_gate.go, and the same
 * rule: a bundled build reads `system/init` as fail-closed, so a missing
 * `plugins` list, no `nightgauge` entry, more than one, or work with no init
 * before it all stop the stage. A build with no bundle warns and runs.
 *
 * On a bundled build the one `nightgauge` entry must have loaded from the
 * bundled directory: its `source` (`nightgauge@inline` for any --plugin-dir
 * load) cannot say which copy ran (#2883).
 */

import * as fs from "fs";
import * as path from "path";

/** The plugin's manifest name, matched exactly. */
const CLAUDE_PLUGIN_NAME = "nightgauge";

/** Ends a stopped stage's stderr; the gate-plugin-missing terminal rule classifies it. */
export const GATE_PLUGIN_MISSING_MARKER = "[gate-plugin-missing]";

/** Deliberately a different literal: a warning must not park a later failure. */
export const GATE_PLUGIN_WARNING_MARKER = "[gate-plugin-warning]";

/**
 * The supported Claude CLI floor: `min_version` of
 * internal/adaptercompat/manifests/claude-headless.json, which a test holds
 * this to (#2883).
 */
export const CLAUDE_CLI_FLOOR = "2.1.223";

/** One entry of a `system/init` event's `plugins` list. */
export interface ClaudeInitPlugin {
  name: string;
  source?: string;
  version?: string;
  /** The directory it loaded from: the --plugin-dir given, for that load. */
  path?: string;
}

export interface ClaudePluginReading {
  verdict: "pass" | "warn" | "stop";
  /** The loaded plugin's version, unset unless exactly one loaded. */
  version?: string;
  /** What was wrong, unset on a pass. */
  reason?: string;
  /** The init's `claude_code_version`, unset when it named none or no init came. */
  cliVersion?: string;
}

export interface ClaudePluginInitOptions {
  /** The bundled plugin directory; unset on a bundled build means a damaged bundle. */
  expectedDir?: string;
  cliVersion?: string;
}

/** The stream-parser fields the gate reads. */
interface ObservedStreamMessage {
  type: string;
  subtype?: string;
  initPlugins?: ClaudeInitPlugin[];
  claudeCodeVersion?: string;
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
  plugins: readonly ClaudeInitPlugin[] | undefined | null,
  options: ClaudePluginInitOptions = {}
): ClaudePluginReading {
  const { expectedDir, cliVersion } = options;
  let reason: string;
  if (bundled && !expectedDir) {
    reason =
      "this build is stamped as bundled but its plugin at <prefix>/claude-plugins/nightgauge has no hooks/hooks.json";
  } else if (plugins === null) {
    reason = "the stream carried work before any system/init event";
  } else if (plugins === undefined) {
    reason = "system/init has no plugins list";
  } else {
    const loaded = plugins.filter((p) => p.name === CLAUDE_PLUGIN_NAME);
    if (loaded.length === 1) {
      const p = loaded[0];
      if (!bundled || samePluginPath(p.path, expectedDir)) {
        return { verdict: "pass", version: p.version, cliVersion };
      }
      reason =
        `system/init lists nightgauge from ${p.source || "an unnamed location"} at ` +
        `${p.path || "an unnamed location"}, not the bundled plugin at ${expectedDir}`;
    } else {
      reason =
        loaded.length === 0
          ? "system/init lists no nightgauge plugin"
          : `system/init lists ${loaded.length} nightgauge plugins, so each gate would fire more than once`;
    }
  }
  return { verdict: bundled ? "stop" : "warn", reason, cliVersion };
}

/** Compare two plugin directories after resolving and, where they exist, realpath. */
function samePluginPath(a: string | undefined, b: string | undefined): boolean {
  if (!a || !b) return false;
  const norm = (p: string): string => {
    const resolved = path.resolve(p);
    try {
      return fs.realpathSync(resolved);
    } catch {
      return resolved;
    }
  };
  return norm(a) === norm(b);
}

/** -1, 0 or 1; undefined when either is not MAJOR.MINOR.PATCH. */
function compareSemver(a: string, b: string): number | undefined {
  const re = /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/;
  const x = re.exec(a);
  const y = re.exec(b);
  if (!x || !y) return undefined;
  for (let i = 1; i <= 3; i++) {
    const d = Number(x[i]) - Number(y[i]);
    if (d !== 0) return d < 0 ? -1 : 1;
  }
  return 0;
}

/** The floor clause of a stop notice, "" when the CLI meets the floor (#2883). */
export function cliFloorClause(cliVersion: string | undefined): string {
  const cmp = cliVersion ? compareSemver(cliVersion, CLAUDE_CLI_FLOOR) : undefined;
  if (cmp !== undefined && cmp >= 0) return "";
  const detected = cliVersion ? `Claude CLI ${cliVersion}` : "Claude CLI reported no version and";
  return (
    `; ${detected} is below the supported floor ${CLAUDE_CLI_FLOOR}: ` +
    `upgrade Claude Code to ${CLAUDE_CLI_FLOOR} or later`
  );
}

/** Applies {@link evaluateClaudePluginInit} to one stage's stream. */
export class ClaudePluginGate {
  private reading: ClaudePluginReading | undefined;
  private noticeTaken = false;

  constructor(
    private readonly bundled: boolean,
    private readonly expectedDir?: string
  ) {}

  /**
   * Returns true on the one message that stops the stage. Only the first init
   * counts. `hook_started` precedes init on a real stream, so other system
   * events decide nothing.
   */
  observe(message: ObservedStreamMessage | null | undefined): boolean {
    if (this.reading || !message) return false;
    if (message.type === "system" && message.subtype === "init") {
      this.reading = evaluateClaudePluginInit(this.bundled, message.initPlugins, {
        expectedDir: this.expectedDir,
        cliVersion: message.claudeCodeVersion,
      });
    } else if (WORK_MESSAGE_TYPES.has(message.type)) {
      this.reading = evaluateClaudePluginInit(this.bundled, null, {
        expectedDir: this.expectedDir,
      });
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

  /**
   * The {@link notice} the first time it exists, then undefined. Later inits
   * (subagents) repeat nothing, and every decision emits, whichever message
   * made it.
   */
  takeNotice(): string | undefined {
    if (this.noticeTaken) return undefined;
    const notice = this.notice;
    if (notice) this.noticeTaken = true;
    return notice;
  }

  /** The stderr line for this stage: the stop marker, a warning, or undefined. */
  get notice(): string | undefined {
    switch (this.reading?.verdict) {
      case "stop":
        return (
          `${GATE_PLUGIN_MISSING_MARKER} stopped before any work: ${this.reading.reason}; ` +
          "this build's safety gates did not load" +
          cliFloorClause(this.reading.cliVersion)
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
