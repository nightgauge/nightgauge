/**
 * IssuePickupStage - First stage in the Nightgauge pipeline
 *
 * Reads a GitHub issue, extracts requirements, and creates a feature branch.
 * This is the entry point of the pipeline - it has no input context file.
 *
 * @see skills/nightgauge-issue-pickup/SKILL.md for full workflow documentation
 * @see docs/CONTEXT_ARCHITECTURE.md for output schema
 */

import {
  BaseStage,
  type StageConfig,
  type StageExecuteOptions,
  type StageExecuteResult,
} from "./base.js";
import { IssueContextSchema, type IssueContext } from "../context/schemas/index.js";
import type { ContextManager } from "../context/ContextManager.js";
import type { StageExecutor, SDKMessage } from "../orchestrator/StageExecutor.js";

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
   * Override execute to handle the case where there's no input context
   */
  async execute(
    executor: StageExecutor,
    contextManager: ContextManager,
    options: StageExecuteOptions
  ): Promise<StageExecuteResult<IssueContext>> {
    const startTime = Date.now();
    const messages: SDKMessage[] = [];

    try {
      // Build the prompt (no input context for this stage)
      const prompt = await this.buildPrompt(options.issueNumber, undefined, options.skillsBasePath);

      // Execute via StageExecutor
      for await (const message of executor.execute({
        stage: this.config.name,
        issueNumber: options.issueNumber,
        prompt,
        model: options.model,
        maxTurns: options.maxTurns,
        cwd: options.cwd,
      })) {
        messages.push(message);
      }

      // Read and validate output context
      const output = await this.readOutputContext(contextManager, options.issueNumber);

      return {
        success: true,
        output,
        messages,
        durationMs: Date.now() - startTime,
      };
    } catch (error) {
      return {
        success: false,
        error: error instanceof Error ? error : new Error(String(error)),
        messages,
        durationMs: Date.now() - startTime,
      };
    }
  }

  /**
   * Override buildPrompt to customize for issue pickup
   * (no input context, but includes issue number prominently)
   */
  protected override async buildPrompt(
    issueNumber: number,
    _inputContext?: void,
    skillsBasePath: string = "skills"
  ): Promise<string> {
    const skillPath = `${skillsBasePath}/nightgauge-issue-pickup/SKILL.md`;

    let skillContent: string;
    try {
      const fs = await import("node:fs/promises");
      skillContent = await fs.readFile(skillPath, "utf-8");
    } catch {
      throw new Error(`Failed to read skill file: ${skillPath}`);
    }

    return `# Pipeline Stage: issue-pickup

## Issue
Pick up issue #${issueNumber}

## Skill Instructions

${skillContent}

## Execution Requirements

1. Follow the skill instructions exactly for issue #${issueNumber}
2. Create the feature branch following the naming convention
3. Write the output context file to .nightgauge/pipeline/issue-${issueNumber}.json
4. The context file must include all required fields from the schema
5. Ensure the branch is pushed to the remote`;
  }
}
