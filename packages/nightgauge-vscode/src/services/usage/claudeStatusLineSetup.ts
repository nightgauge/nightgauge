/**
 * Wiring nightgauge's usage feed into Claude Code's `statusLine` setting
 * (Issue #730).
 *
 * ## What this is for
 *
 * `ClaudeRateLimitUsageProvider` can only report a Max-plan allowance if
 * something has written a reading into `ClaudeRateLimitStore`. Its original
 * writer — the `rate_limit_event` envelope on nightgauge's own `claude -p`
 * stream (Issue #709) — is only observable while a pipeline stage is
 * streaming, so between runs the footer falls through to dollar windows that
 * describe pay-per-token billing rather than a subscription.
 *
 * Claude Code hands its configured `statusLine` command the same account-wide
 * figure on every render of every session. `nightgauge hook claude-statusline`
 * records it. This module builds and applies the settings change that puts
 * that verb in the loop.
 *
 * ## Why the logic lives outside the command
 *
 * Everything here is pure or filesystem-only, with no `vscode` import, so the
 * command-building, merge and unwire rules can be tested directly. The command
 * module owns the prompting and the user's confirmation.
 *
 * ## Rules this file exists to enforce
 *
 * - **An existing status line is never destroyed.** A prior `command` is moved
 *   into `--delegate`, which the verb runs with the same payload and prints
 *   verbatim. Wiring costs the operator nothing.
 * - **Wiring is idempotent.** Applying it twice must not nest a nightgauge
 *   delegate inside another, which would fork the process tree on every render
 *   and double-record the same reading.
 * - **Unwiring restores what was there.** The delegate is lifted back out, so
 *   `enable` → `disable` is a round trip rather than a deletion.
 * - **Nothing else in the file is touched.** The settings document is parsed,
 *   one key is changed, and everything else is re-serialised as it was.
 * - **The wired path is re-resolved when it dies.** The command names an
 *   absolute, version-stamped bundle path, and an extension update installs a
 *   new directory and removes the old one -- so the wiring outlives the binary
 *   it names and every Claude Code render since the update has invoked a dead
 *   path (#807). {@link repairStatusLineBinary} is the repair.
 *
 * @see docs/decisions/018-adapter-usage-quota-model.md
 * @see Issue #730 - Claude Max usage at rest
 */

import { constants as fsConstants } from "node:fs";
import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";

/** Path to Claude Code's user settings, relative to the home directory. */
const CLAUDE_SETTINGS_FILE = ".claude/settings.json";

/**
 * Substring that identifies a `statusLine.command` as nightgauge's feed.
 *
 * Matched on the verb rather than on the binary path, because the path moves
 * with every extension update while the verb does not. This is what makes
 * {@link readStatusLineState} able to recognise a command it wrote from an
 * older bundle.
 */
export const STATUS_LINE_VERB = "hook claude-statusline";

/** Absolute path of Claude Code's user settings file. */
export function claudeSettingsPath(home: string = os.homedir()): string {
  return path.join(home, CLAUDE_SETTINGS_FILE);
}

/** Claude Code's `statusLine` setting, as far as this module reads it. */
interface StatusLineSetting {
  type?: string;
  command?: string;
  padding?: number;
}

/** What the settings file currently says about the usage feed. */
export interface StatusLineState {
  /** True when `statusLine.command` invokes nightgauge's verb. */
  wired: boolean;
  /** The current `statusLine.command`, or null when none is configured. */
  command: string | null;
  /**
   * The operator's own command: the `--delegate` payload when wired, the
   * whole command when some other status line is configured, and null when
   * there is none.
   */
  delegate: string | null;
}

/**
 * Quote a command for embedding in `--delegate`.
 *
 * Single quotes with the standard `'\''` escape, because the verb passes the
 * delegate to a shell and an operator's status line routinely contains spaces,
 * pipes and `$(...)`. Double quotes would let the outer shell expand those
 * before the delegate ever ran.
 */
