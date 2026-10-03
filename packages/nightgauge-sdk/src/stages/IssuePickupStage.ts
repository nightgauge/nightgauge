/**
 * IssuePickupStage - First stage in the Nightgauge pipeline
 *
 * Reads a GitHub issue, extracts requirements, and creates a feature branch.
 * This is the entry point of the pipeline - it has no input context file.
 *
 * @see skills/nightgauge-issue-pickup/SKILL.md for full workflow documentation
 * @see docs/CONTEXT_ARCHITECTURE.md for output schema
 */

import { BaseStage, type StageConfig } from "./base.js";
import { IssueContextSchema, type IssueContext } from "../context/schemas/index.js";

/**
 * IssuePickupStage - Reads issue, creates branch, outputs issue context
 *
 * @example
 * ```typescript
 * const stage = new IssuePickupStage();
 * const result = await stage.execute(executor, contextManager, {
 *   issueNumber: 42,
 * });
 *
 * if (result.success) {
 *   console.log(`Branch created: ${result.output?.branch}`);
 * }
 * ```
 */
export class IssuePickupStage extends BaseStage<void, IssueContext> {
  readonly config: StageConfig<void, IssueContext> = {
    name: "issue-pickup",
    skillPath: "skills/nightgauge-issue-pickup/SKILL.md",
    outputSchema: IssueContextSchema,
    outputContextType: "issue",
    // No inputSchema or inputContextType - this is the first stage
  };

  /**
   * Override buildPrompt to customize for issue pickup
   * (no input context, but includes issue number prominently)
   */
  protected override async buildPrompt(
    issueNumber: number,
    _inputContext: void | undefined,
    skillContent: string
  ): Promise<string> {
    return `# Pipeline Stage: issue-pickup

## Issue
Pick up issue #${issueNumber}

## Skill Instructions

${skillContent}

## Execution Requirements

1. Follow the skill instructions exactly for issue #${issueNumber}
2. Create the feature branch following the naming convention
3. Write the output context file with \`nightgauge layout write pipeline issue-${issueNumber}.json\` (JSON on stdin); never write under the git directory by path
4. The context file must include all required fields from the schema
5. Ensure the branch is pushed to the remote`;
  }
}
