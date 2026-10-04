/**
 * Every SDK-path stage gets exactly its skill's allowed-tools (#2358).
 *
 * `PipelineOrchestrator.runStage` and `runStageStreaming`, which
 * `nightgauge-sdk stage` and `run` drive, used to build the stage's query with
 * no tools at all. They now read the stage SKILL.md's `allowed-tools` from the
 * file the prompt comes from, drop AskUserQuestion as every headless Go
 * dispatcher does, and each adapter's query applies the list: the Claude Agent
 * SDK as `allowedTools`, claude-headless as `--allowedTools`, Codex as the
 * sandbox mode the tools justify, on a fresh start and on a resume.
 *
 * The stage's skill is a fixture through NIGHTGAUGE_SKILL_DIR, the packaged
 * extension's route to the exact skill directory (loadStageSkill).
 */
import { EventEmitter } from "node:events";
import { spawn } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import * as path from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  DEFAULT_STAGES,
  PipelineOrchestrator,
} from "../../src/orchestrator/PipelineOrchestrator.js";
import type { SDKQueryFunction, SDKQueryOptions } from "../../src/orchestrator/StageExecutor.js";
import { ClaudeHeadlessAdapter } from "../../src/cli/adapters/ClaudeHeadlessAdapter.js";
import { ClaudeSdkAdapter } from "../../src/cli/adapters/ClaudeSdkAdapter.js";
import { CodexAdapter } from "../../src/cli/adapters/CodexAdapter.js";
import { withClaudeAllowedTools } from "../../src/cli/adapters/cliQueryHelper.js";
import { NO_ALLOWED_TOOLS, NO_HEADLESS_TOOLS } from "../../src/orchestrator/skillAllowedTools.js";
import { STAGE_SKILL_RENDER_ENV } from "../../src/orchestrator/StageExecutor.js";
import { createMockResult } from "../mocks/agent-sdk.js";

// The CLI adapters' queries spawn their CLI; record the argv instead. The rest
// of node:child_process stays real.
vi.mock("node:child_process", async (importOriginal) => ({
  ...(await importOriginal<typeof import("node:child_process")>()),
  spawn: vi.fn(),
}));

// ClaudeSdkAdapter's query is the Agent SDK's own `query`.
const agentSdk = vi.hoisted(() => ({ query: vi.fn() }));
vi.mock("@anthropic-ai/claude-agent-sdk", () => agentSdk);

const spawnMock = vi.mocked(spawn);

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../../..");
const BYPASS = "--dangerously-bypass-approvals-and-sandbox";
const READ_ONLY = ["Read", "Grep", "Glob"];
const PIPELINE_SET = ["Read", "Write", "Edit", "Glob", "Grep", "Bash", "Task"];

let skillDir: string;

/** Make the stage's SKILL.md one whose frontmatter has `frontmatterTools`. */
function stageSkill(frontmatterTools: string | undefined): void {
  const field = frontmatterTools === undefined ? "" : `allowed-tools: ${frontmatterTools}\n`;
  writeFileSync(
    path.join(skillDir, "SKILL.md"),
    `---\nname: nightgauge-feature-planning\n${field}---\n\n# Plan the feature\n`
  );
}

/** A query that records the options each stage built it with. */
function recordingQuery(seen: SDKQueryOptions[]): SDKQueryFunction {
  return async function* (queryOptions) {
    seen.push(queryOptions);
    yield createMockResult();
  };
}

/** A child process that prints `stdout`, exits 0, and reads its stdin. */
function finishedChild(stdout: string) {
  const child = Object.assign(new EventEmitter(), {
    stdout: new EventEmitter(),
    stderr: new EventEmitter(),
    stdin: { write: vi.fn(), end: vi.fn(), on: vi.fn() },
  });
  process.nextTick(() => {
    child.stdout.emit("data", Buffer.from(stdout));
    child.emit("close", 0);
  });
  return child;
}

