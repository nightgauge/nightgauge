/**
 * A history record whose branch is UNDETERMINED survives every TypeScript
 * reader (#397).
 *
 * THE INPUT IS NOT AUTHORED HERE. Both fixtures are records the REAL Go binary
 * wrote for a run whose feature branch could not be determined from any source,
 * captured verbatim by `scripts/capture-undetermined-branch-fixture.sh` — one
 * from each writer #397 changed:
 *
 *   - `completed-run-record.jsonl` — `state.HistoryWriter.BuildV2Record`, the
 *     primary site, reached over the real IPC wire (transitions +
 *     `pipeline.notifyComplete`). A completed two-stage run with real per-stage
 *     token and cost blocks, and no branch.
 *   - `crash-record.jsonl` — `SynthesizeOrchestratorCrashRecord`, the second
 *     site, reached through real startup crash recovery.
 *
 * See the fixture README for provenance and the regeneration command.
 * Hand-writing `{"branch": ""}` would be this file asserting its own belief
 * about the writer — the #166 failure mode, and precisely the belief that was
 * wrong before #397: the writer emitted `"branch":"feat/{N}"` for every run it
 * could not resolve, so a record that knew nothing was byte-indistinguishable
 * from one that knew.
 *
 * WHAT THESE TESTS DO AND DO NOT PIN. The captures are static bytes, so they
 * cannot fail when a Go writer regresses — they only go stale. The writers are
 * pinned in Go, by
 * `TestScheduler_RecordV2History_UnresolvedBranch_KeyPresentEmptyMeansUndetermined`
 * and `TestSynthesizeOrchestratorCrashRecord_NeverFabricatesABranch`. What is
 * pinned here is the READER half: that a real branchless record survives the
 * schema, the reader, and the dashboard import.
 *
 * The contract has two halves and they fail in opposite directions:
 *
 *   - Go never fabricates, so "" now genuinely reaches these readers. The LIVE
 *     pre-#397 dropper was the strict SCHEMA: `branch: z.string()` (required)
 *     failed `safeParse` for a record without the key, dumping it into
 *     `executionHistoryReader`'s lenient raw-cast fallback — a bare `as`
 *     cast that keeps every key verbatim, unlike a successful `safeParse`,
 *     which strips unrecognized ones. `z.string().default("")` closes that.
 *   - `DashboardState.importParsedRunRecord`'s truthiness guard dropped any
 *     record with a falsy branch — the whole run, not just the field. That
 *     guard is why the fabrication existed in the first place. It runs only on
 *     the LEGACY no-TelemetryStore configuration (see the two `describe`
 *     blocks below), so both paths get their own test.
 *
 * The absent-key case is DERIVED from a capture (the key is deleted from the
 * parsed object), not invented: our writer always emits the key, so no capture
 * can produce that shape — but any other producer of a v1/v2 record can.
 */

