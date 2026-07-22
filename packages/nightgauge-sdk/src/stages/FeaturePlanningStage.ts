/**
 * FeaturePlanningStage - Documentation-first feature planning
 *
 * Reads the issue context, explores documentation, and creates a PLAN.md
 * for user approval before implementation begins.
 *
 * This stage has requiresApproval=true, meaning the orchestrator should
 * emit an approval:needed event and wait for user confirmation.
 *
 * @see skills/nightgauge-feature-planning/SKILL.md for full workflow documentation
 * @see docs/CONTEXT_ARCHITECTURE.md for input/output schemas
 */

import {
  BaseStage,
  type StageConfig,
  type StageExecuteOptions,
  type StageExecuteResult,
} from "./base.js";
import {
  IssueContextSchema,
  PlanningContextSchema,
  type IssueContext,
  type PlanningContext,
} from "../context/schemas/index.js";
import { ContextValidationError } from "../context/ContextManager.js";
import type { StageExecutor } from "../orchestrator/StageExecutor.js";
import { ContextManager } from "../context/ContextManager.js";

/**
 * FeaturePlanningStage - Reads docs/, creates plan, requires approval
 *
 * @example
 * ```typescript
 * const stage = new FeaturePlanningStage();
 *
 * // Check if approval is needed
 * if (stage.requiresApproval()) {
 *   orchestrator.events.emit({
 *     type: 'approval:needed',
 *     stage: stage.getName(),
 *     reason: 'Plan requires review before implementation',
 *   });
 * }
 *
 * const result = await stage.execute(executor, contextManager, {
 *   issueNumber: 42,
 * });
 * ```
 */
export class FeaturePlanningStage extends BaseStage<IssueContext, PlanningContext> {
  readonly config: StageConfig<IssueContext, PlanningContext> = {
    name: "feature-planning",
    skillPath: "skills/nightgauge-feature-planning/SKILL.md",
    inputSchema: IssueContextSchema,
    outputSchema: PlanningContextSchema,
    inputContextType: "issue",
    outputContextType: "planning",
    requiresApproval: true,
  };

  /**
   * Override buildPrompt to include documentation-first instructions
   */
  protected override async buildPrompt(
    issueNumber: number,
    inputContext?: IssueContext,
    skillsBasePath: string = "skills"
  ): Promise<string> {
    const skillPath = `${skillsBasePath}/nightgauge-feature-planning/SKILL.md`;

    let skillContent: string;
    try {
      const fs = await import("node:fs/promises");
      skillContent = await fs.readFile(skillPath, "utf-8");
    } catch {
      throw new Error(`Failed to read skill file: ${skillPath}`);
    }

    const sections: string[] = [
      "# Pipeline Stage: feature-planning",
      "",
      "## Issue",
      `Issue number: ${issueNumber}`,
    ];

    if (inputContext) {
      sections.push("");
      sections.push("## Issue Context");
      sections.push(`Title: ${inputContext.title}`);
      sections.push(`Type: ${inputContext.type}`);
      sections.push(`Branch: ${inputContext.branch}`);
      sections.push("");
      sections.push("### Requirements");
      sections.push(inputContext.requirements.summary ?? "");
      if (inputContext.requirements.acceptance_criteria?.length) {
        sections.push("");
        sections.push("### Acceptance Criteria");
        for (const criterion of inputContext.requirements.acceptance_criteria) {
          sections.push(`- [ ] ${criterion}`);
        }
      }
      if (inputContext.requirements.technical_notes?.length) {
        sections.push("");
        sections.push("### Technical Notes");
        for (const note of inputContext.requirements.technical_notes) {
          sections.push(`- ${note}`);
        }
      }
    }

    sections.push("");
    sections.push("## Skill Instructions");
    sections.push("");
    sections.push(skillContent);
    sections.push("");
    sections.push("## Execution Requirements");
    sections.push("");
    sections.push("1. Follow the documentation-first approach");
    sections.push("2. Read docs/ files BEFORE exploring code");
    sections.push(`3. Create the plan file at .nightgauge/plans/${issueNumber}-*.md (required)`);
    sections.push(
      "4. Do NOT write PLAN.md at repository root; root PLAN.md is legacy/invalid for this stage"
    );
    sections.push(`5. Write the context file to .nightgauge/pipeline/planning-${issueNumber}.json`);
    sections.push("6. The context file must include all required fields from the schema");
    sections.push(
      "7. planning-{N}.json plan_file must point to the .nightgauge/plans path, not PLAN.md"
    );
    sections.push("8. Wait for user approval of the plan before indicating completion");

    return sections.join("\n");
  }