/** The argv of the one CLI the stage spawned. */
function spawnedArgs(): string[] {
  expect(spawnMock).toHaveBeenCalledTimes(1);
  return spawnMock.mock.calls[0][1] as string[];
}

beforeEach(() => {
  vi.clearAllMocks();
  skillDir = mkdtempSync(path.join(tmpdir(), "ng-stage-tools-"));
  vi.stubEnv("NIGHTGAUGE_SKILL_DIR", skillDir);
});

afterEach(() => {
  vi.unstubAllEnvs();
  rmSync(skillDir, { recursive: true, force: true });
});

describe("a stage's query carries its skill's allowed-tools (#2358)", () => {
  it("runStage hands the query exactly the read-only set the skill declares", async () => {
    stageSkill("Read Grep Glob");
    const seen: SDKQueryOptions[] = [];
    const result = await new PipelineOrchestrator(recordingQuery(seen)).runStage(
      "feature-planning",
      7
    );

    expect(result.success).toBe(true);
    expect(seen).toHaveLength(1);
    expect(seen[0].options?.allowedTools).toEqual(READ_ONLY);
    // A tool the skill does not declare is not granted.
    expect(seen[0].options?.allowedTools).not.toContain("Bash");
    expect(seen[0].options?.allowedTools).not.toContain("Write");
  });

  it("runStageStreaming hands it the same set", async () => {
    stageSkill("Read Grep Glob");
    const seen: SDKQueryOptions[] = [];
    const orchestrator = new PipelineOrchestrator(recordingQuery(seen));
    for await (const _ of orchestrator.runStageStreaming("feature-planning", 7)) {
      /* drain */
    }

    expect(seen).toHaveLength(1);
    expect(seen[0].options?.allowedTools).toEqual(READ_ONLY);
  });

  it("drops AskUserQuestion and keeps a Tool(pattern) entry whole", async () => {
    stageSkill("Read, Bash(gh *), AskUserQuestion");
    const seen: SDKQueryOptions[] = [];
    await new PipelineOrchestrator(recordingQuery(seen)).runStage("feature-planning", 7);

    expect(seen[0].options?.allowedTools).toEqual(["Read", "Bash(gh *)"]);
  });

  it("fails a stage whose skill's allowed-tools lists no tool, naming the file", async () => {
    // Read as no list, it got the runner's default: full access under Codex.
    stageSkill("[]");
    const seen: SDKQueryOptions[] = [];
    const result = await new PipelineOrchestrator(recordingQuery(seen)).runStage(
      "feature-planning",
      7
    );

    expect(result.success).toBe(false);
    expect(result.error?.message).toBe(`${path.join(skillDir, "SKILL.md")}: ${NO_ALLOWED_TOOLS}`);
    expect(seen).toHaveLength(0);
  });

  it("fails a stage whose only tool is AskUserQuestion, naming the file (#2390)", async () => {
    // Filtered to nothing, it read as a skill that declares no tools: full
    // access under Codex for the skill that asked for the least.
    stageSkill("AskUserQuestion");
    const seen: SDKQueryOptions[] = [];
    const result = await new PipelineOrchestrator(recordingQuery(seen)).runStage(
      "feature-planning",
      7
    );

    expect(result.success).toBe(false);
    expect(result.error?.message).toBe(`${path.join(skillDir, "SKILL.md")}: ${NO_HEADLESS_TOOLS}`);
    expect(seen).toHaveLength(0);
  });

  it("leaves the option unset for a skill that declares none, as the Go path does", async () => {
    stageSkill(undefined);
    const seen: SDKQueryOptions[] = [];
    await new PipelineOrchestrator(recordingQuery(seen)).runStage("feature-planning", 7);

    expect(seen[0].options?.allowedTools).toBeUndefined();
  });

  it("gives every shipped pipeline stage exactly the tools its SKILL.md declares", async () => {
    vi.stubEnv("NIGHTGAUGE_SKILL_DIR", "");
    const seen: SDKQueryOptions[] = [];
    const orchestrator = new PipelineOrchestrator(recordingQuery(seen));

    for (const stage of DEFAULT_STAGES) {
      const skill = readFileSync(
        path.join(REPO_ROOT, "skills", `nightgauge-${stage}`, "SKILL.md"),
        "utf-8"
      );
      // The shipped skills declare a plain space-separated list.
      const line = /^allowed-tools:(.*)$/m.exec(skill.split("\n---")[0]);
      expect(line, `${stage} declares allowed-tools`).not.toBeNull();
      const declared = line![1].trim().split(/\s+/);

      const result = await orchestrator.runStage(stage, 7);

      expect(result.success, `${stage} ran`).toBe(true);
      expect(seen.at(-1)?.options?.stage).toBe(stage);
      expect(seen.at(-1)?.options?.allowedTools, stage).toEqual(
        declared.filter((tool) => tool !== "AskUserQuestion")
      );
    }
  });
});

