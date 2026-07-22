/**
 * PRCreateStage - Creates pull requests with proper format
 *
 * Reads the dev context and creates a PR with proper title format,
 * issue linking, and review requests.
 *
 * @see skills/nightgauge-pr-create/SKILL.md for full workflow documentation
 * @see docs/CONTEXT_ARCHITECTURE.md for input/output schemas
 */

import { BaseStage, type StageConfig } from "./base.js";
import {
  DevContextSchema,
  PRContextSchema,
  type DevContext,
  type PRContext,
} from "../context/schemas/index.js";

/**
 * PRCreateStage - Creates PR with proper format
 *
 * @example
 * ```typescript
 * const stage = new PRCreateStage();
 * const result = await stage.execute(executor, contextManager, {
 *   issueNumber: 42,
 * });
 *
 * if (result.success) {
 *   console.log(`PR created: ${result.output?.pr_url}`);
 * }
 * ```
 */
export class PRCreateStage extends BaseStage<DevContext, PRContext> {
  readonly config: StageConfig<DevContext, PRContext> = {
    name: "pr-create",
    skillPath: "skills/nightgauge-pr-create/SKILL.md",
    inputSchema: DevContextSchema,
    outputSchema: PRContextSchema,
    inputContextType: "dev",
    outputContextType: "pr",
  };

  /**
   * Override buildPrompt to include dev context summary
   */
  protected override async buildPrompt(
    issueNumber: number,
    inputContext?: DevContext,
    skillsBasePath: string = "skills"
  ): Promise<string> {
    const skillPath = `${skillsBasePath}/nightgauge-pr-create/SKILL.md`;

    let skillContent: string;
    try {
      const fs = await import("node:fs/promises");
      skillContent = await fs.readFile(skillPath, "utf-8");
    } catch {
      throw new Error(`Failed to read skill file: ${skillPath}`);
    }

    const sections: string[] = [
      "# Pipeline Stage: pr-create",
      "",
      "## Issue",
      `Issue number: ${issueNumber}`,
    ];

    if (inputContext) {
      sections.push("");
      sections.push("## Dev Context");
      sections.push(`Commit SHA: ${inputContext.commit_sha}`);
      sections.push("");
      sections.push("### Files Changed");
      if (inputContext.files_changed) {
        if (inputContext.files_changed.created.length > 0) {
          sections.push("**Created:**");
          for (const file of inputContext.files_changed.created) {
            sections.push(`- ${file}`);
          }
        }
        if (inputContext.files_changed.modified.length > 0) {
          sections.push("**Modified:**");
          for (const file of inputContext.files_changed.modified) {
            sections.push(`- ${file}`);
          }
        }
        if (inputContext.files_changed.deleted.length > 0) {
          sections.push("**Deleted:**");
          for (const file of inputContext.files_changed.deleted) {
            sections.push(`- ${file}`);
          }
        }
      }
      sections.push("");
      sections.push("### Test Status");
      if (inputContext.tests_status) {
        sections.push(`- Passed: ${inputContext.tests_status.passed}`);
        sections.push(`- Failed: ${inputContext.tests_status.failed}`);
        if (inputContext.tests_status.coverage !== undefined) {
          sections.push(`- Coverage: ${inputContext.tests_status.coverage}%`);
        }
      }
      sections.push("");
      sections.push("### Quality Checks");
      if (inputContext.quality_checks) {
        sections.push(`- Code standards: ${inputContext.quality_checks.code_standards}`);
        sections.push(`- Security review: ${inputContext.quality_checks.security_review}`);
      }
    }

    sections.push("");
    sections.push("## Skill Instructions");
    sections.push("");
    sections.push(skillContent);
    sections.push("");
    sections.push("## Execution Requirements");
    sections.push("");
    sections.push("1. Run pre-flight checks (JSON/YAML validation, version consistency)");
    sections.push("2. Generate PR title with proper format: [TYPE][#issue] Description");
    sections.push("3. Generate comprehensive PR description");
    sections.push(`4. Create PR that closes #${issueNumber}`);
    sections.push("5. Request appropriate reviewers");
    sections.push(`6. Write the context file to .nightgauge/pipeline/pr-${issueNumber}.json`);
    sections.push("7. The context file must include PR number, URL, and preflight results");

    return sections.join("\n");
  }
}