  /**
   * Override execute() to add a targeted repair pass when the written
   * planning-{N}.json fails schema validation due to serialization errors
   * (e.g. malformed pattern_mining_results or documentation_scope).
   *
   * Repair is attempted exactly once — if it fails, the original error is
   * returned unchanged.
   */
  override async execute(
    executor: StageExecutor,
    contextManager: ContextManager,
    options: StageExecuteOptions
  ): Promise<StageExecuteResult<PlanningContext>> {
    const result = await super.execute(executor, contextManager, options);

    if (result.success) return result;

    // Only attempt repair for schema validation failures
    if (!(result.error instanceof ContextValidationError)) return result;

    const validationError = result.error;
    const repairPrompt = this.buildRepairPrompt(options.issueNumber, validationError);

    try {
      const repairMessages = [];
      for await (const message of executor.execute({
        stage: this.config.name,
        issueNumber: options.issueNumber,
        prompt: repairPrompt,
        model: options.model,
        maxTurns: 5,
        cwd: options.cwd,
      })) {
        repairMessages.push(message);
      }

      const repairedOutput = await this.readOutputContext(contextManager, options.issueNumber);
      return {
        success: true,
        output: repairedOutput,
        messages: [...result.messages, ...repairMessages],
        durationMs: result.durationMs,
      };
    } catch {
      return result;
    }
  }

  /**
   * Build a targeted repair prompt from a ContextValidationError.
   * Includes flattened Zod field errors and annotated examples for the
   * fields most commonly emitted incorrectly by the planning subagent.
   */
  private buildRepairPrompt(issueNumber: number, error: ContextValidationError): string {
    const flat = error.zodError.flatten();
    const fieldErrors = JSON.stringify(flat.fieldErrors, null, 2);

    return [
      `# Repair Task: Fix planning-${issueNumber}.json schema validation errors`,
      "",
      "The planning context file you wrote failed schema validation.",
      "Fix ONLY the fields listed below — do not change any other content.",
      "",
      "## Validation errors",
      "```json",
      fieldErrors,
      "```",
      "",
      "## Correct shapes for commonly broken fields",
      "",
      "### pattern_mining_results.patterns_found — ARRAY OF OBJECTS (not strings)",
      "```json",
      JSON.stringify(
        [
          {
            pattern_type: "structural",
            category: "TypeScript",
            pattern: "description here",
            evidence: ["file.ts:10"],
            frequency: 3,
            example_implementations: ["ChangeTypeSchema"],
          },
        ],
        null,
        2
      ),
      "```",
      "",
      "### pattern_mining_results.pattern_classifications — OBJECT WITH INTEGER COUNTS (not array, not strings)",
      "```json",
      JSON.stringify(
        {
          naming_conventions: 2,
          structural_patterns: 3,
          interface_patterns: 1,
          idioms: 2,
        },
        null,
        2
      ),
      "```",
      "",
      'WRONG: `pattern_classifications: ["naming_conventions", "structural"]` (array)',
      "WRONG: `pattern_classifications: {naming: 2, structural: 3}` (wrong key names)",
      "",
      "### complexity_assessment.documentation_scope — one of: minimal | targeted | standard | extended",
      "",
      "## Instructions",
      `1. Read .nightgauge/pipeline/planning-${issueNumber}.json`,
      "2. Fix only the fields that appear in the validation errors above",
      "3. Write the corrected file back to the same path",
      `4. Verify with: jq . .nightgauge/pipeline/planning-${issueNumber}.json`,
    ].join("\n");
  }
}
