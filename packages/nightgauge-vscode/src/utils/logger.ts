/**
 * Logger utility for Nightgauge Pipeline extension
 *
 * Wraps VS Code OutputChannel for structured logging.
 */

import * as vscode from "vscode";
import { redactSecrets } from "./redaction";

/**
 * Log levels for structured logging
 */
export type LogLevel = "DEBUG" | "INFO" | "WARN" | "ERROR";

/**
 * The one channel every extension-side diagnostic lands in by default (#749).
 * Six channels — Nightgauge, Autonomous, Codex Setup, Go Backend, Pipeline,
 * Plugin Setup — used to split this knowledge across places an operator had
 * to already know to look. All but the Go Backend transport log now fold in
 * here, tagged with a subsystem prefix instead of a separate destination.
 *
 * @see docs/TROUBLESHOOTING.md — where to look when something fails
 */
export const MAIN_CHANNEL_NAME = "Nightgauge";

let mainChannel: vscode.OutputChannel | null = null;

/**
 * The single shared "Nightgauge" output channel. Lazily created so importing
 * this module never touches the VS Code API outside a real extension host,
 * and memoized so every caller — the main Logger, the retired-channel
 * wrappers below, and the IPC transport mirror — writes to the same channel
 * object rather than several channels that merely share a display name.
 */
export function getMainChannel(): vscode.OutputChannel {
  if (!mainChannel) {
    mainChannel = vscode.window.createOutputChannel(MAIN_CHANNEL_NAME);
  }
  return mainChannel;
}

/** Test-only: drop the memoized main channel so the next getMainChannel() recreates it. */
export function resetMainChannelForTests(): void {
  mainChannel = null;
}

export interface LoggerOptions {
  /**
   * Reuse an existing output channel instead of creating a new one. Used to
   * consolidate several logical loggers onto one shared destination (#749).
   * When set, dispose() does not tear down the channel — it isn't this
   * instance's to own, and the shared channel outlives any one consumer.
   */
  channel?: vscode.OutputChannel;
  /**
   * Structured subsystem tag prepended to every formatted line, e.g.
   * "codex-setup". Lets several subsystems share one channel (via `channel`
   * above) while staying greppable.
   */
  prefix?: string;
}

/**
 * Logger class for Nightgauge Pipeline extension
 *
 * Provides structured logging to a VS Code OutputChannel.
 *
 * @example
 * ```typescript
 * const logger = new Logger('Nightgauge Pipeline');
 * logger.info('Pipeline started', { issueNumber: 42 });
 * logger.error('Stage failed', new Error('Connection timeout'));
 * ```
 */
export class Logger {
  private channel: vscode.OutputChannel;
  private readonly ownsChannel: boolean;
  private readonly prefix?: string;

  constructor(name: string, options?: LoggerOptions) {
    if (options?.channel) {
      this.channel = options.channel;
      this.ownsChannel = false;
    } else {
      this.channel = vscode.window.createOutputChannel(name);
      this.ownsChannel = true;
    }
    this.prefix = options?.prefix;
  }

  /**
   * Format a log message with timestamp and level
   */
  private formatMessage(level: LogLevel, message: string, data?: object): string {
    const timestamp = new Date().toISOString();
    const tag = this.prefix ? `[${this.prefix}] ` : "";
    const dataStr = data
      ? ` ${JSON.stringify(data, (_key, value) => {
          if (value instanceof Error) {
            return {
              message: value.message,
              stack: value.stack,
              name: value.name,
            };
          }
          return value;
        })}`
      : "";
    return redactSecrets(`[${timestamp}] [${level}] ${tag}${message}${dataStr}`);
  }

  /**
   * Log a debug message (only shown when debugging)
   */
  debug(message: string, data?: object): void {
    this.channel.appendLine(this.formatMessage("DEBUG", message, data));
  }

  /**
   * Log an informational message
   */
  info(message: string, data?: object): void {
    this.channel.appendLine(this.formatMessage("INFO", message, data));
  }

