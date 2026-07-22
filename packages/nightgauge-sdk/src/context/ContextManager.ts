/**
 * ContextManager - JSON file persistence with Zod validation
 *
 * Handles reading and writing pipeline context files.
 * Ensures type safety through Zod schema validation.
 *
 * @see docs/CONTEXT_ARCHITECTURE.md for file format specifications
 */

import { z } from "zod";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import * as crypto from "node:crypto";
import {
  EpicContextSchema,
  SubIssueFindingsSchema,
  type EpicContext,
  type SubIssueFindings,
} from "./schemas/epic-context.js";
import { SchemaVersionMismatch } from "../errors/PipelineStateErrors.js";

/**
 * Major.minor reader expectation for every context file under
 * .nightgauge/pipeline/. Bump the minor when adding optional fields;
 * bump the major when changing required fields incompatibly.
 *
 * @see docs/PIPELINE_STATE_SCHEMA.md
 */
export const READER_SCHEMA_VERSION = "1.0";

/**
 * Error thrown when a required context file is missing
 */
export class ContextNotFoundError extends Error {
  constructor(
    public readonly filename: string,
    public readonly createdBy: string
  ) {
    super(`Missing context file: ${filename}\nCreated by: ${createdBy}`);
    this.name = "ContextNotFoundError";
  }
}

/**
 * Error thrown when context file validation fails
 */
export class ContextValidationError extends Error {
  constructor(
    public readonly filename: string,
    public readonly zodError: z.ZodError
  ) {
    super(`Invalid context file: ${filename}\n${zodError.message}`);
    this.name = "ContextValidationError";
  }
}

/**
 * ContextManager class for pipeline context file I/O
 *
 * @example
 * ```typescript
 * const ctx = new ContextManager('.nightgauge/pipeline');
 *
 * // Read and validate issue context
 * const issue = await ctx.read(IssueContextSchema, 'issue-42.json');
 *
 * // Write planning context
 * await ctx.write(PlanningContextSchema, 'planning-42.json', planningData);
 *
 * // Cleanup after merge
 * await ctx.cleanup(42);
 * ```
 */
export class ContextManager {
  private basePath: string;

  constructor(basePath: string = ".nightgauge/pipeline") {
    this.basePath = basePath;
  }

  /**
   * Get the full path for a context file
   */
  private getFilePath(filename: string): string {
    return path.join(this.basePath, filename);
  }

  /**
   * Ensure the context directory exists
   */
  private async ensureDirectory(): Promise<void> {
    await fs.mkdir(this.basePath, { recursive: true });
  }

  /**
   * Read and validate a context file
   *
   * @throws {ContextNotFoundError} If the file doesn't exist
   * @throws {ContextValidationError} If validation fails
   */
  async read<T>(schema: z.ZodSchema<T>, filename: string): Promise<T> {
    const filePath = this.getFilePath(filename);

    let content: string;
    try {
      content = await fs.readFile(filePath, "utf-8");
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === "ENOENT") {
        const createdBy = this.inferCreatedBy(filename);
        throw new ContextNotFoundError(filePath, createdBy);
      }
      throw error;
    }

    let data: unknown;
    try {
      data = JSON.parse(content);
    } catch {
      throw new Error(`Failed to parse JSON in ${filePath}`);
    }

    // Schema-version gate (Issue #3238): a missing schema_version is treated
    // as "1.0" for backwards compatibility with pre-Gap-1 files; a major
    // bump is a hard error pointing at the migration doc. Per-schema minor
    // versions are tolerated by the individual Zod schemas via .nullish()
    // on added fields, so the ContextManager only enforces the major gate
    // here. Strict minor checking lives on run-state.json (RunStateManager).
    //
    // A malformed (non-numeric) schema_version is left to the Zod regex on
    // each schema to catch via the normal ContextValidationError path —
    // the major-version gate only fires on parseable-but-incompatible majors.
    const versionField = (data as { schema_version?: unknown })?.schema_version;
    const fileVersion = typeof versionField === "string" ? versionField : "1.0";
    const fileMajor = parseInt(fileVersion.split(".")[0] ?? "0", 10);
    const readerMajor = parseInt(READER_SCHEMA_VERSION.split(".")[0] ?? "0", 10);
    if (!Number.isNaN(fileMajor) && fileMajor !== readerMajor) {
      throw new SchemaVersionMismatch(filePath, fileVersion, readerMajor);
    }