describe("Codex runs a read-only stage read-only (#2358)", () => {
  const CODEX_STDOUT = [
    JSON.stringify({ type: "thread.started", thread_id: "thread-2" }),
    JSON.stringify({
      type: "item.completed",
      item: { id: "item_1", type: "agent_message", text: "Planned." },
    }),
    JSON.stringify({ type: "turn.completed" }),
  ].join("\n");

  beforeEach(() => {
    for (const name of [
      "NIGHTGAUGE_CODEX_CLI_ARGS",
      "NIGHTGAUGE_CODEX_MODEL",
      "NIGHTGAUGE_CODEX_REASONING_EFFORT",
      "NIGHTGAUGE_CODEX_EPHEMERAL",
      "NIGHTGAUGE_CODEX_EPHEMERAL_STAGES",
      "NIGHTGAUGE_CODEX_RESUME_ENABLED",
    ]) {
      vi.stubEnv(name, "");
    }
    vi.stubEnv("NIGHTGAUGE_CODEX_CLI_COMMAND", "codex");
    spawnMock.mockImplementation((() => finishedChild(CODEX_STDOUT)) as unknown as typeof spawn);
  });

  /** Run feature-planning through the real Codex adapter's query. */
  async function runCodexStage(resumeSessionId?: string): Promise<string[]> {
    const queryFn = await new CodexAdapter().createQueryFunction({ stage: "feature-planning" });
    // No `adapter` in the config: it would provision Codex steering (AGENTS.md,
    // ~/.codex/config.toml) for real. The query is the Codex adapter's either way.
    const result = await new PipelineOrchestrator(queryFn).runStage(
      "feature-planning",
      7,
      resumeSessionId ? { resumeSessionId } : undefined
    );
    expect(result.error).toBeUndefined();
    return spawnedArgs();
  }

  it("scopes a fresh start to --sandbox read-only, without the bypass flag", async () => {
    stageSkill("Read Grep Glob");
    const args = await runCodexStage();

    expect(args.slice(0, 5)).toEqual([
      "--ask-for-approval",
      "never",
      "exec",
      "--sandbox",
      "read-only",
    ]);
    expect(args).not.toContain(BYPASS);
  });

  it("scopes a resume to sandbox_mode read-only, without the bypass flag", async () => {
    stageSkill("Read Grep Glob");
    vi.stubEnv("NIGHTGAUGE_CODEX_RESUME_ENABLED", "true");
    const args = await runCodexStage("thread-1");

    expect(args.slice(0, 8)).toEqual([
      "--ask-for-approval",
      "never",
      "exec",
      "resume",
      "thread-1",
      "-",
      "-c",
      'sandbox_mode="read-only"',
    ]);
    expect(args).not.toContain(BYPASS);
    expect(args).not.toContain("--sandbox");
  });

  it("never spawns Codex for a skill whose only tool is AskUserQuestion (#2390)", async () => {
    stageSkill("AskUserQuestion");
    const queryFn = await new CodexAdapter().createQueryFunction({ stage: "feature-planning" });
    const result = await new PipelineOrchestrator(queryFn).runStage("feature-planning", 7);

    expect(result.success).toBe(false);
    expect(result.error?.message).toContain(NO_HEADLESS_TOOLS);
    expect(spawnMock).not.toHaveBeenCalled();
  });

  it("scopes to the dispatcher's render, not the base SKILL.md (#2381)", async () => {
    // The base grants Bash; a whole-file override the render took its tools
    // from narrows them to a read-only set, and that is the sandbox Codex gets.
    stageSkill(PIPELINE_SET.join(" "));
    const renderFile = path.join(skillDir, "render.json");
    writeFileSync(
      renderFile,
      JSON.stringify({
        stage: "feature-planning",
        skill_path: path.join(skillDir, "_overlays", "codex.SKILL.md"),
        content: "# Override body\n",
        allowed_tools: READ_ONLY,
      })
    );
    vi.stubEnv(STAGE_SKILL_RENDER_ENV, renderFile);
    const args = await runCodexStage();

    expect(args.slice(3, 5)).toEqual(["--sandbox", "read-only"]);
    expect(args).not.toContain(BYPASS);
  });

  it("keeps full access for a skill that grants Bash, so the mode follows the skill", async () => {
    stageSkill(PIPELINE_SET.join(" "));
    const args = await runCodexStage();

    expect(args).toContain(BYPASS);
    expect(args).not.toContain("--sandbox");
  });
});