  /**
   * Log a warning message
   */
  warn(message: string, data?: object): void {
    this.channel.appendLine(this.formatMessage("WARN", message, data));
  }

  /**
   * Log an error message and show the output channel
   */
  error(message: string, error?: Error | object): void {
    const data = error instanceof Error ? { error: error.message, stack: error.stack } : error;
    this.channel.appendLine(this.formatMessage("ERROR", message, data));
    this.channel.show(true); // Show output channel on error
  }

  /**
   * Show the output channel
   */
  show(preserveFocus?: boolean): void {
    this.channel.show(preserveFocus);
  }

  /**
   * Clear the output channel
   */
  clear(): void {
    this.channel.clear();
  }

  /**
   * Get the underlying OutputChannel for disposal
   */
  getChannel(): vscode.OutputChannel {
    return this.channel;
  }

  /**
   * Dispose the output channel — a no-op when this instance was constructed
   * with a shared `channel` (see LoggerOptions), since it does not own it.
   */
  dispose(): void {
    if (this.ownsChannel) {
      this.channel.dispose();
    }
  }
}

/**
 * The extension's single top-level Logger, bound to the shared main channel.
 * Bootstrap constructs exactly one of these at activation (#749).
 */
export function createMainLogger(): Logger {
  return new Logger(MAIN_CHANNEL_NAME, { channel: getMainChannel() });
}

/**
 * Wrap the shared main channel for callers that hold a raw
 * `vscode.OutputChannel` reference rather than a `Logger` — several call
 * sites across the extension (autonomous watchdog, project board sync)
 * write many pre-formatted lines directly to a channel and converting every
 * call site to the structured Logger API is out of proportion to the fix.
 * This wrapper gives them the same consolidation, redaction, and subsystem
 * tagging as Logger consumers without changing a single call site (#749):
 *
 *   - `appendLine`/`append`/`replace` prefix the subsystem tag and run the
 *     text through {@link redactSecrets} before it reaches the real channel.
 *   - `clear()` and `dispose()` are no-ops: the shared channel is not this
 *     caller's to wipe or tear down — one subsystem going quiet must not
 *     erase another's history or kill the channel out from under it.
 *   - `show`/`hide` delegate straight through.
 */
export function getPrefixedMainChannel(prefix: string): vscode.OutputChannel {
  const target = getMainChannel();
  const tag = `[${prefix}] `;
  const wrapped: vscode.OutputChannel = {
    get name(): string {
      return target.name;
    },
    append(value: string): void {
      target.append(redactSecrets(value));
    },
    appendLine(value: string): void {
      target.appendLine(`${tag}${redactSecrets(value)}`);
    },
    replace(value: string): void {
      target.replace(redactSecrets(value));
    },
    clear(): void {
      // Shared channel — never clear another subsystem's history.
    },
    show(columnOrPreserveFocus?: unknown, preserveFocus?: unknown): void {
      (target.show as (...args: unknown[]) => void)(columnOrPreserveFocus, preserveFocus);
    },
    hide(): void {
      target.hide();
    },
    dispose(): void {
      // Shared channel — not owned by this caller; it lives for the
      // extension's lifetime, torn down (if ever) by whoever created it.
    },
  };
  return wrapped;
}

/**
 * Mirror a transport-level failure into the main channel with its endpoint
 * and (when parseable from the error text) HTTP status, so an error that
 * reaches a user-visible surface is never findable only in the raw
 * "Nightgauge Go Backend" transport log (#749). Redacted like every other
 * line that reaches the shared channel.
 */
export function logDiagnosticMirror(
  endpoint: string,
  status: number | undefined,
  message: string
): void {
  const timestamp = new Date().toISOString();
  const statusPart = status !== undefined ? ` status=${status}` : "";
  getMainChannel().appendLine(
    redactSecrets(`[${timestamp}] [WARN] [ipc] ${endpoint}${statusPart}: ${message}`)
  );
}