    const result = schema.safeParse(data);
    if (!result.success) {
      throw new ContextValidationError(filePath, result.error);
    }

    return result.data;
  }

  /**
   * Write a validated context file
   *
   * @throws {ContextValidationError} If the data doesn't match the schema
   */
  async write<T>(schema: z.ZodSchema<T>, filename: string, data: T): Promise<void> {
    const result = schema.safeParse(data);
    if (!result.success) {
      throw new ContextValidationError(filename, result.error);
    }

    await this.ensureDirectory();
    const filePath = this.getFilePath(filename);
    const content = JSON.stringify(result.data, null, 2) + "\n";
    await atomicWriteJSON(filePath, content);
  }

  /**
   * Check if a context file exists
   */
  async exists(filename: string): Promise<boolean> {
    const filePath = this.getFilePath(filename);
    try {
      await fs.access(filePath);
      return true;
    } catch {
      return false;
    }
  }

  /**
   * Delete a specific context file
   */
  async delete(filename: string): Promise<void> {
    const filePath = this.getFilePath(filename);
    try {
      await fs.unlink(filePath);
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ENOENT") {
        throw error;
      }
    }
  }

  /**
   * Cleanup all context files for an issue
   *
   * Removes: issue-{N}.json, planning-{N}.json, dev-{N}.json,
   * validate-{N}.json, pr-{N}.json
   */
  async cleanup(issueNumber: number): Promise<string[]> {
    const files = [
      `issue-${issueNumber}.json`,
      `planning-${issueNumber}.json`,
      `dev-${issueNumber}.json`,
      `validate-${issueNumber}.json`,
      `pr-${issueNumber}.json`,
    ];

    const deleted: string[] = [];

    for (const file of files) {
      if (await this.exists(file)) {
        await this.delete(file);
        deleted.push(file);
      }
    }

    return deleted;
  }

  /**
   * List all context files matching a pattern
   */
  async list(pattern?: RegExp): Promise<string[]> {
    try {
      const files = await fs.readdir(this.basePath);
      if (pattern) {
        return files.filter((f) => pattern.test(f));
      }
      return files.filter((f) => f.endsWith(".json"));
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === "ENOENT") {
        return [];
      }
      throw error;
    }
  }

  /**
   * Cleanup all batch context files for an epic
   *
   * Removes: batch-{N}.json, planning-batch-{N}.json, dev-batch-{N}.json
   *
   * @see Issue #801 - Multi-issue context schema for batched pipeline runs
   */
  async cleanupBatch(epicNumber: number): Promise<string[]> {
    const files = [
      `batch-${epicNumber}.json`,
      `planning-batch-${epicNumber}.json`,
      `dev-batch-${epicNumber}.json`,
    ];

    const deleted: string[] = [];

    for (const file of files) {
      if (await this.exists(file)) {
        await this.delete(file);
        deleted.push(file);
      }
    }

    return deleted;
  }

  /**
   * Get the standard filename for a context type and issue/epic
   *
   * For batch types (batch, planning-batch, dev-batch), the number parameter
   * represents the epic number. For all other types, it represents the issue number.
   *
   * @see Issue #801 - Batch type support
   */
  static getFilename(
    type:
      | "issue"
      | "planning"
      | "dev"
      | "validate"
      | "pr"
      | "batch"
      | "planning-batch"
      | "dev-batch"
      | "epic-context",
    number: number
  ): string {
    return `${type}-${number}.json`;
  }

  /**
   * Infer which skill created a context file based on its name
   */
  private inferCreatedBy(filename: string): string {
    // Batch prefixes must be checked before single-issue prefixes
    // to avoid 'batch-' matching 'dev-' or 'planning-' prematurely
    if (filename.startsWith("batch-")) {
      return "/nightgauge-issue-pickup (batch)";
    }
    if (filename.startsWith("planning-batch-")) {
      return "/nightgauge-feature-planning (batch)";
    }
    if (filename.startsWith("dev-batch-")) {
      return "/nightgauge-feature-dev (batch)";
    }
    if (filename.startsWith("issue-")) {
      return "/nightgauge-issue-pickup";
    }
    if (filename.startsWith("planning-")) {
      return "/nightgauge-feature-planning";
    }
    if (filename.startsWith("dev-")) {
      return "/nightgauge-feature-dev";
    }
    if (filename.startsWith("validate-")) {
      return "/nightgauge-feature-validate";
    }
    if (filename.startsWith("pr-")) {
      return "/nightgauge-pr-create";
    }
    if (filename.startsWith("epic-")) {
      return "wave orchestrator (epic context)";
    }
    return "unknown skill";
  }

  /**
   * Read the epic-level shared context file.
   * Returns undefined if no epic context exists yet (first sub-issue).
   *
   * @see Issue #2404 - Epic context accumulator
   */
  async readEpicContext(epicNumber: number): Promise<EpicContext | undefined> {
    const filename = `epic-context-${epicNumber}.json`;
    const filePath = this.getFilePath(filename);
    try {
      const content = await fs.readFile(filePath, "utf-8");
      const parsed = JSON.parse(content);
      return EpicContextSchema.parse(parsed);
    } catch {
      return undefined; // File doesn't exist yet — first sub-issue
    }
  }

  /**
   * Append findings to the epic-level shared context.
   * Merges with existing content — never overwrites other sub-issues' findings.
   *
   * Uses atomic write (temp file + rename) consistent with write().
   *
   * @see Issue #2404 - Epic context accumulator
   */
  async appendEpicContext(
    epicNumber: number,
    issueNumber: number,
    findings: SubIssueFindings
  ): Promise<void> {
    // Validate findings before doing anything
    SubIssueFindingsSchema.parse(findings);

    const existing = (await this.readEpicContext(epicNumber)) ?? {
      schema_version: "1.0" as const,
      epic_number: epicNumber,
      last_updated: new Date().toISOString(),
      sub_issue_findings: {},
      shared_research: {
        codebase_notes: [],
        architecture_notes: [],
        relevant_files: [],
      },
    };

    // Append this sub-issue's findings (keyed by issue number)
    existing.sub_issue_findings[String(issueNumber)] = findings;
    existing.last_updated = new Date().toISOString();

    // Merge relevant files (deduplicate)
    if (findings.files_touched.length > 0) {
      const allFiles = new Set([
        ...existing.shared_research.relevant_files,
        ...findings.files_touched,
      ]);
      existing.shared_research.relevant_files = [...allFiles];
    }

    // Write atomically using the same pattern as write()
    await this.ensureDirectory();
    const filename = `epic-context-${epicNumber}.json`;
    const filePath = this.getFilePath(filename);
    const content = JSON.stringify(existing, null, 2) + "\n";

    await atomicWriteJSON(filePath, content);
  }

  /**
   * Get the base path for context files
   */
  getBasePath(): string {
    return this.basePath;
  }
}