describe("the Claude Agent SDK query receives the skill's tools as allowedTools (#2358)", () => {
  beforeEach(() => {
    agentSdk.query.mockImplementation(async function* () {
      yield createMockResult();
    });
  });

  it("passes the read-only set where it used to pass none", async () => {
    stageSkill("Read Grep Glob");
    const queryFn = await new ClaudeSdkAdapter().createQueryFunction();
    const result = await new PipelineOrchestrator(queryFn).runStage("feature-planning", 7);

    expect(result.success).toBe(true);
    expect(agentSdk.query).toHaveBeenCalledTimes(1);
    const [{ options }] = agentSdk.query.mock.calls[0] as [SDKQueryOptions];
    // The Agent SDK runs these without asking. Any other tool that needs a
    // permission is refused in a headless run, as under the Go path's
    // `--allowedTools`, since no one is there to grant it.
    expect(options?.allowedTools).toEqual(READ_ONLY);
    expect(options?.allowedTools).not.toContain("Bash");
  });

  it("passes the pipeline set a shipped stage declares", async () => {
    stageSkill(PIPELINE_SET.join(" "));
    const queryFn = await new ClaudeSdkAdapter().createQueryFunction();
    await new PipelineOrchestrator(queryFn).runStage("feature-planning", 7);

    const [{ options }] = agentSdk.query.mock.calls[0] as [SDKQueryOptions];
    expect(options?.allowedTools).toEqual(PIPELINE_SET);
  });
});

describe("claude-headless takes the skill's tools as --allowedTools (#2358)", () => {
  beforeEach(() => {
    vi.stubEnv("NIGHTGAUGE_CLAUDE_CLI_COMMAND", "claude");
    vi.stubEnv("NIGHTGAUGE_CLAUDE_CLI_ARGS", "");
    spawnMock.mockImplementation((() => finishedChild("Planned.")) as unknown as typeof spawn);
  });

  async function runHeadlessStage(): Promise<string[]> {
    const queryFn = await new ClaudeHeadlessAdapter().createQueryFunction();
    const result = await new PipelineOrchestrator(queryFn).runStage("feature-planning", 7);
    expect(result.error).toBeUndefined();
    return spawnedArgs();
  }

  it("passes the read-only set comma-joined", async () => {
    stageSkill("Read Grep Glob");

    expect(await runHeadlessStage()).toEqual([
      "--print",
      "--output-format",
      "text",
      "--allowedTools",
      "Read,Grep,Glob",
    ]);
  });

  it("passes a Tool(pattern) entry whole", async () => {
    stageSkill("Read, Bash(gh *)");

    expect(await runHeadlessStage()).toEqual([
      "--print",
      "--output-format",
      "text",
      "--allowedTools",
      "Read,Bash(gh *)",
    ]);
  });

  it("adds nothing for a skill that declares no tools", async () => {
    stageSkill(undefined);

    expect(await runHeadlessStage()).toEqual(["--print", "--output-format", "text"]);
  });

  it("keeps an operator's own allowed tools as given", async () => {
    stageSkill("Read Grep Glob");
    vi.stubEnv("NIGHTGAUGE_CLAUDE_CLI_ARGS", "--print --output-format text --allowedTools Read");

    expect(await runHeadlessStage()).toEqual([
      "--print",
      "--output-format",
      "text",
      "--allowedTools",
      "Read",
    ]);
    expect(withClaudeAllowedTools(["--allowed-tools=Read"], ["Bash"])).toEqual([
      "--allowed-tools=Read",
    ]);
  });
});

