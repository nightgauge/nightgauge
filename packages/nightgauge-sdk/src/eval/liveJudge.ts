/**
 * Live LLM judge for the model-eval scorer (Issue #4173 / #4174 follow-up).
 *
 * Implements the `EvalJudge` boundary with a real model: it spawns a grader
 * model (distinct from the model under test, to avoid self-grading bias) inside
 * the cell's workspace, lets it inspect the produced work, and returns per-rubric
 * dimension scores. This is what differentiates subjective quality on job classes
 * where deterministic gates are weak — UI, UX, and docs — so a frontier model's
 * cleaner, more accessible, better-explained work actually outscores a weaker
 * one that merely passes the same tests.
 *
 * The judge is **read-only**: `Write`/`Edit`/`Bash` are disallowed so repeated
 * samples (the reliability guard runs it N times) see an identical workspace and
 * the grader can never mutate the artifact it is grading.
 *
 * The spawn boundary is injected (shared with `LiveCellExecutor`) so the judge is
 * unit-testable without a real CLI. Judge cost is grading overhead and is NOT
 * attributed to the evaluated model's telemetry.
 *
 * @see docs/decisions/011-model-eval-system.md
 * @see qualityScorer.ts — runJudgeWithReliabilityGuard consumes this
 */

import { tmpdir } from "node:os";
import { getModelDescriptor } from "./modelRegistry.js";
import { defaultCliSpawn, type CliSpawnFn } from "./liveCellExecutor.js";
import { parseClaudeResult } from "./evalAdapters.js";
import type { EvalJudge, EvalJudgeVerdict, JudgeDimensionScore } from "./qualityScorer.js";
import type { EvalRubric, EvalTask, QualityDimensionName } from "./modelEvalSchemas.js";

/** Default grader model — strong judgment, cheaper than Opus. Never the SUT. */
export const DEFAULT_JUDGE_MODEL = "claude-sonnet-5";

/** 3 min — grading is a single reasoning turn over embedded source, not a crawl. */
const DEFAULT_JUDGE_TIMEOUT_MS = 180_000;

export interface LiveClaudeJudgeOptions {
  /** Absolute path to the workspace holding the produced work to grade. */
  workspaceDir: string;
  /** The task that was implemented (its instruction frames the grading). */
  task: EvalTask;
  /** CLI command (default `NIGHTGAUGE_CLAUDE_CLI_COMMAND` or `claude`). */
  command?: string;
  /** Grader model id/tier (default {@link DEFAULT_JUDGE_MODEL}). */
  model?: string;
  /** Per-invocation timeout. */
  timeoutMs?: number;
  /** Injected model spawn — tests pass a fake; production uses the default. */
  spawnClaude?: CliSpawnFn;
  /**
   * Gathers the engineer's source files to embed in the grading prompt. Injected
   * for tests; the default walks the workspace, skipping dependency/build dirs and
   * capping total size. Embedding the source (rather than letting the grader
   * explore a `node_modules`-heavy tree with tools) keeps grading fast and stable.
   */
  collectSources?: (workspaceDir: string) => Promise<SourceFile[]>;
}

/** One collected source file for embedding in the grading prompt. */
export interface SourceFile {
  path: string;
  content: string;
}

/**
 * `EvalJudge` that grades a completed task by giving a grader model the
 * engineer's source (embedded in the prompt) and asking for per-dimension scores.
 * One instance per cell. Because the source is embedded, the grader needs no file
 * tools — so grading is a single fast turn, not a slow workspace crawl.
 */
export class LiveClaudeJudge implements EvalJudge {
  private readonly workspaceDir: string;
  private readonly task: EvalTask;
  private readonly command: string;
  private readonly model: string;
  private readonly timeoutMs: number;
  private readonly spawnClaude: CliSpawnFn;
  private readonly collectSources: (workspaceDir: string) => Promise<SourceFile[]>;

