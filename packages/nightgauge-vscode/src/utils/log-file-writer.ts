/**
 * LogFileWriter - Utility for persisting pipeline logs to disk
 *
 * Writes log entries to .nightgauge/logs/ directory with timestamp prefixes.
 * Respects pipeline.logs config from config.yaml.
 *
 * @see Issue #190 - Pipeline logs persistence
 * @see docs/ARCHITECTURE.md for utility patterns
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";
import { redactSecrets } from "./redaction";

/**
 * Parsed log entry from disk log file
 *
 * @see Issue #1352 - Output window replay persisted log
 */
export interface LogFileEntry {
  timestamp: Date;
  level: string;
  stage: string | null;
  text: string;
}

/**
 * Descriptor for a single log file on disk
 *
 * Returned by {@link LogFileWriter.listLogs} so callers can enumerate
 * archived sessions (e.g., for output-window rehydration on restart)
 * without loading full file contents.
 *
 * @see Issue #2818 - Rehydrate output window tabs from disk logs on restart
 */
export interface LogFileDescriptor {
  issueNumber: number;
  startedAt: Date;
  filePath: string;
}

/**
 * Configuration for log file writing
 */
export interface LogFileConfig {
  /** Whether to write logs to files (default: true) */
  retain: boolean;
  /** Directory for log files relative to workspace root (default: .nightgauge/logs) */
  dir: string;
  /** Maximum age in days before cleanup (optional) */
  max_age_days?: number;
  /** Maximum number of log files to keep (optional) */
  max_count?: number;
  /**
   * Per-entry cap in chars for disk writes (default: 65536 — #192). The disk
   * log is the persistent forensic record: the old 200-char UI truncation
   * leaked into it and made the log useless at the moments that mattered
   * (the forbidden `--admin` attempt left no trace). Generous by design —
   * never drop command bodies.
   */
  max_entry_chars?: number;
}

/**
 * Default per-entry disk cap (#192): 64KB keeps pathological single entries
 * bounded while preserving full tool_use inputs / results and code-block
 * bodies — the single most valuable forensic data.
 */
export const DEFAULT_DISK_LOG_MAX_ENTRY_CHARS = 64 * 1024;

/**
 * Default configuration values
 */
const DEFAULT_CONFIG: LogFileConfig = {
  retain: true,
  dir: ".nightgauge/logs",
};

/**
 * Utility class for writing pipeline logs to disk
 *
 * @example
 * ```typescript
 * // Write a log entry
 * await LogFileWriter.appendToLog(
 *   '/workspace/root',
 *   42,
 *   'INFO',
 *   'feature-dev',
 *   'Starting implementation...',
 *   { retain: true, dir: '.nightgauge/logs' }
 * );
 *
 * // Generate filename for current session
 * const filename = LogFileWriter.generateFilename(42);
 * // Returns: "2026-02-04_42_session.log"
 * ```
 */
export class LogFileWriter {
  /**
   * Append a log entry to the session log file
   *
   * @param workspaceRoot - Absolute path to workspace root
   * @param issueNumber - Current issue number (null if none)
   * @param level - Log level (INFO, DEBUG, WARNING, ERROR, etc.)
   * @param stage - Pipeline stage (or null if not stage-specific)
   * @param message - Log message content
   * @param config - Optional log file configuration
   */
  static async appendToLog(
    workspaceRoot: string,
    issueNumber: number | null,
    level: string,
    stage: string | null,
    message: string,
    config?: Partial<LogFileConfig>
  ): Promise<void> {
    const mergedConfig = { ...DEFAULT_CONFIG, ...config };

    // Respect retain: false to disable file logging
    if (!mergedConfig.retain) {
      return;
    }

    const logDir = path.join(workspaceRoot, mergedConfig.dir);
    const filename = this.generateFilename(issueNumber);
    const logPath = path.join(logDir, filename);
    const timestamp = new Date().toISOString();
    const stageTag = stage ? `[${stage}] ` : "";
    // Redact secrets before they hit disk: stage stdout / tool_result output can
    // echo tokens or PEM blocks, and these logs persist under .nightgauge/logs/
    // (#170). Truncation is not redaction, so scrub the message content here.
    const safeMessage = redactSecrets(message);
    const line = `[${timestamp}] [${level.toUpperCase()}] ${stageTag}${safeMessage}\n`;

    try {
      // Ensure log directory exists
      await fs.mkdir(logDir, { recursive: true });

      // Append log entry (atomic per line)
      await fs.appendFile(logPath, line, "utf-8");
    } catch (error) {
      // Log warning but don't throw - disk logging is non-critical
      // Failing to write a log should never break the pipeline
      console.warn(`[Nightgauge] Failed to write to log file: ${error}`);
    }
  }