describe("a dispatcher's render is the stage's prompt and grant (#2381)", () => {
  /** Write a render handoff for feature-planning and point the SDK at it. */
  function handOver(render: Record<string, unknown>): void {
    const file = path.join(skillDir, "render.json");
    writeFileSync(file, JSON.stringify({ stage: "feature-planning", ...render }));
    vi.stubEnv(STAGE_SKILL_RENDER_ENV, file);
  }

  it("prompts with the render's body verbatim and grants the render's tools", async () => {
    stageSkill(PIPELINE_SET.join(" "));
    // An absolute path in the render must not be rewritten a second time.
    const body = `# Rendered\n\n## Host adaptation: OpenCode\n\nRead ${skillDir}/skills/_shared/x.md\n`;
    handOver({
      skill_path: path.join(skillDir, "SKILL.md"),
      content: body,
      allowed_tools: READ_ONLY,
    });
    const seen: SDKQueryOptions[] = [];
    const result = await new PipelineOrchestrator(recordingQuery(seen)).runStage(
      "feature-planning",
      7
    );

    expect(result.success).toBe(true);
    expect(seen[0].prompt).toContain(body);
    expect(seen[0].prompt).not.toContain("# Plan the feature");
    expect(seen[0].options?.allowedTools).toEqual(READ_ONLY);
  });

  it("leaves the option unset when the rendered skill declares no tools", async () => {
    stageSkill(PIPELINE_SET.join(" "));
    handOver({
      skill_path: path.join(skillDir, "SKILL.md"),
      content: "# Injected\n",
      allowed_tools: [],
    });
    const seen: SDKQueryOptions[] = [];
    await new PipelineOrchestrator(recordingQuery(seen)).runStage("feature-planning", 7);

    expect(seen[0].prompt).toContain("# Injected");
    expect(seen[0].options?.allowedTools).toBeUndefined();
  });

  it("refuses a render for another stage", async () => {
    stageSkill("Read");
    handOver({ stage: "feature-dev", skill_path: "/x/SKILL.md", content: "# Dev\n" });
    const seen: SDKQueryOptions[] = [];
    const result = await new PipelineOrchestrator(recordingQuery(seen)).runStage(
      "feature-planning",
      7
    );

    expect(result.success).toBe(false);
    expect(result.error?.message).toContain("not feature-planning");
    expect(seen).toHaveLength(0);
  });

  it("refuses a rendered skill whose only tool is AskUserQuestion, naming its file", async () => {
    stageSkill("Read");
    handOver({
      skill_path: "/x/override.SKILL.md",
      content: "# Ask\n",
      allowed_tools: ["AskUserQuestion"],
    });
    const seen: SDKQueryOptions[] = [];
    const result = await new PipelineOrchestrator(recordingQuery(seen)).runStage(
      "feature-planning",
      7
    );

    expect(result.error?.message).toBe(`/x/override.SKILL.md: ${NO_HEADLESS_TOOLS}`);
    expect(seen).toHaveLength(0);
  });
});