/**
 * Atomic + fsync write contract: write-temp → fsync(file) → rename → fsync(parent dir)
 *
 * Used for every JSON write under .nightgauge/pipeline/. Guarantees that
 * a reader observes either the prior version or the new version — never
 * partial JSON, even on power loss between rename and the next disk flush.
 *
 * Directory fsync is best-effort: macOS treats it as a no-op and Windows
 * disallows opening directories as files. EISDIR/EINVAL/ENOTSUP/EPERM are
 * swallowed; any other error propagates.
 *
 * @see ADR-004 in .nightgauge/knowledge/features/3238-graceful-pipeline-stop-with-durable/decisions.md
 */
export async function atomicWriteJSON(filePath: string, content: string): Promise<void> {
  await fs.mkdir(path.dirname(filePath), { recursive: true });
  const tempPath = `${filePath}.${crypto.randomUUID()}.tmp`;
  try {
    const fh = await fs.open(tempPath, "w");
    try {
      await fh.writeFile(content, "utf-8");
      await fh.sync();
    } finally {
      await fh.close();
    }
    await fs.rename(tempPath, filePath);
    await fsyncDir(path.dirname(filePath));
  } catch (error) {
    await fs.unlink(tempPath).catch(() => {});
    throw error;
  }
}

async function fsyncDir(dirPath: string): Promise<void> {
  let dh: fs.FileHandle | undefined;
  try {
    dh = await fs.open(dirPath, "r");
    await dh.sync();
  } catch (error) {
    const code = (error as NodeJS.ErrnoException).code;
    // macOS / Windows / certain FUSE filesystems do not permit fsync on a
    // directory FD. Those cases are documented as no-ops, not failures.
    if (code === "EISDIR" || code === "EINVAL" || code === "ENOTSUP" || code === "EPERM") {
      return;
    }
    throw error;
  } finally {
    if (dh) await dh.close().catch(() => {});
  }
}
