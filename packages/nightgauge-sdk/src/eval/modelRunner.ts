/**
 * Cross-Model Skill Evaluation Harness — model invocation abstraction.
 *
 * `EvalModelRunner` decouples the harness from how a model is actually invoked.
 * Two implementations:
 *  - `MockModelRunner` — resolves output from in-memory fixtures keyed by
 *    `(scenarioId, model)`. Deterministic, zero API cost. CI default + what the
 *    harness's own tests use.
 *  - `LiveClaudeModelRunner` — spawns `claude --print --model <tier>`, matching
 *    the live pipeline's headless invocation shape. Selected ONLY when
 *    `NIGHTGAUGE_SKILL_EVAL_LIVE=1`. Never exercised in CI.
 *
 * Live mode relies on ambient `claude` auth; no API keys are read, stored, or
 * logged here.
 *
 * @see Issue #3814 - Build a cross-model skill evaluation harness
 * @see packages/nightgauge-sdk/src/cli/adapters/ClaudeHeadlessAdapter.ts
 */

import { spawn } from "child_process";
import type { ModelTier } from "../analysis/AutoModelSelector.js";
import type { EvalScenario } from "./schemas.js";
import type { ModelOutput } from "./assertions.js";

/** Abstraction over "run this scenario against this model, give me the output". */
export interface EvalModelRunner {
  readonly mode: "mock" | "live";
  run(scenario: EvalScenario, model: ModelTier): Promise<ModelOutput>;
}

// ---------------------------------------------------------------------------
// Mock runner
// ---------------------------------------------------------------------------

/** Fixture entry: the canned output for one (scenario, model) cell. */
export interface MockFixture {
  text: string;
  exit_code?: number;
}

/** Fixtures keyed by scenario id → model tier → canned output. */
export type MockFixtureMap = Record<string, Partial<Record<ModelTier, MockFixture>>>;

/**
 * Deterministic runner backed by an in-memory fixture map. This is what CI and
 * the harness unit tests use — no process spawning, no network.
 */
export class MockModelRunner implements EvalModelRunner {
  readonly mode = "mock" as const;

  constructor(private readonly fixtures: MockFixtureMap) {}

  async run(scenario: EvalScenario, model: ModelTier): Promise<ModelOutput> {
    const fixture = this.fixtures[scenario.id]?.[model];
    if (!fixture) {
      throw new Error(
        `missing mock fixture for scenario "${scenario.id}" + model "${model}". ` +
          `Add an entry under evals/fixtures/${scenario.skill}/.`
      );
    }
    return { text: fixture.text, exit_code: fixture.exit_code };
  }
}

// ---------------------------------------------------------------------------
// Live runner
// ---------------------------------------------------------------------------

/** A minimal spawn result the live runner collects. */
interface SpawnResult {
  stdout: string;
  stderr: string;
  code: number;
}

/** Injectable spawn function so the live runner stays testable without a real CLI. */
export type SpawnFn = (
  command: string,
  args: string[],
  prompt: string,
  cwd: string,
  timeoutMs: number
) => Promise<SpawnResult>;

const DEFAULT_LIVE_TIMEOUT_MS = 600_000; // 10 minutes, matches cliQueryHelper

/** Default spawn implementation: pipe the prompt to the CLI via stdin. */
const defaultSpawn: SpawnFn = (command, args, prompt, cwd, timeoutMs) =>
  new Promise<SpawnResult>((resolve, reject) => {
    const child = spawn(command, args, { cwd, stdio: ["pipe", "pipe", "pipe"] });
    let stdout = "";
    let stderr = "";
    let settled = false;

    const timer = setTimeout(() => {
      if (settled) return;
      settled = true;
      child.kill("SIGTERM");
      reject(new Error(`claude invocation timed out after ${timeoutMs}ms`));
    }, timeoutMs);

    child.stdout.on("data", (chunk) => (stdout += chunk.toString()));
    child.stderr.on("data", (chunk) => (stderr += chunk.toString()));
    child.on("error", (err) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      reject(err);
    });
    child.on("close", (code) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve({ stdout, stderr, code: code ?? 1 });
    });

    child.stdin.write(prompt);
    child.stdin.end();
  });

export interface LiveClaudeModelRunnerOptions {
  /** CLI command (defaults to `claude` or `NIGHTGAUGE_CLAUDE_CLI_COMMAND`). */
  command?: string;
  /** Working directory for the spawned CLI. */
  cwd?: string;
  /** Per-invocation timeout. */
  timeoutMs?: number;
  /** Injectable spawn — tests pass a fake; production uses the default. */
  spawnFn?: SpawnFn;
}

/**
 * Live runner that spawns `claude --print --model <tier>` and feeds the
 * scenario prompt over stdin, mirroring `ClaudeHeadlessAdapter`'s shape. The
 * model is selected by **tier alias** so the CLI resolves the concrete version
 * exactly as the live pipeline does.
 */
export class LiveClaudeModelRunner implements EvalModelRunner {
  readonly mode = "live" as const;

  private readonly command: string;
  private readonly cwd: string;
  private readonly timeoutMs: number;
  private readonly spawnFn: SpawnFn;

  constructor(options: LiveClaudeModelRunnerOptions = {}) {
    this.command = options.command ?? process.env.NIGHTGAUGE_CLAUDE_CLI_COMMAND ?? "claude";
    this.cwd = options.cwd ?? process.cwd();
    this.timeoutMs = options.timeoutMs ?? DEFAULT_LIVE_TIMEOUT_MS;
    this.spawnFn = options.spawnFn ?? defaultSpawn;
  }

  async run(scenario: EvalScenario, model: ModelTier): Promise<ModelOutput> {
    const args = ["--print", "--output-format", "text", "--model", model];
    const result = await this.spawnFn(
      this.command,
      args,
      scenario.prompt,
      this.cwd,
      this.timeoutMs
    );
    return { text: result.stdout, exit_code: result.code };
  }
}

/**
 * `true` when live mode is explicitly enabled. Live mode requires
 * `NIGHTGAUGE_SKILL_EVAL_LIVE=1` — the two-tier gate borrowed from #2092's
 * `PLATFORM_TEST_URL` pattern. CI never sets this, so CI always runs mock mode.
 */
export function isLiveModeEnabled(env: NodeJS.ProcessEnv = process.env): boolean {
  return env.NIGHTGAUGE_SKILL_EVAL_LIVE === "1";
}