  /**
   * Generate the log filename for the current session
   *
   * Format: {YYYY-MM-DD}_{issue-number}_session.log
   * Without issue number: {YYYY-MM-DD}_session.log
   *
   * @param issueNumber - Issue number or null
   * @param date - Optional date override (for testing)
   * @returns Filename string
   */
  static generateFilename(issueNumber: number | null, date?: Date): string {
    const d = date ?? new Date();
    const dateStr = d.toISOString().split("T")[0]; // YYYY-MM-DD
    const issuePart = issueNumber ? `_${issueNumber}` : "";
    return `${dateStr}${issuePart}_session.log`;
  }

  /**
   * Get the full path to the current session log file
   *
   * @param workspaceRoot - Workspace root path
   * @param issueNumber - Issue number or null
   * @param config - Optional config with dir override
   * @returns Full path to log file
   */
  static getLogPath(
    workspaceRoot: string,
    issueNumber: number | null,
    config?: Partial<LogFileConfig>
  ): string {
    const mergedConfig = { ...DEFAULT_CONFIG, ...config };
    const logDir = path.join(workspaceRoot, mergedConfig.dir);
    const filename = this.generateFilename(issueNumber);
    return path.join(logDir, filename);
  }

  /**
   * Check if log file exists for the current session
   *
   * @param workspaceRoot - Workspace root path
   * @param issueNumber - Issue number or null
   * @param config - Optional config
   * @returns True if log file exists
   */
  static async exists(
    workspaceRoot: string,
    issueNumber: number | null,
    config?: Partial<LogFileConfig>
  ): Promise<boolean> {
    const logPath = this.getLogPath(workspaceRoot, issueNumber, config);
    try {
      await fs.access(logPath);
      return true;
    } catch {
      return false;
    }
  }

  /**
   * Read and parse all log entries for an issue from disk
   *
   * Finds all log files matching *_{issueNumber}_session.log, reads them in
   * chronological order (by filename date prefix), and returns parsed entries.
   *
   * Returns empty array if no log files exist or logging is disabled.
   *
   * @param workspaceRoot - Absolute path to workspace root
   * @param issueNumber - Issue number to read logs for
   * @param config - Optional config with dir override
   * @returns Parsed log entries in chronological order
   *
   * @see Issue #1352 - Output window replay persisted log
   */
  static async readEntriesForIssue(
    workspaceRoot: string,
    issueNumber: number,
    config?: Partial<LogFileConfig>
  ): Promise<LogFileEntry[]> {
    const mergedConfig = { ...DEFAULT_CONFIG, ...config };
    if (!mergedConfig.retain) return [];

    const logDir = path.join(workspaceRoot, mergedConfig.dir);

    let filenames: string[];
    try {
      const allFiles = await fs.readdir(logDir);
      filenames = allFiles.filter((f) => f.endsWith(`_${issueNumber}_session.log`)).sort(); // YYYY-MM-DD prefix sorts chronologically
    } catch {
      return []; // Directory doesn't exist — no logs yet
    }

    const entries: LogFileEntry[] = [];
    for (const filename of filenames) {
      const logPath = path.join(logDir, filename);
      try {
        const content = await fs.readFile(logPath, "utf-8");
        for (const line of content.split("\n")) {
          const parsed = LogFileWriter.parseLogLine(line);
          if (parsed) entries.push(parsed);
        }
      } catch {
        // Skip unreadable files — non-critical
      }
    }
    return entries;
  }

