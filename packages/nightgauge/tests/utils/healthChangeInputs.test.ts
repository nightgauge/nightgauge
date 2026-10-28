/**
 * healthChangeInputs — the files the per-change health metrics read (#1284).
 * An absent survival journal reads as null, never as empty data.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { readChangeSignals } from "../../src/utils/healthChangeInputs";
import { mkFakeCloneLayout } from "../helpers/cloneLayout";

let root: string;
let pipeline: string;
const filter = {
  startDate: new Date("2026-03-01T00:00:00Z"),
  endDate: new Date("2026-03-31T23:59:59Z"),
};

const jsonl = (rows: unknown[]) => rows.map((r) => JSON.stringify(r)).join("\n") + "\n";

beforeEach(() => {
  root = fs.mkdtempSync(path.join(os.tmpdir(), "ng-change-inputs-"));
  pipeline = mkFakeCloneLayout(root).pipeline;
});

afterEach(() => fs.rmSync(root, { recursive: true, force: true }));

describe("readChangeSignals", () => {
  it("is null when survival-records.jsonl is absent", async () => {
    expect(await readChangeSignals(root, filter, vi.fn())).toBeNull();
  });

  it("folds survival by SHA, filters the period, skips malformed lines", async () => {
    fs.writeFileSync(
      path.join(pipeline, "survival-records.jsonl"),
      jsonl([
        {
          merge_commit_sha: "aaaaaaa",
          issue_number: 1,
          pr_number: 11,
          merged_at: "2026-03-05T00:00:00Z",
          verdict: "pending",
        },
        {
          merge_commit_sha: "bbbbbbb",
          issue_number: 2,
          pr_number: 12,
          merged_at: "2026-02-05T00:00:00Z",
          verdict: "survived",
        },
        {
          merge_commit_sha: "aaaaaaa",
          issue_number: 1,
          pr_number: 11,
          merged_at: "2026-03-05T00:00:00Z",
          verdict: "reverted",
        },
      ]) + "{not json\n"
    );
    const reader = vi.fn(async () => ["a.ts"]);
    const r = await readChangeSignals(root, filter, reader);
    expect(r?.survival).toEqual([
      {
        issueNumber: 1,
        prNumber: 11,
        mergeCommitSha: "aaaaaaa",
        mergedAt: "2026-03-05T00:00:00Z",
        verdict: "reverted",
      },
    ]);
    expect(r?.planComparisons).toEqual([
      { issueNumber: 1, plannedFiles: null, mergedFiles: ["a.ts"] },
    ]);
  });

  it("reads only in-period backtrack trace events", async () => {
    fs.writeFileSync(path.join(pipeline, "survival-records.jsonl"), "");
    fs.mkdirSync(path.join(pipeline, "trace"), { recursive: true });
    fs.writeFileSync(
      path.join(pipeline, "trace", "run1.jsonl"),
      jsonl([
        { run_id: "run1", issue: 7, ts: "2026-03-02T00:00:00Z", kind: "backtrack" },
        { run_id: "run1", issue: 7, ts: "2026-03-02T00:00:00Z", kind: "stage_start" },
        { run_id: "run1", issue: 8, ts: "2026-01-02T00:00:00Z", kind: "backtrack" },
      ])
    );
    const r = await readChangeSignals(root, filter, vi.fn());
    expect(r?.backtracks).toEqual([{ issueNumber: 7, runId: "run1", ts: "2026-03-02T00:00:00Z" }]);
  });

  it("reads planned files from planning context and never passes an invalid SHA to git", async () => {
    fs.writeFileSync(
      path.join(pipeline, "survival-records.jsonl"),
      jsonl([
        {
          merge_commit_sha: "abc1234",
          issue_number: 3,
          merged_at: "2026-03-05T00:00:00Z",
          verdict: "survived",
        },
        {
          merge_commit_sha: "; rm -rf /",
          issue_number: 4,
          merged_at: "2026-03-06T00:00:00Z",
          verdict: "survived",
        },
      ])
    );
    fs.writeFileSync(
      path.join(pipeline, "planning-3.json"),
      JSON.stringify({ files_to_create: ["n.ts"], files_to_modify: ["m.ts"] })
    );
    const reader = vi.fn(async () => ["m.ts"]);
    const r = await readChangeSignals(root, filter, reader);
    expect(reader).toHaveBeenCalledTimes(1);
    expect(reader).toHaveBeenCalledWith(root, "abc1234");
    expect(r?.planComparisons).toEqual([
      { issueNumber: 3, plannedFiles: ["n.ts", "m.ts"], mergedFiles: ["m.ts"] },
      { issueNumber: 4, plannedFiles: null, mergedFiles: [] },
    ]);
  });
});
