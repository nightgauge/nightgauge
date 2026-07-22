/**
 * CodexContextGenerator — provisions baseline system steering for Codex stages.
 *
 * Codex takes system-level guidance from AGENTS.md (project root or CODEX_HOME),
 * the way Claude Code takes it from the `claude_code` SDK preset and Gemini from
 * GEMINI.md. This generator writes the same provider-neutral baseline steering
 * (standards, security, git workflow, key rules + the current task) that the
 * Claude preset would supply, so Codex stages are not left unguided.
 *
 * Unlike GEMINI.md (a fully generated, disposable file), AGENTS.md is commonly a
 * COMMITTED, user-authored file. So this generator never clobbers it: it writes
 * its content into a clearly-delimited MANAGED BLOCK and, on cleanup, removes
 * only that block — user content outside the markers is preserved byte-for-byte,
 * and a file that was purely generated is removed entirely.
 *
 * @see Issue #4028 - Provider-aware system steering (Codex AGENTS.md)
 */

import * as fs from "node:fs";
import * as fsPromises from "node:fs/promises";
import * as path from "node:path";
import {
  readProjectDescription,
  readStandards,
  readSecurity,
  readGitWorkflow,
  upsertManagedBlock,
  stripManagedBlock,
  CODEX_MANAGED_BEGIN,
} from "./steeringSources.js";

// Re-exported so callers (the SDK barrel, tests) can reach the managed-block
// markers/helpers via the Codex generator that owns the AGENTS.md contract,
// while the implementations live provider-neutrally in steeringSources. #4028
export {
  CODEX_MANAGED_BEGIN,
  CODEX_MANAGED_END,
  upsertManagedBlock,
  stripManagedBlock,
} from "./steeringSources.js";

/** Options for generating AGENTS.md steering. */
export interface CodexContextOptions {
  /** Project root directory (where AGENTS.md lives / will be written). */
  projectRoot: string;
  /** Current pipeline stage name. */
  stage: string;
  /** Issue number being worked on. */
  issueNumber: number;
  /** Issue title (optional). */
  issueTitle?: string;
  /** Acceptance criteria from issue context (optional). */
  acceptanceCriteria?: string[];
  /** Adapter name (used for the guard check). */
  adapter: string;
}

/**
 * Configuration for AGENTS.md steering. Read from `.nightgauge/config.yaml`
 * under `pipeline.codex_context` (mirrors `pipeline.gemini_context`).
 */
export interface CodexContextConfig {
  /** Whether AGENTS.md steering provisioning is enabled (default: true). */
  enabled?: boolean;
  /** Include coding standards section (default: true). */
  include_standards?: boolean;
  /** Include git workflow section (default: true). */
  include_git_workflow?: boolean;
  /** Additional custom sections to append inside the managed block. */
  custom_sections?: Array<{ heading: string; content: string }>;
}

/**
 * Read a file and return its contents, or null if it doesn't exist.
 */
function readFileOrNull(filePath: string): string | null {
  try {
    return fs.readFileSync(filePath, "utf-8");
  } catch {
    return null;
  }
}

/**
 * Process-scoped reference count of active managed-block provisioners per
 * AGENTS.md path. The block is a SINGLE shared region in one file, so when two
 * Codex stages run concurrently against the same project root (e.g. a headless
 * stage and an interactive TUI session, #4024) the first one to finish must NOT
 * strip the block out from under the other. Cleanup is therefore refcounted:
 * `generate` retains, `cleanup` releases, and the block is only stripped when
 * the last provisioner releases. (#4024 review #3)
 */
const managedBlockRefs = new Map<string, number>();

function retainManagedBlock(filePath: string): void {
  const key = path.resolve(filePath);
  managedBlockRefs.set(key, (managedBlockRefs.get(key) ?? 0) + 1);
}

/** Release one reference; returns true when the caller should actually strip. */
function releaseManagedBlock(filePath: string): boolean {
  const key = path.resolve(filePath);
  const n = managedBlockRefs.get(key) ?? 0;
  if (n > 1) {
    managedBlockRefs.set(key, n - 1);
    return false; // another provisioner still holds the block
  }
  managedBlockRefs.delete(key);
  return true;
}

export class CodexContextGenerator {
  /**
   * Provision AGENTS.md steering if the adapter is Codex.
   *
   * @returns The file path if provisioned, null if skipped (non-Codex / disabled).
   */
  async generate(
    options: CodexContextOptions,
    config?: CodexContextConfig
  ): Promise<string | null> {
    if (!this.isCodexAdapter(options.adapter) || config?.enabled === false) {
      return null;
    }

    const filePath = path.join(options.projectRoot, "AGENTS.md");
    const next = upsertManagedBlock(
      readFileOrNull(filePath),
      this.assembleContent(options, config)
    );
    await fsPromises.writeFile(filePath, next, "utf-8");
    retainManagedBlock(filePath);
    return filePath;
  }

