import { describe, expect, it } from "vitest";

import {
  describeToolCallCorrelationGap,
  ToolCallLog,
  TOOL_CALL_LOG_MAX_ENTRIES,
} from "../../src/utils/skillRunner";

/**
 * Tool-call visibility was lost for every run since #143 removed
 * `Dashboard.writeBackupHistoryRecord` — the only writer that ever persisted
 * `tool_calls` into run history. #144 restores it by generalizing the
 * existing `RecentBashRing` pattern (Bash-only) to a `ToolCallLog` that
 * captures every tool, forwarded through the existing Go-authoritative
 * `pipeline.stageResult` channel instead of reintroducing a second writer.
 *
 * These tests prove the fix is not Bash-only: a non-Bash tool (Read/Edit)
 * must produce an entry, which the old deleted writer did and a
 * Bash-scoped replacement would not.
 */
describe("ToolCallLog — all-tools call log (Issue #144)", () => {
  it("records a non-Bash tool_use + tool_result pair", () => {
    const log = new ToolCallLog();

    log.observeToolUse("Read", { file_path: "internal/state/history.go" }, "tool-1");
    log.observeToolResult("tool-1", false, "file contents");

    expect(log.size).toBe(1);
    const [entry] = log.snapshot();
    expect(entry.tool).toBe("Read");
    expect(entry.target).toBe("internal/state/history.go");
    expect(entry.result).toBe("file contents");
    expect(entry.error).toBeUndefined();
    expect(entry.timestamp).toBeDefined();
  });

  it("records an Edit tool call and marks a failed result as an error", () => {
    const log = new ToolCallLog();

    log.observeToolUse("Edit", { file_path: "src/foo.ts" }, "tool-2");
    log.observeToolResult("tool-2", true, "old_string not found");

    const [entry] = log.snapshot();
    expect(entry.tool).toBe("Edit");
    expect(entry.error).toBe("old_string not found");
    expect(entry.result).toBeUndefined();
  });

  it("captures Bash calls too — it is a superset, not a replacement", () => {
    const log = new ToolCallLog();
    log.observeToolUse("Bash", { command: "go test ./..." }, "tool-3");
    log.observeToolResult("tool-3", false, "ok");

    expect(log.size).toBe(1);
    expect(log.snapshot()[0].tool).toBe("Bash");
  });

  it("dedupes a tool_use delivered under the same id twice", () => {
    const log = new ToolCallLog();
    log.observeToolUse("Read", { file_path: "a.ts" }, "dup-id");
    log.observeToolUse("Read", { file_path: "a.ts" }, "dup-id");
    expect(log.size).toBe(1);
  });

  it("bounds retention at TOOL_CALL_LOG_MAX_ENTRIES", () => {
    const log = new ToolCallLog();
    for (let i = 0; i < TOOL_CALL_LOG_MAX_ENTRIES + 25; i++) {
      log.observeToolUse("Read", { file_path: `file-${i}.ts` }, `id-${i}`);
    }
    expect(log.size).toBe(TOOL_CALL_LOG_MAX_ENTRIES);
    // Oldest-first: the retained window should end on the most recent entry.
    const snapshot = log.snapshot();
    expect(snapshot[snapshot.length - 1].target).toBe(`file-${TOOL_CALL_LOG_MAX_ENTRIES + 24}.ts`);
  });

  it("ignores a call with no tool name", () => {
    const log = new ToolCallLog();
    log.observeToolUse("", { file_path: "a.ts" }, "id-empty");
    expect(log.size).toBe(0);
  });

  it("a tool_result with no matching tool_use is a no-op", () => {
    const log = new ToolCallLog();
    log.observeToolResult("unknown-id", false, "result");
    expect(log.size).toBe(0);
  });
});

/**
 * The same correlation hole #302 closed on {@link RecentBashRing}, one level up.
 *
 * `ToolCallLog` joins tool_results to calls by `toolUseId`, and `tokenParser`
 * drops non-string ids by design (#155/#169). A stage whose events arrive
 * id-less therefore records calls that no result can ever join: entries are
 * pushed but never written to `byId`, `observeToolResult` returns silently, and
 * nothing on the public surface counts the loss. The Dashboard then renders
 * every row with no result, no error and no duration — indistinguishable from
 * a stage where every call quietly succeeded.
 *
 * Same two quantities as #302, and for the same reason:
 *
 * - `retainedIndexedCount` — of the calls the exit record will actually carry,
 *   how many could ever bind a result. This is what the check asks.
 * - `correlatedResults` / `capturedTotal` — lifetime tallies, supporting data
 *   only. Correlation races the kill, and a lifetime count says nothing about
 *   the 200 retained rows.
 *
 * (#402)
 */