  /**
   * Enumerate archived session log files on disk.
   *
   * Scans the log directory for files named `{YYYY-MM-DD}_{issue}_session.log`,
   * applies retention limits from the provided config, and returns descriptors
   * sorted newest first. The list is deliberately lightweight — no file
   * contents are read; call {@link readLog} for each descriptor that should
   * be rehydrated.
   *
   * Retention semantics:
   * - `max_age_days`: files with a date prefix older than N days are excluded
   * - `max_count`: after age filtering, keep only the N most recent files
   * - Files lacking a parseable issue number or date are skipped silently
   *
   * @param workspaceRoot - Absolute path to workspace root
   * @param config - Optional config; `retain: false` returns an empty list
   * @returns Descriptors sorted by startedAt descending (newest first)
   *
   * @see Issue #2818 - Rehydrate output window tabs from disk logs on restart
   */
  static async listLogs(
    workspaceRoot: string,
    config?: Partial<LogFileConfig>
  ): Promise<LogFileDescriptor[]> {
    const mergedConfig = { ...DEFAULT_CONFIG, ...config };
    if (!mergedConfig.retain) return [];

    const logDir = path.join(workspaceRoot, mergedConfig.dir);

    let filenames: string[];
    try {
      filenames = await fs.readdir(logDir);
    } catch {
      return []; // Directory does not exist yet — nothing to rehydrate
    }

    const pattern = /^(\d{4}-\d{2}-\d{2})_(\d+)_session\.log$/;
    const descriptors: LogFileDescriptor[] = [];

    for (const filename of filenames) {
      const match = filename.match(pattern);
      if (!match) continue; // Skip non-matching files (e.g., stray `_session.log` without issue)

      const [, dateStr, issueStr] = match;
      const issueNumber = Number.parseInt(issueStr, 10);
      if (!Number.isFinite(issueNumber)) continue;

      // Parse date prefix as UTC midnight — consistent with generateFilename's UTC ISO prefix
      const startedAt = new Date(`${dateStr}T00:00:00.000Z`);
      if (isNaN(startedAt.getTime())) continue;

      descriptors.push({
        issueNumber,
        startedAt,
        filePath: path.join(logDir, filename),
      });
    }

    // Apply max_age_days relative to now
    if (mergedConfig.max_age_days !== undefined && mergedConfig.max_age_days > 0) {
      const cutoff = Date.now() - mergedConfig.max_age_days * 24 * 60 * 60 * 1000;
      for (let i = descriptors.length - 1; i >= 0; i--) {
        if (descriptors[i].startedAt.getTime() < cutoff) {
          descriptors.splice(i, 1);
        }
      }
    }

    // Sort newest first (descending)
    descriptors.sort((a, b) => b.startedAt.getTime() - a.startedAt.getTime());

    // Apply max_count after age filter so the N most recent eligible files survive
    if (mergedConfig.max_count !== undefined && mergedConfig.max_count >= 0) {
      return descriptors.slice(0, mergedConfig.max_count);
    }

    return descriptors;
  }

  /**
   * Delete session log files beyond the retention window.
   *
   * Enumerates eligible logs, retains the most recent `max_count` within
   * `max_age_days`, and deletes the rest. Returns counts for user reporting.
   *
   * @param workspaceRoot - Absolute path to workspace root
   * @param config - Partial log config; defaults applied when fields unset
   * @returns { kept, deleted, failed } — file counts after cleanup
   */
  static async cleanupLogs(
    workspaceRoot: string,
    config?: Partial<LogFileConfig>
  ): Promise<{ kept: number; deleted: number; failed: number }> {
    const mergedConfig = { ...DEFAULT_CONFIG, ...config };
    const logDir = path.join(workspaceRoot, mergedConfig.dir);

    let filenames: string[];
    try {
      filenames = await fs.readdir(logDir);
    } catch {
      return { kept: 0, deleted: 0, failed: 0 };
    }

    const pattern = /^(\d{4}-\d{2}-\d{2})_(\d+)_session\.log$/;
    type Entry = { filePath: string; startedAt: Date };
    const entries: Entry[] = [];

    for (const filename of filenames) {
      const match = filename.match(pattern);
      if (!match) continue;
      const startedAt = new Date(`${match[1]}T00:00:00.000Z`);
      if (isNaN(startedAt.getTime())) continue;
      entries.push({ filePath: path.join(logDir, filename), startedAt });
    }

    entries.sort((a, b) => b.startedAt.getTime() - a.startedAt.getTime());

    const toKeep = new Set<string>();
    const ageCutoff =
      mergedConfig.max_age_days && mergedConfig.max_age_days > 0
        ? Date.now() - mergedConfig.max_age_days * 24 * 60 * 60 * 1000
        : null;
    const countLimit =
      mergedConfig.max_count !== undefined && mergedConfig.max_count >= 0
        ? mergedConfig.max_count
        : null;

    for (const entry of entries) {
      if (ageCutoff !== null && entry.startedAt.getTime() < ageCutoff) continue;
      if (countLimit !== null && toKeep.size >= countLimit) break;
      toKeep.add(entry.filePath);
    }

    let deleted = 0;
    let failed = 0;
    for (const entry of entries) {
      if (toKeep.has(entry.filePath)) continue;
      try {
        await fs.unlink(entry.filePath);
        deleted += 1;
      } catch {
        failed += 1;
      }
    }

    return { kept: toKeep.size, deleted, failed };
  }

