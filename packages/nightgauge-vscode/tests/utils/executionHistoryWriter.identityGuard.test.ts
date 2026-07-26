/**
 * executionHistoryWriter.identityGuard.test.ts
 *
 * #307 / #141 — the history writer must reject a run record that does not name
 * both the repository it belongs to and the run it describes.
 *
 * #307 established the rule for a PRESENT-but-empty identity (null/""), the
 * signature of a record assembled from shared/cleared per-run state and
 * mis-routed into a sibling repo's history. It deliberately exempted an ABSENT
 * identity as "the normal shape of the current builder".
 *
 * #141 removed that exemption, because the exemption was the hole: the builders
 * omitting repo/run_id were precisely the ones producing mis-filed, undedupable
 * records. Without run_id the idempotency key degrades to issue+timestamp, which
 * two producers spell differently and therefore never collide on — so an
 * identity-less record is both written to the wrong repository AND duplicated on
 * every finalize. Absent and empty are now treated identically: rejected.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import { ExecutionHistoryWriter } from "../../src/utils/executionHistoryWriter";
import type { ExecutionHistoryRecord } from "../../src/schemas/executionHistory";

vi.mock("node:fs/promises");

const WS = "/ws/platform";

/** Minimal run record; identity fields overridden per test. */
function runRecord(overrides: Record<string, unknown>): ExecutionHistoryRecord {
  return {
    record_type: "run",
    schema_version: 2,
    issue_number: 209,
    recorded_at: new Date("2026-07-19T17:31:34.563Z").toISOString(),
    outcome: "complete",
    stages: {},
    tokens: {
      total_input: 0,
      total_output: 0,
      total_cache_read: 0,
      total_cache_creation: 0,
      estimated_cost_usd: 0,
    },
    ...overrides,
  } as unknown as ExecutionHistoryRecord;
}

describe("ExecutionHistoryWriter identity guard (#307)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(fs.mkdir).mockResolvedValue(undefined);
    vi.mocked(fs.appendFile).mockResolvedValue();
    vi.mocked(fs.readFile).mockResolvedValue("" as unknown as Buffer);
    vi.mocked(fs.writeFile).mockResolvedValue();
    ExecutionHistoryWriter.lastValidationError = null;
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("rejects a run record with run_id: null and never appends it", async () => {
    const ok = await ExecutionHistoryWriter.appendRecord(
      WS,
      runRecord({ repo: null, run_id: null })
    );
    expect(ok).toBe(false);
    expect(fs.appendFile).not.toHaveBeenCalled();
    expect(ExecutionHistoryWriter.lastValidationError).toMatch(/#307/);
  });

  it("rejects a run record with an empty-string repo", async () => {
    const ok = await ExecutionHistoryWriter.appendRecord(
      WS,
      runRecord({ repo: "", run_id: "d25ec02e" })
    );
    expect(ok).toBe(false);
    expect(fs.appendFile).not.toHaveBeenCalled();
  });

  it("rejects a run record with an empty-string run_id", async () => {
    const ok = await ExecutionHistoryWriter.appendRecord(
      WS,
      runRecord({ repo: "acme/platform", run_id: "" })
    );
    expect(ok).toBe(false);
    expect(fs.appendFile).not.toHaveBeenCalled();
  });

  it("appends a run record that carries a real identity", async () => {
    await ExecutionHistoryWriter.appendRecord(
      WS,
      runRecord({ repo: "acme/platform", run_id: "d25ec02e-4368-43f1-a3b6-c7f669dfa95b" })
    );
    expect(fs.appendFile).toHaveBeenCalledTimes(1);
    const [filePath, line] = vi.mocked(fs.appendFile).mock.calls[0];
    expect(String(filePath)).toContain("/ws/platform/.nightgauge/pipeline/history/");
    expect(String(line)).toContain("d25ec02e-4368-43f1-a3b6-c7f669dfa95b");
  });

  it("rejects a run record whose identity fields are absent entirely (#141)", async () => {
    // Previously permitted: absent was treated as "the normal shape of the
    // current builder" rather than as corruption. That exemption is what let
    // undedupable, mis-filed records into the store, so absent is now rejected
    // exactly like empty.
    const ok = await ExecutionHistoryWriter.appendRecord(WS, runRecord({}));
    expect(ok).toBe(false);
    expect(fs.appendFile).not.toHaveBeenCalled();
    expect(ExecutionHistoryWriter.lastValidationError).toMatch(/#141/);
  });

  it("rejects a run record that names a repo but no run (#141)", async () => {
    const ok = await ExecutionHistoryWriter.appendRecord(WS, runRecord({ repo: "acme/platform" }));
    expect(ok).toBe(false);
    expect(fs.appendFile).not.toHaveBeenCalled();
  });

  it("rejects a run record that names a run but no repo (#141)", async () => {
    const ok = await ExecutionHistoryWriter.appendRecord(
      WS,
      runRecord({ run_id: "d25ec02e-4368-43f1-a3b6-c7f669dfa95b" })
    );
    expect(ok).toBe(false);
    expect(fs.appendFile).not.toHaveBeenCalled();
  });

  // A run belonging to repo A must land in repo A's history and nowhere else.
  // The identity-less record is the cross-contamination vector: with no repo on
  // the record, a writer rooted at the workspace's launch folder silently files
  // it under whichever repo happens to be workspaceFolders[0].
  it("writes each repo's run into that repo's own history directory (#141)", async () => {
    const repoA = "/ws/repo-a";
    const repoB = "/ws/repo-b";

    await ExecutionHistoryWriter.appendRecord(
      repoA,
      runRecord({ repo: "example/repo-a", run_id: "run-a", issue_number: 11 })
    );
    await ExecutionHistoryWriter.appendRecord(
      repoB,
      runRecord({ repo: "example/repo-b", run_id: "run-b", issue_number: 22 })
    );

    const writes = vi
      .mocked(fs.appendFile)
      .mock.calls.map(([p, line]) => ({ path: String(p), line: String(line) }));
    expect(writes).toHaveLength(2);

    const aWrites = writes.filter((w) => w.path.startsWith(`${repoA}/`));
    const bWrites = writes.filter((w) => w.path.startsWith(`${repoB}/`));
    expect(aWrites).toHaveLength(1);
    expect(bWrites).toHaveLength(1);

    // No cross-contamination in either direction.
    expect(aWrites[0].line).toContain("example/repo-a");
    expect(aWrites[0].line).not.toContain("example/repo-b");
    expect(bWrites[0].line).toContain("example/repo-b");
    expect(bWrites[0].line).not.toContain("example/repo-a");
  });
});
