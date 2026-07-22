/**
 * Logger utility for Nightgauge Pipeline extension
 *
 * Wraps VS Code OutputChannel for structured logging.
 */

import * as vscode from "vscode";

/**
 * Log levels for structured logging
 */
export type LogLevel = "DEBUG" | "INFO" | "WARN" | "ERROR";

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

  constructor(name: string) {
    this.channel = vscode.window.createOutputChannel(name);
  }

  /**
   * Format a log message with timestamp and level
   */
  private formatMessage(level: LogLevel, message: string, data?: object): string {
    const timestamp = new Date().toISOString();
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
    return `[${timestamp}] [${level}] ${message}${dataStr}`;
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
  show(): void {
    this.channel.show();
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
   * Dispose the output channel
   */
  dispose(): void {
    this.channel.dispose();
  }
}