export function quoteDelegate(command: string): string {
  return `'${command.replace(/'/g, `'\\''`)}'`;
}

/**
 * Extract the `--delegate` argument from a nightgauge status-line command.
 *
 * Handles the single-quoted form {@link quoteDelegate} produces, plus a bare
 * unquoted token for a command an operator may have written by hand.
 */
export function parseDelegate(command: string): string | null {
  const quoted = command.match(/--delegate\s+'((?:[^']|'\\'')*)'/);
  if (quoted !== null) {
    return quoted[1].replace(/'\\''/g, "'");
  }
  const bare = command.match(/--delegate\s+(\S+)/);
  return bare !== null ? bare[1] : null;
}

/**
 * Build the `statusLine.command` that wires the feed in.
 *
 * `existingCommand` is the operator's current status line, if any. A command
 * that is already nightgauge's is not re-wrapped — its own delegate is carried
 * across instead, so applying this twice is a no-op rather than a nested
 * invocation.
 */
export function buildStatusLineCommand(binaryPath: string, existingCommand: string | null): string {
  const base = `${quoteDelegate(binaryPath)} ${STATUS_LINE_VERB}`;
  const delegate =
    existingCommand === null
      ? null
      : existingCommand.includes(STATUS_LINE_VERB)
        ? parseDelegate(existingCommand)
        : existingCommand;
  return delegate === null || delegate.trim() === ""
    ? base
    : `${base} --delegate ${quoteDelegate(delegate)}`;
}

/**
 * Extract the nightgauge binary path out of a wired `statusLine.command`.
 *
 * Only the head -- everything before the verb -- is examined, so a `--delegate`
 * payload containing quotes cannot be mistaken for the binary.
 *
 * Returns null for any head this module did not write. That is the whole point:
 * an operator may have wrapped the invocation (`/usr/bin/env`, a `nice`, a
 * wrapper script), and rewriting a shape we do not understand would replace one
 * broken command with a differently broken one.
 */
export function parseStatusLineBinary(command: string): string | null {
  const verbAt = command.indexOf(STATUS_LINE_VERB);
  if (verbAt < 0) {
    return null;
  }
  const head = command.slice(0, verbAt).trim();
  if (head === "") {
    return null;
  }
  const quoted = head.match(/^'((?:[^']|'\\'')*)'$/);
  if (quoted !== null) {
    return quoted[1].replace(/'\\''/g, "'");
  }
  // A bare, unquoted path is the only other shape we recognise. Anything with
  // whitespace in it is a wrapper, not a path.
  return /\s/.test(head) ? null : head;
}

/** What {@link repairStatusLineBinary} did, and why. */
export type StatusLineRepairOutcome =
  /** No nightgauge status line is configured. Nothing to repair. */
  | "not-wired"
  /** The wired binary is still there. */
  | "healthy"
  /** Wired, but the command's head is not a shape this module wrote. */
  | "unrecognized"
  /** The wired binary was gone and the command now names a live one. */
  | "repaired"
  /** The wired binary was gone and no binary could be resolved. Left alone. */
  | "unresolvable";

export interface StatusLineRepairResult {
  outcome: StatusLineRepairOutcome;
  /** The dead path that was wired, when there was one. */
  staleBinary: string | null;
  /** The path now wired, when a repair happened. */
  binary: string | null;
  /** The document to write. Reference-identical to the input when unchanged. */
  settings: Record<string, unknown>;
  changed: boolean;
}

export interface StatusLineRepairDeps {
  /**
   * Resolve the nightgauge binary the extension would use right now.
   *
   * Called only when a repair is actually needed, so the common healthy path
   * costs one `stat` and no PATH lookup.
   */
  resolveBinary: () => Promise<string | null>;
  /** True when the path exists and is executable. Defaults to a real check. */
  isExecutable?: (binaryPath: string) => Promise<boolean>;
}

async function defaultIsExecutable(binaryPath: string): Promise<boolean> {
  try {
    await fs.access(binaryPath, fsConstants.X_OK);
    return true;
  } catch {
    return false;
  }
}

/**
 * Re-point a wired status line whose binary no longer exists.
 *
 * `readStatusLineState` matches on the verb, never on the path, so it happily
 * reports `wired: true` for a command naming a bundle that was deleted two
 * updates ago. Recognising a command is not the same as knowing it runs, and
 * this closes that gap.
 *
 * Refuses to act in the two cases where acting would be worse than a dead
 * command: a head this module did not write, and no resolvable binary to point
 * at. In both the settings document is returned untouched and the caller logs
 * the condition.
 *
 * Idempotent: a second call finds the binary executable and reports "healthy".
 */
export async function repairStatusLineBinary(
  settings: Record<string, unknown>,
  deps: StatusLineRepairDeps
): Promise<StatusLineRepairResult> {
  const unchanged = (
    outcome: StatusLineRepairOutcome,
    staleBinary: string | null = null
  ): StatusLineRepairResult => ({ outcome, staleBinary, binary: null, settings, changed: false });

  const state = readStatusLineState(settings);
  if (!state.wired || state.command === null) {
    return unchanged("not-wired");
  }

  const wiredBinary = parseStatusLineBinary(state.command);
  if (wiredBinary === null) {
    return unchanged("unrecognized");
  }

  const isExecutable = deps.isExecutable ?? defaultIsExecutable;
  if (await isExecutable(wiredBinary)) {
    return unchanged("healthy", wiredBinary);
  }

  const resolved = await deps.resolveBinary();
  // Resolving to the same dead path is not a repair; the resolver's own
  // existence checks should prevent it, and if they ever do not, rewriting the
  // command to itself would report success for a still-broken feed.
  if (resolved === null || resolved === wiredBinary) {
    return unchanged("unresolvable", wiredBinary);
  }

  return {
    outcome: "repaired",
    staleBinary: wiredBinary,
    binary: resolved,
    // buildStatusLineCommand carries the existing --delegate payload across:
    // it parses the delegate out (unescaping) and re-quotes it, which is a
    // round trip, so the operator's own command survives byte-for-byte.
    settings: withStatusLineWired(settings, resolved),
    changed: true,
  };
}

/**
 * Parse a settings document, tolerating an absent or unreadable file.
 *
 * Returns `null` when the file exists but is not a JSON object — the caller
 * must refuse to write in that case rather than replacing a document it could
 * not understand. An absent file yields an empty object, which is a legitimate
 * starting point.
 */
export async function readClaudeSettings(
  settingsPath: string
): Promise<Record<string, unknown> | null> {
  let text: string;
  try {
    text = await fs.readFile(settingsPath, "utf8");
  } catch {
    return {};
  }
  if (text.trim() === "") {
    return {};
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return null;
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
    return null;
  }
  return parsed as Record<string, unknown>;
}

/** Read the `statusLine` setting out of a parsed settings document. */
export function readStatusLineState(settings: Record<string, unknown>): StatusLineState {
  const raw = settings.statusLine;
  if (typeof raw !== "object" || raw === null || Array.isArray(raw)) {
    return { wired: false, command: null, delegate: null };
  }
  const command = (raw as StatusLineSetting).command;
  if (typeof command !== "string" || command.trim() === "") {
    return { wired: false, command: null, delegate: null };
  }
  if (command.includes(STATUS_LINE_VERB)) {
    return { wired: true, command, delegate: parseDelegate(command) };
  }
  return { wired: false, command, delegate: command };
}

/**
 * Return a copy of `settings` with the feed wired in.
 *
 * Every other key, and every other key of `statusLine` itself (`padding`, and
 * whatever Claude Code adds next), is carried through untouched.
 */
export function withStatusLineWired(
  settings: Record<string, unknown>,
  binaryPath: string
): Record<string, unknown> {
  const state = readStatusLineState(settings);
  const existing =
    typeof settings.statusLine === "object" && settings.statusLine !== null
      ? (settings.statusLine as StatusLineSetting)
      : {};
  return {
    ...settings,
    statusLine: {
      ...existing,
      type: "command",
      command: buildStatusLineCommand(binaryPath, state.command),
    },
  };
}

/**
 * Return a copy of `settings` with the feed removed.
 *
 * The delegated command is lifted back into place. With no delegate the
 * `statusLine` key is dropped entirely rather than left as an empty command,
 * which Claude Code would render as a blank status line.
 */
export function withStatusLineUnwired(settings: Record<string, unknown>): Record<string, unknown> {
  const state = readStatusLineState(settings);
  if (!state.wired) {
    return settings;
  }
  const existing = settings.statusLine as StatusLineSetting;
  if (state.delegate === null || state.delegate.trim() === "") {
    const { statusLine: _removed, ...rest } = settings;
    return rest;
  }
  return { ...settings, statusLine: { ...existing, type: "command", command: state.delegate } };
}

/**
 * Write a settings document back, atomically.
 *
 * Claude Code reads this file on its own schedule, and an operator may be
 * editing it; a temp file plus a rename means neither ever sees a truncated
 * document. Two-space indent and a trailing newline match what Claude Code
 * itself writes, so wiring the feed does not show up as a whole-file reformat.
 */
export async function writeClaudeSettings(
  settingsPath: string,
  settings: Record<string, unknown>
): Promise<void> {
  const dir = path.dirname(settingsPath);
  await fs.mkdir(dir, { recursive: true });
  const tempPath = path.join(dir, `.settings-${process.pid}-${Date.now()}.json`);
  try {
    await fs.writeFile(tempPath, `${JSON.stringify(settings, null, 2)}\n`, "utf8");
    await fs.rename(tempPath, settingsPath);
  } catch (error) {
    await fs.rm(tempPath, { force: true }).catch(() => undefined);
    throw error;
  }
}

/**
 * Resolve the real path of a settings file that may be a symlink.
 *
 * Claude Code's own setup guidance says to update the target when
 * `~/.claude/settings.json` is a symlink — dotfile repositories commonly link
 * it — and renaming over a symlink would replace the link with a regular file,
 * silently detaching the operator's managed config.
 */
export async function resolveSettingsTarget(settingsPath: string): Promise<string> {
  try {
    return await fs.realpath(settingsPath);
  } catch {
    return settingsPath;
  }
}