import { readFileSync, mkdtempSync, mkdirSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import {
  ExecutionHistoryRunRecordSchema,
  ExecutionHistoryRunRecordV2Schema,
  ExecutionHistoryRunRecordV3Schema,
} from "../../../src/schemas/executionHistory";
import { ExecutionHistoryReader } from "../../../src/utils/executionHistoryReader";
import { TelemetryStore } from "../../../src/services/TelemetryStore";
import { DashboardState } from "../../../src/views/dashboard/DashboardState";
import { getRunsTabHtml } from "../../../src/views/dashboard/tabs/RunsTabHtml";
import type { RunsListData } from "../../../src/views/dashboard/DashboardState";
import type { RunsEntry } from "../../../src/services/IpcClientBase";
import { createMockMemento } from "../../mocks/memento";

// ---------------------------------------------------------------------------
// The captured fixtures
// ---------------------------------------------------------------------------

const FIXTURE_DIR = join(__dirname, "..", "..", "fixtures", "undetermined-branch");

const RUN_LINE = readFileSync(join(FIXTURE_DIR, "completed-run-record.jsonl"), "utf-8").trim();
const CRASH_LINE = readFileSync(join(FIXTURE_DIR, "crash-record.jsonl"), "utf-8").trim();
const RUN_INDEX = JSON.parse(
  readFileSync(join(FIXTURE_DIR, "completed-run-index.json"), "utf-8")
) as { entries: Array<Record<string, unknown>> };

function parsedRun(): Record<string, unknown> {
  return JSON.parse(RUN_LINE) as Record<string, unknown>;
}

function parsedCrash(): Record<string, unknown> {
  return JSON.parse(CRASH_LINE) as Record<string, unknown>;
}

describe("undetermined-branch history record (#397)", () => {
  describe("the fixtures match their provenance", () => {
    // These check that the committed captures still show what the README says
    // they show. They are assertions about FILES, not about the current Go
    // writers — a regression in Go leaves these green and the fixture stale,
    // which is why the writers carry their own Go tests (see the file header).
    it.each([
      ["BuildV2Record (completed run)", () => RUN_LINE],
      ["SynthesizeOrchestratorCrashRecord", () => CRASH_LINE],
    ])("%s carries the branch key, present and empty, in the bytes Go emitted", (_name, line) => {
      // Asserted on the raw line, not the parsed object: JSON.parse reports the
      // same `""` for a key written empty and a key that was never there, and
      // key-present-AND-empty is the entire contract.
      expect(line()).toContain('"branch":""');
      expect(line()).not.toContain("feat/");

      const rec = JSON.parse(line()) as Record<string, unknown>;
      expect(Object.keys(rec)).toContain("branch");
      expect(rec.branch).toBe("");
      // A positive issue number is what the pre-#397 writers turned into
      // `feat/{N}`, so a capture without one would be evidence of nothing.
      expect(rec.issue_number as number).toBeGreaterThan(0);
    });

    it('carries "source":"scheduler" in the bytes Go emitted, not merely some member (#446)', () => {
      // A VALUE pin on the raw line, for the same reason the branch pin above
      // is one. The strict-parse test below is satisfied by ANY member of
      // MODEL_SELECTION_SOURCES, so a fixture regenerated after a vocabulary
      // rename would keep it green while the real corpus — 207/207 entries
      // reading "scheduler" — dropped back to the lenient fallback. That is the
      // exact defect #446 closed, so the byte the corpus actually carries is
      // pinned to the file independently of what the enum happens to list.
      expect(RUN_LINE).toContain('"source":"scheduler"');
    });

    it("carries an empty branch on the index entry Go wrote alongside the run", () => {
      expect(RUN_INDEX.entries).toHaveLength(1);
      expect(RUN_INDEX.entries[0]).toHaveProperty("branch");
      expect(RUN_INDEX.entries[0].branch).toBe("");
    });
  });

  describe("schemas/executionHistory", () => {
    it("accepts the captured record and keeps the branch empty", () => {
      const crash = ExecutionHistoryRunRecordV3Schema.safeParse(parsedCrash());
      expect(crash.success).toBe(true);
      expect(crash.success && crash.data.branch).toBe("");
    });

    // The completed-run capture, WHOLE — no field deleted to get it past the
    // schema. Until #446 this assertion was impossible: Go writes
    // `stages.<s>.model_selection.source: "scheduler"` and this package's
    // `source` enum listed nine values that no writer emits, so every record
    // carrying a model_selection failed safeParse and reached the reader's
    // lenient raw-cast fallback — where no zod default runs at all. A test
    // could only reach the schema behaviour it meant to test by deleting the
    // field the drift lived in. #446 made the SDK's MODEL_SELECTION_SOURCES the
    // single vocabulary authority (Go mirrors it, pinned by
    // TestModelSelectionSourcesPinnedToSDK), so the bytes Go actually wrote now
    // validate as captured.
    it("STRICTLY parses the captured completed run, model_selection included (#446)", () => {
      const run = parsedRun();
      // Guard the premise: a capture that lost its model_selection would make
      // this test pass for the wrong reason.
      const stages = run.stages as Record<string, { model_selection?: { source?: string } }>;
      const sources = Object.values(stages).map((s) => s.model_selection?.source);
      expect(sources.length).toBeGreaterThan(0);
      expect(sources.every((s) => typeof s === "string" && s.length > 0)).toBe(true);

      const parsed = ExecutionHistoryRunRecordV2Schema.safeParse(run);
      expect(parsed.success ? null : parsed.error.issues).toBeNull();
      expect(parsed.success).toBe(true);
      // The vocabulary reached the parsed record intact, not coerced away.
      expect(
        parsed.success && Object.values(parsed.data.stages).map((s) => s.model_selection?.source)
      ).toEqual(sources);
    });

    it('normalizes an ABSENT branch key to "" instead of rejecting the record', () => {
      // Derived from a capture, not invented: delete the one key. Our writer
      // never omits it, so this shape cannot be captured — but rejecting it
      // costs the whole record, not the field.
      const v3 = parsedCrash();
      delete v3.branch;
      const v3Result = ExecutionHistoryRunRecordV3Schema.safeParse(v3);
      expect(v3Result.success).toBe(true);
      expect(v3Result.success && v3Result.data.branch).toBe("");

      const v2 = { ...parsedCrash(), schema_version: "2" } as Record<string, unknown>;
      delete v2.branch;
      const v2Result = ExecutionHistoryRunRecordV2Schema.safeParse(v2);
      expect(v2Result.success).toBe(true);
      expect(v2Result.success && v2Result.data.branch).toBe("");

      // V1 is a separate declaration, not an extension of V2 — assert it too.
      const v1 = { ...parsedCrash(), schema_version: "1" } as Record<string, unknown>;
      delete v1.branch;
      const v1Result = ExecutionHistoryRunRecordSchema.safeParse(v1);
      expect(v1Result.success).toBe(true);
      expect(v1Result.success && v1Result.data.branch).toBe("");
    });
  });

  describe("readers", () => {
    let root: string;

    beforeEach(() => {
      ExecutionHistoryReader.clearCache();
      root = mkdtempSync(join(tmpdir(), "ng-undetermined-branch-"));
      mkdirSync(join(root, ".nightgauge", "pipeline", "history"), { recursive: true });
    });

    afterEach(() => {
      ExecutionHistoryReader.clearCache();
      rmSync(root, { recursive: true, force: true });
    });

    function writeHistory(line: string): void {
      const day = new Date().toISOString().slice(0, 10);
      writeFileSync(join(root, ".nightgauge", "pipeline", "history", `${day}.jsonl`), line + "\n");
    }

    describe("the production attach path (TelemetryStore)", () => {
      // This is what the shipping extension runs. `bootstrap` constructs a
      // TelemetryStore whenever the workspace root is known, and
      // `backfillFromPipelineArtifacts` then delegates to
      // `loadFromTelemetryStore()` — ExecutionHistoryReader.readAll →
      // buildIndexEntry → indexEntryToRunSummary, none of which ever had a
      // branch guard. `importParsedRunRecord` is not on this path at all.
      function attach(): DashboardState {
        return new DashboardState(createMockMemento(), root, new TelemetryStore(root));
      }

      it("imports the captured run and keeps its branch empty", async () => {
        writeHistory(RUN_LINE);

        const state = attach();
        const imported = await state.backfillFromPipelineArtifacts();
        expect(imported).toBe(1);

        const history = state.getHistory();
        expect(history).toHaveLength(1);
        expect(history[0].issueNumber).toBe(parsedRun().issue_number);
        // The run is kept AND told the truth about its branch — never a
        // placeholder that reads like a branch it actually used.
        expect(history[0].branch).toBe("");
      });

      it('normalizes an ABSENT branch key to "" rather than undefined', async () => {
        // One derivation from the capture: the `branch` key is deleted — the
        // shape our writer cannot produce but other producers can. Everything
        // else, model_selection included, is the captured record, so this
        // exercises the strict schema rather than the lenient raw-cast fallback
        // (which is all a record with a model_selection could reach before
        // #446).
        const record = parsedRun();
        delete record.branch;
        // A schema-only witness, independent of the branch assertion below
        // (see the comment further down for why that independence matters):
        // Zod's z.object() STRIPS unrecognized keys from its parsed output by
        // default, while the reader's lenient fallback is a bare `as` cast
        // that keeps everything verbatim. Planting an unknown top-level key
        // and checking its ABSENCE after a read proves the strict safeParse
        // ran, without depending on any particular field's value semantics.
        //
        // Until #682 this used the #3228 `cost_source` backfill for the same
        // purpose (its presence after a read was proof of the strict path,
        // since the captured per_stage blocks carry none of their own). #682
        // removed that backfill — Go now writes cost_source itself, so the
        // reader no longer manufactures it — which retired that witness
        // along with it.
        (record as Record<string, unknown>).__strict_parse_witness = true;
        writeHistory(JSON.stringify(record));

        const state = attach();
        const imported = await state.backfillFromPipelineArtifacts();
        expect(imported).toBe(1);

        const history = state.getHistory();
        expect(history).toHaveLength(1);
        // `undefined` is the value PipelineRunSummary.branch is typed not to
        // hold, and the value a bare `z.string()` rejection would have cost the
        // whole record over.
        expect(history[0].branch).toBe("");
        expect(history[0].branch).not.toBeUndefined();

        // #446's live-path regression pin, stated rather than implied. The two
        // assertions above only prove the strict branch ran BECAUSE neither
        // projection coerces (executionHistoryWriter's `branch: record.branch`
        // and DashboardState's `branch: entry.branch`); a defensive `?? ""`
        // added to either would silently delete the gate while every test
        // stayed green. This witness does not depend on that: the
        // unknown-key strip only happens inside a successful `safeParse`, so
        // its ABSENCE after a read is unambiguous proof the strict path
        // executed on the live reader — for a record carrying a
        // model_selection, which before #446 could only reach the lenient
        // raw cast.
        const recs = await ExecutionHistoryReader.readAll(root);
        expect(recs).toHaveLength(1);
        expect(recs[0]).not.toHaveProperty("__strict_parse_witness");
      });

      it("rebuilds the same branch Go wrote into index.json", async () => {
        // The index is the SECOND surface carrying the branch
        // (state.V2IndexEntry → HistoryIndexEntry → indexEntryToRunSummary).
        // Go's own index for this exact record is committed next to it, so the
        // TypeScript projection can be checked against it rather than against a
        // guess.
        writeHistory(RUN_LINE);

        const index = await new TelemetryStore(root).rebuildIndex();
        expect(index.entries).toHaveLength(1);
        expect(index.entries[0].branch).toBe(RUN_INDEX.entries[0].branch);
        expect(index.entries[0].branch).toBe("");
      });
    });

    describe("the legacy no-TelemetryStore path (importParsedRunRecord)", () => {
      // Reached only when DashboardState is constructed without a
      // TelemetryStore. This is where the truthiness guard lived: it dropped
      // the whole record — every stage, token and cost — over a falsy branch,
      // which is why Go fabricated one.
      function legacy(): DashboardState {
        return new DashboardState(createMockMemento(), root);
      }

      it("imports the captured run instead of dropping it", async () => {
        writeHistory(RUN_LINE);
        const state = legacy();

        const imported = await state.backfillFromPipelineArtifacts();
        expect(imported).toBe(1);

        const history = state.getHistory();
        expect(history).toHaveLength(1);
        expect(history[0].issueNumber).toBe(parsedRun().issue_number);
        expect(history[0].branch).toBe("");
      });

      it('normalizes an ABSENT branch key to "" rather than undefined', async () => {
        // Derived from the capture by deleting the one key. This path builds
        // the summary by hand rather than through a schema, so the `typeof`
        // coercion at the summary site is the only thing standing between an
        // absent key and `undefined` on a field typed `string`.
        const record = parsedRun();
        delete record.branch;
        writeHistory(JSON.stringify(record));

        const state = legacy();
        const imported = await state.backfillFromPipelineArtifacts();
        expect(imported).toBe(1);

        const history = state.getHistory();
        expect(history).toHaveLength(1);
        expect(history[0].branch).toBe("");
        expect(history[0].branch).not.toBeUndefined();
      });
    });
  });

  describe("rendering", () => {
    // NOTE ON SCOPE: this cell renders a `RunsEntry`, which the extension gets
    // from the hosted platform (`platform.getAnalyticsRuns`), not from the local
    // record above. The two contracts are separate and are asserted separately:
    // the local record survives the readers (above), and the Runs tab prints an
    // explicit label instead of a blank cell for a run with no branch recorded
    // (here). The VALUE under test is still taken from the capture rather than
    // typed in, so the empty string being rendered is the one Go actually wrote.
    const CAPTURED_BRANCH = parsedRun().branch as string;

    function makeRunsData(branch: string): RunsListData {
      const entry: RunsEntry = {
        issue_number: 812,
        title: "Undetermined branch for the whole run",
        branch,
        outcome: "complete",
        duration_ms: 600_000,
        total_cost_usd: "0.44",
        started_at: new Date().toISOString(),
      };
      return {
        entries: [entry],
        filters: { dateFrom: "", dateTo: "", outcomeFilter: "", branchFilter: "" },
        pagination: {
          page: 0,
          pageSize: 20,
          totalCount: 1,
          hasMore: false,
          cursorStack: [undefined],
        },
        isLoading: false,
        hasAccess: true,
      };
    }

    it("labels an empty branch explicitly rather than rendering an empty cell", () => {
      expect(CAPTURED_BRANCH).toBe("");
      const html = getRunsTabHtml(makeRunsData(CAPTURED_BRANCH));
      expect(html).toContain("(branch not determined)");
      // The blank cell is the failure mode being closed: a reader cannot tell
      // "no branch" from "the renderer lost it".
      expect(html).not.toContain('<span class="runs-branch" title=""></span>');
    });

    it("treats a whitespace-only branch as no branch", () => {
      // Same visual failure as "", one space later.
      const html = getRunsTabHtml(makeRunsData("   "));
      expect(html).toContain("(branch not determined)");
    });

    it("leaves a real branch untouched", () => {
      const html = getRunsTabHtml(makeRunsData("fix/397-no-branch-fabrication"));
      expect(html).toContain("fix/397-no-branch-fabrication");
      expect(html).not.toContain("(branch not determined)");
    });
  });
});
