/**
 * GeminiContextGenerator - Generates GEMINI.md context file for Gemini CLI
 *
 * Gemini CLI automatically reads GEMINI.md from the working directory for
 * project context (analogous to how Claude Code reads CLAUDE.md). This class
 * generates that file before Gemini-based stage execution, assembling content
 * from project standards, issue context, and stage instructions.
 *
 * @see Issue #1055 - Add GEMINI.md context file generation
 */

import * as fs from "node:fs";
import * as fsPromises from "node:fs/promises";
import * as path from "node:path";
import {
  readProjectDescription,
  readStandards,
  readSecurity,
  readGitWorkflow,
} from "./steeringSources.js";

/**
 * Options for generating GEMINI.md context
 */
export interface GeminiContextOptions {
  /** Project root directory (where GEMINI.md will be written) */
  projectRoot: string;
  /** Current pipeline stage name */
  stage: string;
  /** Issue number being worked on */
  issueNumber: number;
  /** Issue title (optional) */
  issueTitle?: string;
  /** Acceptance criteria from issue context (optional) */
  acceptanceCriteria?: string[];
  /** Adapter name (used for guard check) */
  adapter: string;
}

/**
 * Configuration for GEMINI.md template customization
 *
 * Read from `.nightgauge/config.yaml` under `pipeline.gemini_context`.
 */
export interface GeminiContextConfig {
  /** Whether GEMINI.md generation is enabled (default: true) */
  enabled?: boolean;
  /** Include coding standards section (default: true) */
  include_standards?: boolean;
  /** Include git workflow section (default: true) */
  include_git_workflow?: boolean;
  /** Additional custom sections to append */
  custom_sections?: Array<{ heading: string; content: string }>;
}

export class GeminiContextGenerator {
  /**
   * Generate GEMINI.md if the adapter is Gemini-based.
   *
   * @returns The file path if generated, null if skipped (non-Gemini adapter)
   */
  async generate(
    options: GeminiContextOptions,
    config?: GeminiContextConfig
  ): Promise<string | null> {
    if (!this.isGeminiAdapter(options.adapter)) {
      return null;
    }

    if (config?.enabled === false) {
      return null;
    }

    const content = this.assembleContent(options, config);
    const filePath = path.join(options.projectRoot, "GEMINI.md");
    await fsPromises.writeFile(filePath, content, "utf-8");
    return filePath;
  }

  /**
   * Synchronous generation for use in skillRunner (which returns a handle synchronously).
   *
   * @returns The file path if generated, null if skipped
   */
  generateSync(options: GeminiContextOptions, config?: GeminiContextConfig): string | null {
    if (!this.isGeminiAdapter(options.adapter)) {
      return null;
    }

    if (config?.enabled === false) {
      return null;
    }

    const content = this.assembleContent(options, config);
    const filePath = path.join(options.projectRoot, "GEMINI.md");
    fs.writeFileSync(filePath, content, "utf-8");
    return filePath;
  }

  /**
   * Remove GEMINI.md from the project root.
   */
  async cleanup(projectRoot: string): Promise<void> {
    const filePath = path.join(projectRoot, "GEMINI.md");
    try {
      await fsPromises.unlink(filePath);
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ENOENT") {
        throw error;
      }
    }
  }

  /**
   * Assemble the GEMINI.md content from project files and context.
   *
   * This is a pure function (given the same options and file system state,
   * produces the same output). Reads standards files from the project root.
   */
  assembleContent(options: GeminiContextOptions, config?: GeminiContextConfig): string {
    const sections: string[] = [];

    // Header
    sections.push("# Project Context for Gemini CLI\n");

    // Project description from CLAUDE.md or AGENTS.md
    const projectDesc = readProjectDescription(options.projectRoot);
    if (projectDesc) {
      sections.push("## Project\n");
      sections.push(projectDesc + "\n");
    }

    // Coding standards
    if (config?.include_standards !== false) {
      const standards = readStandards(options.projectRoot);
      if (standards) {
        sections.push("## Coding Standards\n");
        sections.push(standards + "\n");
      }
    }

    // Security rules
    const security = readSecurity(options.projectRoot);
    if (security) {
      sections.push("## Security\n");
      sections.push(security + "\n");
    }

    // Git workflow
    if (config?.include_git_workflow !== false) {
      const gitWorkflow = readGitWorkflow(options.projectRoot);
      if (gitWorkflow) {
        sections.push("## Git Workflow\n");
        sections.push(gitWorkflow + "\n");
      }
    }

    // Current task context
    sections.push("## Current Task\n");
    sections.push(`Stage: ${options.stage}`);
    if (options.issueTitle) {
      sections.push(`Issue: #${options.issueNumber} - ${options.issueTitle}\n`);
    } else {
      sections.push(`Issue: #${options.issueNumber}\n`);
    }

    // Acceptance criteria
    if (options.acceptanceCriteria && options.acceptanceCriteria.length > 0) {
      sections.push("### Acceptance Criteria\n");
      for (const criterion of options.acceptanceCriteria) {
        sections.push(`- ${criterion}`);
      }
      sections.push("");
    }

    // Key rules (always included)
    sections.push("## Key Rules\n");
    sections.push("- Never push directly to main");
    sections.push("- Never hardcode secrets");
    sections.push("- Follow existing patterns in the codebase");
    sections.push("");

    // Custom sections from config
    if (config?.custom_sections && config.custom_sections.length > 0) {
      for (const section of config.custom_sections) {
        sections.push(`## ${section.heading}\n`);
        sections.push(section.content + "\n");
      }
    }

    return sections.join("\n");
  }

  /**
   * Check if the adapter is a Gemini-based adapter.
   */
  private isGeminiAdapter(adapter: string): boolean {
    return adapter === "gemini" || adapter === "gemini-sdk";
  }
}