  constructor(options: LiveClaudeJudgeOptions) {
    this.workspaceDir = options.workspaceDir;
    this.task = options.task;
    this.command = options.command ?? process.env.NIGHTGAUGE_CLAUDE_CLI_COMMAND ?? "claude";
    this.model = options.model ?? DEFAULT_JUDGE_MODEL;
    this.timeoutMs = options.timeoutMs ?? DEFAULT_JUDGE_TIMEOUT_MS;
    this.spawnClaude = options.spawnClaude ?? defaultCliSpawn;
    this.collectSources = options.collectSources ?? collectWorkspaceSources;
  }

  async judge(rubric: EvalRubric): Promise<EvalJudgeVerdict> {
    const model = getModelDescriptor(this.model)?.concrete_version ?? this.model;
    const sources = await this.collectSources(this.workspaceDir);
    const prompt = buildJudgePrompt(this.task, rubric, sources);
    // Run from a neutral cwd with all file/exec tools disallowed: the grader has
    // everything in the prompt, so it cannot (and need not) crawl the workspace.
    const res = await this.spawnClaude(
      this.command,
      buildJudgeArgs(model),
      prompt,
      tmpdir(),
      this.timeoutMs
    );
    if (res.code !== 0) {
      throw new Error(
        `judge invocation failed (exit ${res.code}): ${tail(res.stderr) || "no stderr"}`
      );
    }
    const parsed = parseClaudeResult(res.stdout);
    if (!parsed) {
      throw new Error(`could not parse judge CLI result for task ${this.task.id}`);
    }
    return extractJudgeVerdict(parsed.result ?? "", rubric);
  }
}

/** Grader args: no file/exec tools — the source is in the prompt. */
function buildJudgeArgs(model: string): string[] {
  return [
    "--print",
    "--output-format",
    "json",
    "--model",
    model,
    "--dangerously-skip-permissions",
    // Keep the flag that consumes a variadic list LAST so it does not swallow
    // subsequent flags. Everything the grader needs is embedded in the prompt.
    "--disallowed-tools",
    "Write",
    "Edit",
    "Bash",
    "NotebookEdit",
    "Read",
    "Glob",
    "Grep",
    "WebFetch",
    "WebSearch",
  ];
}

function buildJudgePrompt(task: EvalTask, rubric: EvalRubric, sources: SourceFile[]): string {
  const criteria = rubric.criteria
    .map((c) => `- ${c.dimension} (weight ${c.weight}): ${c.guidance}`)
    .join("\n");
  const shape = rubric.criteria
    .map((c) => `{"dimension":"${c.dimension}","score":<0-100>,"rationale":"<one sentence>"}`)
    .join(",");
  const files = sources.length
    ? sources.map((f) => `=== ${f.path} ===\n${f.content}`).join("\n\n")
    : "(no source files were produced)";
  return [
    "You are an expert code reviewer grading a COMPLETED software task. Grade the",
    "engineer's solution below on the given rubric. Judge only what is shown.",
    "",
    "Task that was implemented:",
    task.instruction,
    "",
    "Engineer's source files:",
    files,
    "",
    "Score ONLY these rubric dimensions, each 0-100 (100 = excellent, 0 = absent/broken):",
    criteria,
    "",
    "Return a SINGLE JSON object and nothing else, in exactly this shape:",
    `{"dimensions":[${shape}]}`,
  ].join("\n");
}

/** Directories/files never relevant to grading (dependencies, build output, VCS). */
const IGNORE_DIRS = new Set([
  "node_modules",
  ".git",
  "dist",
  "build",
  "coverage",
  ".next",
  ".turbo",
  "out",
]);
const IGNORE_FILES = new Set(["package-lock.json", "yarn.lock", "pnpm-lock.yaml"]);
const SOURCE_EXTENSIONS = new Set([
  ".ts",
  ".tsx",
  ".js",
  ".jsx",
  ".mjs",
  ".cjs",
  ".json",
  ".md",
  ".css",
  ".scss",
  ".html",
  ".vue",
  ".svelte",
]);
const MAX_TOTAL_CHARS = 60_000;
const MAX_FILE_CHARS = 20_000;