describe("ToolCallLog — indexed-ness, joins, and duration (#402)", () => {
  const use = (log: ToolCallLog, file: string, id?: string) =>
    log.observeToolUse("Read", { file_path: file }, id);

  it("counts NO retained entry as indexed when every tool_use was id-less", () => {
    const log = new ToolCallLog();
    use(log, "src/a.ts");
    use(log, "src/b.ts");

    // Calls captured — so `size` cannot report the gap …
    expect(log.size).toBe(2);
    expect(log.capturedTotal).toBe(2);
    // … and not one of them is reachable by a tool_result.
    expect(log.retainedIndexedCount).toBe(0);
    expect(log.correlatedResults).toBe(0);
  });

  it("counts an entry as indexed the moment its id is written, before any result", () => {
    // Indexed-ness is reachability, not arrival: an entry with an id is one a
    // tool_result CAN find, whether or not the stage lived long enough for it
    // to. Conflating the two misdiagnoses a stage killed mid-call.
    const log = new ToolCallLog();
    use(log, "src/a.ts", "toolu_01");

    expect(log.retainedIndexedCount).toBe(1);
    expect(log.correlatedResults).toBe(0);
  });

  it("tracks indexed-ness per entry in a mixed stage", () => {
    const log = new ToolCallLog();
    use(log, "src/a.ts", "toolu_01");
    use(log, "src/b.ts");
    use(log, "src/c.ts", "toolu_03");

    expect(log.size).toBe(3);
    expect(log.retainedIndexedCount).toBe(2);
  });

  it("does not count a rejected or duplicate observation toward the lifetime total", () => {
    const log = new ToolCallLog();
    log.observeToolUse("", { file_path: "a.ts" }, "toolu_01"); // no tool name
    use(log, "src/a.ts", "toolu_02");
    use(log, "src/a.ts", "toolu_02"); // same tool_use, second delivery shape

    expect(log.capturedTotal).toBe(1);
  });

  it("counts each result that joins its call", () => {
    const log = new ToolCallLog();
    use(log, "src/a.ts", "toolu_01");
    use(log, "src/b.ts", "toolu_02");

    log.observeToolResult("toolu_01", false, "ok");
    expect(log.correlatedResults).toBe(1);
    log.observeToolResult("toolu_02", true, "boom");
    expect(log.correlatedResults).toBe(2);
  });

  it("does not count a result for a call it never retained", () => {
    const log = new ToolCallLog();
    use(log, "src/a.ts", "toolu_01");
    log.observeToolResult("toolu_unknown", true, "boom");

    expect(log.correlatedResults).toBe(0);
  });

  it("counts the join once when the same result is delivered twice", () => {
    // A repeat delivery is not a second correlated result; counting it would
    // let one chatty result mask a stage where nothing else joined at all.
    const log = new ToolCallLog();
    use(log, "src/a.ts", "toolu_01");
    log.observeToolResult("toolu_01", false, "ok");
    log.observeToolResult("toolu_01", false, "ok");

    expect(log.correlatedResults).toBe(1);
  });

  it("counts the join once even when the result carried no text at all", () => {
    // The join must be decided by whether this entry was ever joined, NOT by
    // whether `result`/`error` ended up populated: a successful call with an
    // empty tool_result body leaves BOTH legitimately undefined, so a
    // result-shaped predicate would recount every redelivery of it forever.
    const log = new ToolCallLog();
    use(log, "src/a.ts", "toolu_01");
    log.observeToolResult("toolu_01", false);
    log.observeToolResult("toolu_01", false);

    const [entry] = log.snapshot();
    expect(entry.result).toBeUndefined();
    expect(entry.error).toBeUndefined();
    expect(log.correlatedResults).toBe(1);
  });

  it("populates duration_ms on the joined entry and leaves it absent on the unjoined one", () => {
    // `duration_ms` is the wire field the Dashboard already reads
    // (Dashboard.ts / DashboardState.ts); nothing ever wrote it. The join is
    // the only moment both ends of the interval are known.
    const log = new ToolCallLog();
    use(log, "src/joined.ts", "toolu_01");
    use(log, "src/never-joined.ts", "toolu_02"); // still in flight at exit
    log.observeToolResult("toolu_01", false, "ok");

    const [joined, unjoined] = log.snapshot();
    expect(typeof joined.duration_ms).toBe("number");
    expect(joined.duration_ms).toBeGreaterThanOrEqual(0);
    expect(Object.prototype.hasOwnProperty.call(unjoined, "duration_ms")).toBe(false);
  });

  it("leaves duration_ms absent on an id-less call — the honesty the detector reports", () => {
    const log = new ToolCallLog();
    use(log, "src/a.ts");
    log.observeToolResult("toolu_01", false, "ok"); // nothing to bind to

    const [entry] = log.snapshot();
    expect(Object.prototype.hasOwnProperty.call(entry, "duration_ms")).toBe(false);
  });

  it("drops indexed-ness with the entry it belonged to, because the window is the record", () => {
    // Partial drift in miniature: the one good entry evicts, and what remains —
    // which is what gets written — is entirely unreachable.
    const log = new ToolCallLog();
    use(log, "src/first.ts", "toolu_00");
    log.observeToolResult("toolu_00", false, "ok");
    for (let i = 1; i <= TOOL_CALL_LOG_MAX_ENTRIES; i++) use(log, `src/drifted-${i}.ts`);

    expect(log.size).toBe(TOOL_CALL_LOG_MAX_ENTRIES);
    expect(log.retainedIndexedCount).toBe(0);
    // The lifetime tally still remembers that correlation once worked …
    expect(log.correlatedResults).toBe(1);
    // … which is exactly why it cannot be the predicate.
  });

  it("counts every call it ever saw, not just the retained window", () => {
    const log = new ToolCallLog();
    for (let i = 0; i < TOOL_CALL_LOG_MAX_ENTRIES + 25; i++)
      use(log, `src/f-${i}.ts`, `toolu_${i}`);

    expect(log.size).toBe(TOOL_CALL_LOG_MAX_ENTRIES);
    expect(log.capturedTotal).toBe(TOOL_CALL_LOG_MAX_ENTRIES + 25);
  });

  it("never leaks the internal bookkeeping into the serialised record", () => {
    // `tool_calls` crosses IPC into the persisted run record and must match Go's
    // ToolCallRecord exactly; a stray diagnostic field would be schema drift.
    const log = new ToolCallLog();
    use(log, "src/a.ts", "toolu_01");
    log.observeToolResult("toolu_01", false, "ok");
    use(log, "src/b.ts");

    const wireKeys = ["tool", "target", "timestamp", "duration_ms", "result", "error"];
    for (const entry of log.snapshot()) {
      for (const key of Object.keys(entry)) expect(wireKeys).toContain(key);
      expect(Object.prototype.hasOwnProperty.call(entry, "indexed")).toBe(false);
      expect(Object.prototype.hasOwnProperty.call(entry, "joined")).toBe(false);
    }
  });
});