  /**
   * Synchronous provisioning for use in skillRunner (returns a handle synchronously).
   */
  generateSync(options: CodexContextOptions, config?: CodexContextConfig): string | null {
    if (!this.isCodexAdapter(options.adapter) || config?.enabled === false) {
      return null;
    }

    const filePath = path.join(options.projectRoot, "AGENTS.md");
    const next = upsertManagedBlock(
      readFileOrNull(filePath),
      this.assembleContent(options, config)
    );
    fs.writeFileSync(filePath, next, "utf-8");
    retainManagedBlock(filePath);
    return filePath;
  }

  /**
   * Remove the managed block after stage completion, preserving any user content.
   * Deletes AGENTS.md only when it held nothing but the generated block.
   *
   * Refcounted (#4024 review #3): a release while another provisioner still holds
   * the block is a no-op, so a concurrent same-root Codex stage keeps its steering.
   */
  async cleanup(projectRoot: string): Promise<void> {
    const filePath = path.join(projectRoot, "AGENTS.md");
    if (!releaseManagedBlock(filePath)) {
      return;
    }
    const existing = readFileOrNull(filePath);
    if (existing === null || !existing.includes(CODEX_MANAGED_BEGIN)) {
      // No file, or a user-authored file we never touched — leave it alone.
      return;
    }

    const stripped = stripManagedBlock(existing);
    if (stripped.trim().length === 0) {
      await fsPromises.unlink(filePath).catch(() => {});
      return;
    }
    await fsPromises.writeFile(filePath, stripped, "utf-8");
  }

  /**
   * Synchronous cleanup for use in skillRunner's process-close handler, which is
   * synchronous. Same non-destructive semantics as {@link cleanup}.
   */
  cleanupSync(projectRoot: string): void {
    const filePath = path.join(projectRoot, "AGENTS.md");
    if (!releaseManagedBlock(filePath)) {
      return;
    }
    const existing = readFileOrNull(filePath);
    if (existing === null || !existing.includes(CODEX_MANAGED_BEGIN)) {
      return;
    }

    const stripped = stripManagedBlock(existing);
    if (stripped.trim().length === 0) {
      try {
        fs.unlinkSync(filePath);
      } catch {
        /* best-effort */
      }
      return;
    }
    fs.writeFileSync(filePath, stripped, "utf-8");
  }

  /**
   * Assemble the inner content of the managed block (the steering itself).
   * Pure given the same options + file system state.
   */
  assembleContent(options: CodexContextOptions, config?: CodexContextConfig): string {
    const sections: string[] = [];

    sections.push("# Nightgauge Pipeline Steering (Codex)\n");
    sections.push(
      "_This block is managed by the Nightgauge pipeline. Edits inside the_\n" +
        "_markers are overwritten; add your own guidance outside them._\n"
    );

    // Shared reader strips any managed block first, so we never read our own
    // output back as the description (would otherwise duplicate markers). #4028
    const projectDesc = readProjectDescription(options.projectRoot);
    if (projectDesc) {
      sections.push("## Project\n");
      sections.push(projectDesc + "\n");
    }

    if (config?.include_standards !== false) {
      const standards = readStandards(options.projectRoot);
      if (standards) {
        sections.push("## Coding Standards\n");
        sections.push(standards + "\n");
      }
    }

    const security = readSecurity(options.projectRoot);
    if (security) {
      sections.push("## Security\n");
      sections.push(security + "\n");
    }

    if (config?.include_git_workflow !== false) {
      const gitWorkflow = readGitWorkflow(options.projectRoot);
      if (gitWorkflow) {
        sections.push("## Git Workflow\n");
        sections.push(gitWorkflow + "\n");
      }
    }

    // Intentionally STABLE: no per-issue Stage/Issue/Acceptance-Criteria here.
    // AGENTS.md may be a committed file, so the managed block stays idempotent
    // and commit-safe; the per-issue task context is delivered via the prompt
    // (this block is the preset-equivalent BASELINE steering, not the task).
    sections.push("## Key Rules\n");
    sections.push("- Never push directly to main");
    sections.push("- Never hardcode secrets");
    sections.push("- Follow existing patterns in the codebase");
    sections.push("");

    if (config?.custom_sections && config.custom_sections.length > 0) {
      for (const section of config.custom_sections) {
        sections.push(`## ${section.heading}\n`);
        sections.push(section.content + "\n");
      }
    }

    return sections.join("\n").trimEnd();
  }

  /**
   * Check if the adapter is Codex.
   */
  private isCodexAdapter(adapter: string): boolean {
    return adapter === "codex";
  }
}
