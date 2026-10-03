/**
 * The exported stage classes grant each query their skill's allowed-tools, as
 * `nightgauge-sdk stage` does (#2358). BaseStage.execute reads the stage's
 * SKILL.md once, builds the prompt from it and passes the skill's tools
 * without AskUserQuestion. These classes used to pass none, so a query they
 * built was never granted what its skill declares.
 */
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import * as fs from "node:fs/promises";
import * as os from "node:os";
import * as path from "node:path";
import { EventBus, PipelineRunEmitter } from "../../src/events/EventBus.js";
import { TokenTracker } from "../../src/tracking/TokenTracker.js";
import { ContextManager } from "../../src/context/ContextManager.js";
import { StageExecutor, type SDKQueryOptions } from "../../src/orchestrator/StageExecutor.js";
import { NO_ALLOWED_TOOLS } from "../../src/orchestrator/skillAllowedTools.js";
import {
  BaseStage,
  FeatureDevStage,
  FeaturePlanningStage,
  IssuePickupStage,
  PRCreateStage,
} from "../../src/stages/index.js";
import {
  DevContextSchema,
  IssueContextSchema,
  PlanningContextSchema,
} from "../../src/context/schemas/index.js";

const ISSUE = 2358;

const STAGES: Array<[string, () => BaseStage<unknown, unknown>]> = [
  ["nightgauge-issue-pickup", () => new IssuePickupStage() as BaseStage<unknown, unknown>],
  ["nightgauge-feature-planning", () => new FeaturePlanningStage() as BaseStage<unknown, unknown>],
  ["nightgauge-feature-dev", () => new FeatureDevStage() as BaseStage<unknown, unknown>],
  ["nightgauge-pr-create", () => new PRCreateStage() as BaseStage<unknown, unknown>],
];

let tempRoot: string;
let skillsBasePath: string;
let contextPath: string;
let contextManager: ContextManager;
let seen: SDKQueryOptions[];
let executor: StageExecutor;

/** Give every stage skill this frontmatter `allowed-tools` line, or none. */
async function writeSkills(allowedTools: string | undefined): Promise<void> {
  const field = allowedTools === undefined ? "" : `allowed-tools: ${allowedTools}\n`;
  for (const [dir] of STAGES) {
    await fs.mkdir(path.join(skillsBasePath, dir), { recursive: true });
    await fs.writeFile(
      path.join(skillsBasePath, dir, "SKILL.md"),
      `---\nname: ${dir}\n${field}---\n\n# ${dir}\n`,
      "utf-8"
    );
  }
}

/** The input context each stage reads before its query. */
async function writeInputContexts(): Promise<void> {
  await contextManager.write(IssueContextSchema, `issue-${ISSUE}.json`, {
    schema_version: "1.4",
    issue_number: ISSUE,
    title: "Grant the stage classes their tools",
    type: "bug",
    branch: "fix/2358-stage-tools",
    base_branch: "main",
    requirements: { summary: "Pass allowed-tools", acceptance_criteria: ["Tools reach the query"] },
    labels: ["type:fix"],
    created_at: "2026-10-02T00:00:00.000Z",
  });
  await contextManager.write(PlanningContextSchema, `planning-${ISSUE}.json`, {
    schema_version: "1.1",
    issue_number: ISSUE,
    plan_file: `.nightgauge/plans/${ISSUE}-stage-tools.md`,
    approach: "Read the skill once",
    files_to_create: [],
    files_to_modify: ["packages/nightgauge-sdk/src/stages/base.ts"],
    created_at: "2026-10-02T00:05:00.000Z",
  });
  await contextManager.write(DevContextSchema, `dev-${ISSUE}.json`, {
    schema_version: "1.0",
    issue_number: ISSUE,
    commit_sha: "abc123",
    files_changed: { created: [], modified: [], deleted: [] },
    tests_status: { passed: 1, failed: 0, coverage: 90 },
    quality_checks: { code_standards: "passed", security_review: "passed" },
    created_at: "2026-10-02T00:10:00.000Z",
  });
}

beforeEach(async () => {
  tempRoot = await fs.mkdtemp(path.join(os.tmpdir(), "ng-stage-classes-"));
  skillsBasePath = path.join(tempRoot, "skills");
  contextPath = path.join(tempRoot, ".nightgauge/pipeline");
  await fs.mkdir(contextPath, { recursive: true });
  contextManager = new ContextManager(contextPath);
  await writeInputContexts();
  seen = [];
  executor = new StageExecutor(
    new TokenTracker(),
    new PipelineRunEmitter(new EventBus(), 1),
    async function* (options) {
      seen.push(options);
      yield { type: "result", usage: { input_tokens: 1, output_tokens: 1 }, total_cost_usd: 0 };
    }
  );
});

afterEach(async () => {
  await fs.rm(tempRoot, { recursive: true, force: true });
});

describe("the exported stage classes grant their skill's tools (#2358)", () => {
  it.each(STAGES)("%s's query gets its allowed-tools without AskUserQuestion", async (_, make) => {
    await writeSkills("Read, Grep, Bash(gh *), AskUserQuestion");

    await make().execute(executor, contextManager, { issueNumber: ISSUE, skillsBasePath });

    expect(seen).toHaveLength(1);
    expect(seen[0].options?.allowedTools).toEqual(["Read", "Grep", "Bash(gh *)"]);
    // The prompt is built from the same SKILL.md.
    expect(seen[0].prompt).toContain("Bash(gh *), AskUserQuestion");
  });

  it("leaves the option unset for a skill that declares none", async () => {
    await writeSkills(undefined);

    await new FeatureDevStage().execute(executor, contextManager, {
      issueNumber: ISSUE,
      skillsBasePath,
    });

    expect(seen).toHaveLength(1);
    expect(seen[0].options?.allowedTools).toBeUndefined();
  });

  it("fails a stage whose allowed-tools lists no tool, and queries nothing", async () => {
    await writeSkills("[]");

    const result = await new FeatureDevStage().execute(executor, contextManager, {
      issueNumber: ISSUE,
      skillsBasePath,
    });

    expect(result.success).toBe(false);
    // The refusal names the file, as the Go render's does.
    expect(result.error?.message).toBe(
      `${path.join(skillsBasePath, "nightgauge-feature-dev", "SKILL.md")}: ${NO_ALLOWED_TOOLS}`
    );
    expect(seen).toHaveLength(0);
  });

  it("grants feature-planning's repair pass the same tools", async () => {
    await writeSkills("Read Bash AskUserQuestion");
    // A planning context that fails its schema sends the stage to its repair pass.
    await fs.writeFile(
      path.join(contextPath, `planning-${ISSUE}.json`),
      JSON.stringify({ schema_version: "1.1", issue_number: ISSUE }),
      "utf-8"
    );

    await new FeaturePlanningStage().execute(executor, contextManager, {
      issueNumber: ISSUE,
      skillsBasePath,
    });

    expect(seen).toHaveLength(2);
    expect(seen[1].prompt).toContain("Repair Task");
    expect(seen[1].options?.allowedTools).toEqual(["Read", "Bash"]);
  });
});