/**
 * The self-check itself, sibling to `describeForensicCaptureGap` (#147/#302).
 *
 * It runs inside `runStageSkillHeadless`, which spawns a subprocess and cannot
 * be exercised cheaply — the same constraint that put `extractBashCommand`,
 * `RecentBashRing` and the bash detector out at module scope. An untestable
 * detector is how #147 stayed silent through 2,533 records.
 *
 * The predicate is "none of the RETAINED calls is indexed", not "the lifetime
 * correlated count is zero", for the reasons #302 established: the lifetime
 * form misdiagnoses a stage killed mid-call and goes blind to partial drift.
 */
describe("describeToolCallCorrelationGap — the tool-call log's self-check (#402)", () => {
  const use = (log: ToolCallLog, file: string, id?: string) =>
    log.observeToolUse("Read", { file_path: file }, id);

  const check = (log: ToolCallLog, parsedToolEventCount: number) =>
    describeToolCallCorrelationGap({
      stage: "feature-dev",
      parsedToolEventCount,
      retainedCalls: log.size,
      retainedIndexedCalls: log.retainedIndexedCount,
      capturedTotal: log.capturedTotal,
      correlatedResults: log.correlatedResults,
    });

  it("says nothing when the stage parsed no tool events at all", () => {
    // A stage that genuinely ran nothing is a different (already-reported)
    // condition, not a capture gap — and an empty log is exactly what it looks
    // like, so without this guard every such stage would warn.
    expect(check(new ToolCallLog(), 0)).toBeUndefined();
  });

  it("warns when tool events were parsed but no tool call was recorded", () => {
    const warning = check(new ToolCallLog(), 214);

    expect(warning).toBeDefined();
    expect(warning).toContain("[forensic-capture-gap]");
    expect(warning).toContain("feature-dev");
    expect(warning).toContain("parsed 214 tool event(s)");
    expect(warning).toContain("(Issue #402)");
  });

  it("warns when NONE of the retained calls carried a usable id", () => {
    // `size > 0` suppresses the first arm, so without this one the stage reports
    // nothing and its record — populated `tool_calls`, every result, error and
    // duration absent — renders as calls that all quietly succeeded.
    const log = new ToolCallLog();
    use(log, "src/a.ts");
    use(log, "src/b.ts");
    // The results still arrive; they have nothing to bind to.
    log.observeToolResult("toolu_01", true, "boom");

    const warning = check(log, 214);

    expect(warning).toBeDefined();
    expect(warning).toContain("[forensic-capture-gap]");
    expect(warning).toContain("feature-dev");
    // Composed fragments, not bare digits: `toContain("2")` would pass on the
    // "2" inside "214" and survive the count being wrong entirely.
    expect(warning).toContain("captured 2 tool call(s)");
    expect(warning).toContain("parsed 214 tool event(s)");
    expect(warning).toContain("(0 result(s) correlated over the stage)");
    expect(warning).toContain("NONE of the 2 tool call(s) retained");
    expect(warning).toContain("(Issue #402)");
  });

  it("warns when the one correlated call evicted and the retained window is id-less", () => {
    // Correlation demonstrably worked (lifetime tally 1), so a lifetime
    // predicate stays SILENT — while the record actually being written is 200
    // calls with no results and no way to have had any.
    const log = new ToolCallLog();
    use(log, "src/first.ts", "toolu_00");
    log.observeToolResult("toolu_00", false, "ok");
    for (let i = 1; i <= TOOL_CALL_LOG_MAX_ENTRIES; i++) use(log, `src/drifted-${i}.ts`);

    expect(log.correlatedResults).toBe(1); // the shape that would suppress it
    const warning = check(log, 133);

    expect(warning).toBeDefined();
    expect(warning).toContain("(Issue #402)");
    expect(warning).toContain(`NONE of the ${TOOL_CALL_LOG_MAX_ENTRIES} tool call(s) retained`);
    // Lifetime figures are supporting data, and honestly labelled — they
    // describe the stage, not the retained window.
    expect(warning).toContain(`captured ${TOOL_CALL_LOG_MAX_ENTRIES + 1} tool call(s)`);
    expect(warning).toContain("(1 result(s) correlated over the stage)");
  });

  it("says nothing when the only call had a good id and the stage died first", () => {
    // Zero correlated results, and nothing wrong: the stage was killed while
    // its first-and-only call was still in flight. A lifetime predicate fires
    // here and blames the parser — a confident wrong diagnosis.
    const log = new ToolCallLog();
    use(log, "src/a.ts", "toolu_01");

    expect(log.correlatedResults).toBe(0);
    expect(check(log, 61)).toBeUndefined();
  });

  it("says nothing when a single id-less call sits among indexed, joined ones", () => {
    // Occasional id-less events are not a divergence; the record still carries
    // results. Only a wholly unreachable window is.
    const log = new ToolCallLog();
    use(log, "src/a.ts", "toolu_01");
    use(log, "src/interstitial.ts");
    use(log, "src/b.ts", "toolu_03");
    log.observeToolResult("toolu_01", false, "ok");
    log.observeToolResult("toolu_03", true, "boom");

    expect(check(log, 40)).toBeUndefined();
  });

  it("says nothing on a fully healthy stage", () => {
    const log = new ToolCallLog();
    use(log, "src/a.ts", "toolu_01");
    log.observeToolResult("toolu_01", false, "ok");

    expect(check(log, 12)).toBeUndefined();
  });
});
