/**
 * healthLearningInputs - readers for the learning-effectiveness and
 * skill-drift health dimensions (#1900).
 *
 * | Dimension | File | Fields |
 * | --- | --- | --- |
 * | learning-effectiveness | `<clone>/pipeline/history/outcomes.jsonl` | `completedAt`, `predictedModel`, `actualModel`, `predictedSize`, `actualSize` |
 * | learning-effectiveness | `<clone>/pipeline/calibration.json` | `updated_at` |
 * | skill-drift | `<git-dir>/nightgauge-worktree/health/skill-effectiveness.jsonl` | latest record per (`skill_file`, `commit_hash`) |
 *
 * An absent file reads as `null`, so the dimension reports it as missing
 * input and is excluded — never as an empty corpus. Rows are restricted to
 * the analysis period. Both readers stream line by line.
 */

import * as fs from "node:fs";
import * as path from "node:path";
import * as readline from "node:readline";

import type { LearningOutcomeEntry, LearningSignals } from "@nightgauge/sdk";
import type { SkillEffectivenessRecord } from "../schemas/skillEffectiveness";
import type { DateRangeFilter } from "../types/aggregation";
import { isUsableWorkspaceRoot, pipelineStateDir } from "./cloneLayout";
import { SkillEffectivenessWriter } from "./SkillEffectivenessWriter";

const str = (v: unknown): string => (typeof v === "string" ? v : "");

function inRange(ts: string, filter: DateRangeFilter): boolean {
  const t = Date.parse(ts);
  return t >= filter.startDate.getTime() && t <= filter.endDate.getTime();
}

/** Outcome pairs in the period plus calibration freshness; null without outcomes.jsonl. */
export async function readLearningSignals(
  workspaceRoot: string,
  filter: DateRangeFilter
): Promise<LearningSignals | null> {
  if (!isUsableWorkspaceRoot(workspaceRoot)) return null;
  const dir = pipelineStateDir(workspaceRoot);
  const outcomesPath = path.join(dir, "history", "outcomes.jsonl");
  try {
    await fs.promises.access(outcomesPath);
  } catch {
    return null;
  }

  const outcomes: LearningOutcomeEntry[] = [];
  const lines = readline.createInterface({
    input: fs.createReadStream(outcomesPath, { encoding: "utf-8" }),
    crlfDelay: Infinity,
  });
  for await (const line of lines) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    let row: Record<string, unknown>;
    try {
      row = JSON.parse(trimmed);
    } catch {
      continue;
    }
    const completedAt = str(row.completedAt);
    if (!completedAt || !inRange(completedAt, filter)) continue;
    outcomes.push({
      completedAt,
      predictedModel: str(row.predictedModel),
      actualModel: str(row.actualModel),
      predictedSize: str(row.predictedSize),
      actualSize: str(row.actualSize),
    });
  }

  let calibrationUpdatedAt: string | null;
  try {
    const calibration = JSON.parse(
      await fs.promises.readFile(path.join(dir, "calibration.json"), "utf-8")
    ) as { updated_at?: unknown };
    calibrationUpdatedAt = str(calibration.updated_at) || null;
  } catch {
    calibrationUpdatedAt = null;
  }

  return { outcomes, calibrationUpdatedAt };
}

/** Latest record per skill change inside the period; null without the file. */
export async function readSkillEffectiveness(
  workspaceRoot: string,
  filter: DateRangeFilter
): Promise<SkillEffectivenessRecord[] | null> {
  return SkillEffectivenessWriter.readLatest(workspaceRoot, {
    since: filter.startDate,
    until: filter.endDate,
  });
}