  /**
   * Read and parse a single log file into entries.
   *
   * Unreadable files resolve to an empty array so the caller can continue
   * rehydrating sibling logs without aborting.
   *
   * @param filePath - Absolute path to a session log file
   * @returns Parsed entries in file order; empty on read/parse failure
   *
   * @see Issue #2818 - Rehydrate output window tabs from disk logs on restart
   */
  static async readLog(filePath: string): Promise<LogFileEntry[]> {
    let content: string;
    try {
      content = await fs.readFile(filePath, "utf-8");
    } catch {
      return [];
    }

    const entries: LogFileEntry[] = [];
    for (const line of content.split("\n")) {
      const parsed = LogFileWriter.parseLogLine(line);
      if (parsed) entries.push(parsed);
    }
    return entries;
  }

  /**
   * Parse a single log line into a LogFileEntry
   *
   * Format: [ISO8601] [LEVEL] [stage?] message
   * Example: [2026-03-06T10:00:00.000Z] [INFO] [feature-dev] Starting implementation...
   *
   * @param line - Raw log line string
   * @returns Parsed entry or null if line is empty/malformed
   *
   * @see Issue #1352 - Output window replay persisted log
   */
  static parseLogLine(line: string): LogFileEntry | null {
    const trimmed = line.trim();
    if (!trimmed) return null;

    // Match: [timestamp] [level] (optional: [stage]) message
    const match = trimmed.match(/^\[([^\]]+)\] \[([^\]]+)\] (?:\[([^\]]+)\] )?(.+)$/);
    if (!match) return null;

    const [, ts, level, stage, text] = match;
    const timestamp = new Date(ts);
    if (isNaN(timestamp.getTime())) return null;

    return { timestamp, level, stage: stage ?? null, text };
  }

  /**
   * Truncate a message for disk logging
   *
   * Preserves the first N characters and appends a size metadata suffix.
   * Messages under the limit are returned unchanged.
   *
   * @param message - The message to potentially truncate
   * @param maxChars - Maximum characters to keep (default: 200)
   * @returns Original message if under limit, or truncated with size suffix
   *
   * @see Issue #770 - Reduce session log bloat
   */
  static truncateForLog(message: string, maxChars: number = 200): string {
    if (message.length <= maxChars) {
      return message;
    }
    const totalSize = message.length;
    const sizeLabel =
      totalSize > 1024 ? `${(totalSize / 1024).toFixed(1)}KB` : `${totalSize} chars`;
    return `${message.slice(0, maxChars)}... [truncated, ${sizeLabel} total]`;
  }

  /**
   * Format a log entry line (without writing to disk)
   *
   * Useful for testing or creating entries for batch writes.
   *
   * @param level - Log level
   * @param stage - Pipeline stage or null
   * @param message - Log message
   * @param timestamp - Optional timestamp override
   * @returns Formatted log line
   */
  static formatEntry(
    level: string,
    stage: string | null,
    message: string,
    timestamp?: Date
  ): string {
    const ts = (timestamp ?? new Date()).toISOString();
    const stageTag = stage ? `[${stage}] ` : "";
    return `[${ts}] [${level.toUpperCase()}] ${stageTag}${message}`;
  }
}
