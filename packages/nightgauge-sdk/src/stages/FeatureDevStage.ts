/**
 * FeatureDevStage - Implements features following approved plans
 *
 * Reads the planning context and PLAN.md, then implements the feature
 * following documented standards. Writes code, tests, and commits changes.
 *
 * @see skills/nightgauge-feature-dev/SKILL.md for full workflow documentation
 * @see docs/CONTEXT_ARCHITECTURE.md for input/output schemas
 */

import { BaseStage, type StageConfig } from "./base.js";
import {
  PlanningContextSchema,
  DevContextSchema,
  type PlanningContext,
  type DevContext,
} from "../context/schemas/index.js";

/**
 * FeatureDevStage - Implements plan, writes code, runs tests
 *
 * @example
 * ```typescript
 * const stage = new FeatureDevStage();
 * const result = await stage.execute(executor, contextManager, {
 *   issueNumber: 42,
 * });
 *
 * if (result.success) {
 *   console.log(`Commit: ${result.output?.commit_sha}`);
 *   console.log(`Tests: ${result.output?.tests_status.passed} passed`);
 * }
 * ```
 */
export class FeatureDevStage extends BaseStage<PlanningContext, DevContext> {
  readonly config: StageConfig<PlanningContext, DevContext> = {
    name: "feature-dev",
    skillPath: "skills/nightgauge-feature-dev/SKILL.md",
    inputSchema: PlanningContextSchema,
    outputSchema: DevContextSchema,
    inputContextType: "planning",
    outputContextType: "dev",
  };

  /**
   * Override buildPrompt to include plan file reference
   */
  protected override async buildPrompt(
    issueNumber: number,
    inputContext?: PlanningContext,
    skillsBasePath: string = "skills"
  ): Promise<string> {
    const skillPath = `${skillsBasePath}/nightgauge-feature-dev/SKILL.md`;

    let skillContent: string;
    try {
      const fs = await import("node:fs/promises");
      skillContent = await fs.readFile(skillPath, "utf-8");
    } catch {
      throw new Error(`Failed to read skill file: ${skillPath}`);
    }

    const sections: string[] = [
      "# Pipeline Stage: feature-dev",
      "",
      "## Issue",
      `Issue number: ${issueNumber}`,
    ];

    if (inputContext) {
      sections.push("");
      sections.push("## Planning Context");
      sections.push(`Plan file: ${inputContext.plan_file}`);
      sections.push(`Approach: ${inputContext.approach}`);
      sections.push("");
      sections.push("### Files to Create");
      for (const file of inputContext.files_to_create ?? []) {
        sections.push(`- ${file}`);
      }
      sections.push("");
      sections.push("### Files to Modify");
      for (const file of inputContext.files_to_modify ?? []) {
        sections.push(`- ${file}`);
      }
      if (inputContext.patterns_applied) {
        sections.push("");
        sections.push("### Patterns Applied");
        for (const [key, value] of Object.entries(inputContext.patterns_applied)) {
          sections.push(`- ${key}: ${value}`);
        }
      }
      if (inputContext.coverage_baseline) {
        sections.push("");
        sections.push("### Coverage Baseline");
        const cb = inputContext.coverage_baseline;
        if (cb.statements !== undefined) sections.push(`- Statements: ${cb.statements}%`);
        if (cb.branches !== undefined) sections.push(`- Branches: ${cb.branches}%`);
        if (cb.lines !== undefined) sections.push(`- Lines: ${cb.lines}%`);
      }
    }

    sections.push("");
    sections.push("## Skill Instructions");
    sections.push("");
    sections.push(skillContent);
    sections.push("");
    sections.push("## Execution Requirements");
    sections.push("");
    sections.push("1. Read and follow the PLAN.md file exactly");
    sections.push("2. Create all files listed in files_to_create");
    sections.push("3. Modify all files listed in files_to_modify");
    sections.push("4. Write tests alongside implementation");
    sections.push("5. Run tests and ensure they pass");
    sections.push("6. Commit changes with proper message format");
    sections.push(`7. Write the context file to .nightgauge/pipeline/dev-${issueNumber}.json`);
    sections.push("8. The context file must include commit SHA, files changed, and test status");

    return sections.join("\n");
  }
}