/** Default source collector: a bounded walk skipping dependency/build/VCS dirs. */
async function collectWorkspaceSources(workspaceDir: string): Promise<SourceFile[]> {
  const { readdir, readFile } = await import("node:fs/promises");
  const { join, relative, extname } = await import("node:path");
  const out: SourceFile[] = [];
  let total = 0;

  async function walk(current: string): Promise<void> {
    if (total >= MAX_TOTAL_CHARS) return;
    let entries;
    try {
      entries = await readdir(current, { withFileTypes: true });
    } catch {
      return;
    }
    for (const entry of entries.sort((a, b) => a.name.localeCompare(b.name))) {
      if (total >= MAX_TOTAL_CHARS) return;
      const full = join(current, entry.name);
      if (entry.isDirectory()) {
        if (!IGNORE_DIRS.has(entry.name)) await walk(full);
        continue;
      }
      if (IGNORE_FILES.has(entry.name) || !SOURCE_EXTENSIONS.has(extname(entry.name))) continue;
      let content: string;
      try {
        content = await readFile(full, "utf-8");
      } catch {
        continue;
      }
      if (content.length > MAX_FILE_CHARS)
        content = content.slice(0, MAX_FILE_CHARS) + "\n… (truncated)";
      out.push({ path: relative(workspaceDir, full), content });
      total += content.length;
    }
  }

  await walk(workspaceDir);
  return out;
}

/**
 * Parse the grader's JSON verdict from its final answer text, keeping only the
 * rubric's dimensions and clamping scores to 0–100. Robust to prose around the
 * JSON. Throws when no scorable dimension is recovered (so the reliability guard
 * surfaces a broken judge rather than silently scoring 0).
 */
export function extractJudgeVerdict(text: string, rubric: EvalRubric): EvalJudgeVerdict {
  const obj = firstJsonObject(text);
  const allowed = new Set<QualityDimensionName>(rubric.criteria.map((c) => c.dimension));
  const dimensions: JudgeDimensionScore[] = [];
  const raw =
    obj && Array.isArray((obj as { dimensions?: unknown }).dimensions)
      ? (obj as { dimensions: unknown[] }).dimensions
      : [];
  for (const entry of raw) {
    if (!entry || typeof entry !== "object") continue;
    const d = entry as { dimension?: unknown; score?: unknown; rationale?: unknown };
    if (typeof d.dimension !== "string" || !allowed.has(d.dimension as QualityDimensionName))
      continue;
    const score = clamp0to100(Number(d.score));
    if (Number.isNaN(score)) continue;
    dimensions.push({
      dimension: d.dimension as QualityDimensionName,
      score,
      rationale: typeof d.rationale === "string" ? d.rationale : undefined,
    });
  }
  if (dimensions.length === 0) {
    throw new Error("judge returned no scorable rubric dimensions");
  }
  return { dimensions };
}

/** Find and parse the first balanced `{...}` object in a string; null if none. */
function firstJsonObject(text: string): unknown {
  const start = text.indexOf("{");
  if (start === -1) return null;
  let depth = 0;
  let inStr = false;
  let escaped = false;
  for (let i = start; i < text.length; i++) {
    const ch = text[i];
    if (inStr) {
      if (escaped) escaped = false;
      else if (ch === "\\") escaped = true;
      else if (ch === '"') inStr = false;
      continue;
    }
    if (ch === '"') inStr = true;
    else if (ch === "{") depth++;
    else if (ch === "}") {
      depth--;
      if (depth === 0) {
        try {
          return JSON.parse(text.slice(start, i + 1));
        } catch {
          return null;
        }
      }
    }
  }
  return null;
}

function clamp0to100(n: number): number {
  if (Number.isNaN(n)) return NaN;
  return Math.max(0, Math.min(100, n));
}

function tail(s: string, max = 400): string {
  const t = s.trim();
  return t.length <= max ? t : "…" + t.slice(-max);
}
