/**
 * healthChangeInputs - reader for the per-change health metrics (#1284).
 *
 * | Metric | Source |
 * | --- | --- |
 * | change failure rate | `<clone>/pipeline/survival-records.jsonl`, folded by `merge_commit_sha` |
 * | per-change first-pass merge | `<clone>/pipeline/trace/*.jsonl` lines with `kind: "backtrack"` |
 * | plan fidelity | `<clone>/pipeline/planning-{N}.json` against `git diff-tree` of the merge SHA |
 *
 * Nothing here writes: these are files the pipeline already produces. An
 * absent survival journal reads as `null`, so the metrics are omitted rather
 * than reported as 0. Rows are restricted to the analysis period.
 */

import { execFile } from "node:child_process";
import * as fs from "node:fs";
import * as path from "node:path";
import * as readline from "node:readline";
import { promisify } from "node:util";

import type {
  BacktrackEntry,
  ChangeSignals,
  PlanComparisonEntry,
  SurvivalVerdictEntry,
} from "@nightgauge/sdk";
import type { DateRangeFilter } from "../types/aggregation";
import { isUsableWorkspaceRoot, pipelineStateDir } from "./cloneLayout";

const execFileAsync = promisify(execFile);

const SHA_PATTERN = /^[0-9a-f]{7,40}$/;
const VERDICTS = new Set<SurvivalVerdictEntry["verdict"]>([
  "pending",
  "survived",
  "reverted",
  "broke",
  "unobserved",
]);

/** Lists the files a merge commit touched. Injected in tests. */
export type MergedFilesReader = (workspaceRoot: string, sha: string) => Promise<string[]>;

/** `git diff-tree` of one validated SHA, without a shell; [] on any failure. */
export const gitMergedFiles: MergedFilesReader = async (workspaceRoot, sha) => {
  if (!SHA_PATTERN.test(sha)) return [];
  try {
    const { stdout } = await execFileAsync(
      "git",
      ["diff-tree", "--no-commit-id", "--name-only", "-r", "-m", "--first-parent", sha],
      { cwd: workspaceRoot, maxBuffer: 4 * 1024 * 1024 }
    );
    return [
      ...new Set(
        stdout
          .split("\n")
          .map((l) => l.trim())
          .filter(Boolean)
      ),
    ];
  } catch {
    return [];
  }
};

const str = (v: unknown): string => (typeof v === "string" ? v : "");
const num = (v: unknown): number => (typeof v === "number" && Number.isFinite(v) ? v : 0);

function inRange(ts: string, filter: DateRangeFilter): boolean {
  const t = Date.parse(ts);
  return t >= filter.startDate.getTime() && t <= filter.endDate.getTime();
}

async function* jsonLines(file: string): AsyncGenerator<Record<string, unknown>> {
  const lines = readline.createInterface({
    input: fs.createReadStream(file, { encoding: "utf-8" }),
    crlfDelay: Infinity,
  });
  for await (const line of lines) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    try {
      const row = JSON.parse(trimmed);
      if (row && typeof row === "object") yield row as Record<string, unknown>;
    } catch {
      continue;
    }
  }
}

/** Folds the journal by SHA, last write wins (Go `survival.Store.Load`). */
async function readSurvival(
  file: string,
  filter: DateRangeFilter
): Promise<SurvivalVerdictEntry[]> {
  const bySha = new Map<string, SurvivalVerdictEntry>();
  for await (const row of jsonLines(file)) {
    const sha = str(row.merge_commit_sha);
    const verdict = str(row.verdict) as SurvivalVerdictEntry["verdict"];
    if (!sha || !VERDICTS.has(verdict)) continue;
    bySha.set(sha, {
      issueNumber: num(row.issue_number),
      prNumber: num(row.pr_number),
      mergeCommitSha: sha,
      mergedAt: str(row.merged_at),
      verdict,
    });
  }
  return [...bySha.values()].filter((s) => s.mergedAt && inRange(s.mergedAt, filter));
}

async function readBacktracks(
  traceDir: string,
  filter: DateRangeFilter
): Promise<BacktrackEntry[]> {
  let names: string[];
  try {
    names = await fs.promises.readdir(traceDir);
  } catch {
    return [];
  }
  const out: BacktrackEntry[] = [];
  for (const name of names.filter((n) => n.endsWith(".jsonl"))) {
    for await (const row of jsonLines(path.join(traceDir, name))) {
      if (row.kind !== "backtrack") continue;
      const ts = str(row.ts);
      const issueNumber = num(row.issue);
      if (!ts || !issueNumber || !inRange(ts, filter)) continue;
      out.push({ issueNumber, runId: str(row.run_id), ts });
    }
  }
  return out;
}

async function readPlannedFiles(dir: string, issue: number): Promise<string[] | null> {
  try {
    const ctx = JSON.parse(
      await fs.promises.readFile(path.join(dir, `planning-${issue}.json`), "utf-8")
    ) as { files_to_create?: unknown; files_to_modify?: unknown };
    const list = (v: unknown): string[] =>
      Array.isArray(v) ? v.filter((f): f is string => typeof f === "string") : [];
    if (!Array.isArray(ctx.files_to_create) && !Array.isArray(ctx.files_to_modify)) return null;
    return [...list(ctx.files_to_create), ...list(ctx.files_to_modify)];
  } catch {
    return null;
  }
}

/** Per-change inputs in the period; null without `survival-records.jsonl`. */
export async function readChangeSignals(
  workspaceRoot: string,
  filter: DateRangeFilter,
  mergedFiles: MergedFilesReader = gitMergedFiles
): Promise<ChangeSignals | null> {
  if (!isUsableWorkspaceRoot(workspaceRoot)) return null;
  const dir = pipelineStateDir(workspaceRoot);
  const survivalPath = path.join(dir, "survival-records.jsonl");
  try {
    await fs.promises.access(survivalPath);
  } catch {
    return null;
  }

  const [survival, backtracks] = await Promise.all([
    readSurvival(survivalPath, filter),
    readBacktracks(path.join(dir, "trace"), filter),
  ]);

  const planComparisons: PlanComparisonEntry[] = await Promise.all(
    survival
      .filter((s) => s.issueNumber > 0)
      .map(async (s) => ({
        issueNumber: s.issueNumber,
        plannedFiles: await readPlannedFiles(dir, s.issueNumber),
        mergedFiles: SHA_PATTERN.test(s.mergeCommitSha)
          ? await mergedFiles(workspaceRoot, s.mergeCommitSha)
          : [],
      }))
  );

  return { survival, backtracks, planComparisons };
}
