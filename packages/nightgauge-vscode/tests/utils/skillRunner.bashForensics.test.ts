import { describe, expect, it } from "vitest";

import {
  LAST_BASH_COMMAND_MAX_CHARS,
  RECENT_BASH_MAX_ENTRIES,
  RecentBashRing,
  describeForensicCaptureGap,
  extractBashCommand,
} from "../../src/utils/skillRunner";

/**
 * `last_bash_command` is documented as the strongest single forensic anchor in
 * a stage-exit record, on the reasoning that most silent kills happen mid-Bash.
 * It was nonetheless empty in 100% of records ever written — 0 of 2,533 across
 * two multi-repo workspaces, including all 110 failures.
 *
 * The cause was shape blindness. The CLI delivers tool calls two ways: singular
 * `content_block_start` (`parsed.toolName`) and plural assistant-message
 * (`parsed.toolUses[]`). The progress monitor deliberately reads BOTH and duly
 * counted hundreds of tool events per stage; the forensic capture read only the
 * singular shape and saw none of them.
 *
 * These tests pin the extraction to the shape-agnostic contract, because the
 * capture itself lives inside a subprocess-spawning function that cannot be
 * exercised cheaply. (#147)
 */
describe("extractBashCommand — the last_bash_command forensic anchor", () => {
  it("extracts a Bash command regardless of which delivery shape supplied it", () => {
    const command = "flutter test integration_test/app_e2e/scoring_test.dart";

    // Singular `content_block_start` shape.
    expect(extractBashCommand("Bash", { command })).toBe(command);
    // Plural assistant-message `toolUses[]` shape — same call, same result.
    // This is the case that produced 0/2,533 in production.
    const fromPlural = [{ name: "Bash", input: { command } }].map((t) =>
      extractBashCommand(t.name, t.input)
    );
    expect(fromPlural).toEqual([command]);
  });

  it("ignores non-Bash tools so an unrelated call cannot clobber the anchor", () => {
    for (const tool of ["Read", "Edit", "Write", "Grep", "Task", "TodoWrite"]) {
      expect(extractBashCommand(tool, { command: "rm -rf /" })).toBeUndefined();
    }
  });

  it("returns undefined — not an empty string — when Bash carries no command", () => {
    // Callers use undefined to mean "leave the previous value intact". Returning
    // "" would blank a genuine earlier command and reintroduce the empty field.
    expect(extractBashCommand("Bash", {})).toBeUndefined();
    expect(extractBashCommand("Bash", undefined)).toBeUndefined();
    expect(extractBashCommand("Bash", { command: 42 })).toBeUndefined();
    expect(extractBashCommand("Bash", { command: null })).toBeUndefined();
  });

  it("elides an over-long command so one pathological call cannot bloat the record", () => {
    const long = "echo " + "x".repeat(LAST_BASH_COMMAND_MAX_CHARS * 2);
    const got = extractBashCommand("Bash", { command: long });
    expect(got).toBeDefined();
    expect(got).toHaveLength(LAST_BASH_COMMAND_MAX_CHARS + 1); // + the ellipsis
    expect(got?.endsWith("…")).toBe(true);
    expect(got?.startsWith("echo xxx")).toBe(true);
  });

  it("keeps a command that is exactly at the cap unelided", () => {
    const exact = "y".repeat(LAST_BASH_COMMAND_MAX_CHARS);
    expect(extractBashCommand("Bash", { command: exact })).toBe(exact);
  });
});

/**
 * One command is thin evidence. Stage subprocesses run with
 * `--no-session-persistence`, so no transcript survives the stage and the exit
 * record is the only durable account of what it did. A validate stage that
 * exited with `last_bash_command` = `true` is equally consistent with a benign
 * trailing `|| true` and with a stage that ran no verification at all, and
 * after the fact the two are indistinguishable. (#156)
 */
describe("RecentBashRing — the last N Bash commands", () => {
  const use = (ring: RecentBashRing, command: string, id?: string) =>
    ring.observeToolUse("Bash", { command }, id);

  it("answers the question one command could not: did it verify before the no-op tail?", () => {
    const ring = new RecentBashRing();
    use(ring, "npm run -w nightgauge-vscode vitest run", "t1");
    ring.observeToolResult("t1", true);
    use(ring, "true", "t2");
    ring.observeToolResult("t2", false);

    // The pre-#156 view is unchanged — `true`, exit 0 — and still says nothing.
    expect(ring.last()?.cmd).toBe("true");
    expect(ring.last()?.exit).toBe(0);
    // The ring says the suite ran, and that it failed.
    expect(ring.snapshot()).toEqual([
      { cmd: "npm run -w nightgauge-vscode vitest run", exit: 1 },
      { cmd: "true", exit: 0 },
    ]);
  });

  it("retains the NEWEST N commands and evicts the oldest", () => {
    const ring = new RecentBashRing();
    for (let i = 0; i < RECENT_BASH_MAX_ENTRIES * 3; i++) use(ring, `cmd-${i}`, `t${i}`);

    const snap = ring.snapshot();
    expect(snap).toHaveLength(RECENT_BASH_MAX_ENTRIES);
    // Evicting the newest would keep the stage's startup noise and discard the
    // commands nearest the failure — the exact opposite of what a retro needs.
    expect(snap[snap.length - 1]?.cmd).toBe(`cmd-${RECENT_BASH_MAX_ENTRIES * 3 - 1}`);
    expect(snap[0]?.cmd).toBe(`cmd-${RECENT_BASH_MAX_ENTRIES * 2}`);
  });

  it("counts a tool_use reported through BOTH stream shapes once", () => {
    // The CLI can deliver the same tool_use as a singular `content_block_start`
    // AND inside a complete `assistant` message. With a single slot the second
    // write was a harmless no-op; a ring would keep every command twice and
    // silently halve its own depth.
    const ring = new RecentBashRing();
    use(ring, "go build ./...", "toolu_01");
    use(ring, "go build ./...", "toolu_01");
    use(ring, "go test ./...", "toolu_02");

    expect(ring.snapshot()).toEqual([{ cmd: "go build ./..." }, { cmd: "go test ./..." }]);
    expect(ring.size).toBe(2);
  });

  it("binds each result to its own command, not to whatever ran last", () => {
    // A tool_result arrives in a later user message and can land after a newer
    // command has already started. Single-slot correlation dropped those; the
    // ring must attribute them correctly rather than to the newest entry.
    const ring = new RecentBashRing();
    use(ring, "slow-suite", "t1");
    use(ring, "quick-lint", "t2");
    ring.observeToolResult("t2", false);
    ring.observeToolResult("t1", true);

    expect(ring.snapshot()).toEqual([
      { cmd: "slow-suite", exit: 1 },
      { cmd: "quick-lint", exit: 0 },
    ]);
  });

  it("leaves exit ABSENT — not 0 — when the result never landed", () => {
    // The absent-vs-zero distinction is why Go stores this as *int. Booking a
    // 0 here would report a command that succeeded when the stage was in fact
    // killed while it was still running.
    const ring = new RecentBashRing();
    use(ring, "flutter test integration_test/", "t1");

    const [entry] = ring.snapshot();
    expect(entry).toEqual({ cmd: "flutter test integration_test/" });
    expect(Object.prototype.hasOwnProperty.call(entry ?? {}, "exit")).toBe(false);
    expect(ring.last()?.exit).toBeUndefined();
  });

  it("ignores non-Bash tools and Bash calls carrying no command", () => {
    const ring = new RecentBashRing();
    ring.observeToolUse("Read", { command: "rm -rf /" }, "t1");
    ring.observeToolUse("Bash", {}, "t2");
    ring.observeToolUse("Bash", { command: 42 }, "t3");

    expect(ring.size).toBe(0);
    expect(ring.last()).toBeUndefined();
    // size 0 is what drives the #147 forensic-capture-gap warning, so a ring
    // that quietly stored junk would suppress it.
    expect(ring.snapshot()).toEqual([]);
  });

  it("truncates each entry, so ten commands stay a few KB", () => {
    const ring = new RecentBashRing();
    use(ring, "echo " + "x".repeat(LAST_BASH_COMMAND_MAX_CHARS * 2), "t1");

    const cmd = ring.snapshot()[0]?.cmd ?? "";
    expect(cmd).toHaveLength(LAST_BASH_COMMAND_MAX_CHARS + 1); // + the ellipsis
    expect(cmd.endsWith("…")).toBe(true);
  });

  it("still records a command when the stream supplied no tool_use id", () => {
    // Without an id the exit can never be correlated, but the command itself is
    // the primary anchor — dropping it would be a regression on #147.
    const ring = new RecentBashRing();
    use(ring, "gh pr merge --squash --admin");
    use(ring, "git push");

    expect(ring.snapshot()).toEqual([{ cmd: "gh pr merge --squash --admin" }, { cmd: "git push" }]);
  });

  it("does not alias its internal entries to callers", () => {
    const ring = new RecentBashRing();
    use(ring, "go test ./...", "t1");

    const snap = ring.snapshot();
    snap[0]!.cmd = "mutated";
    expect(ring.snapshot()[0]?.cmd).toBe("go test ./...");
  });
});

/**
 * The ring records entries unconditionally but indexes them for correlation
 * only when the stream supplied a string tool_use id — `tokenParser` drops
 * non-string ids by design. A stage in which EVERY retained tool_use arrived
 * id-less therefore reports `size > 0` (so the #147 gap warning is suppressed)
 * while nothing in the record is reachable by a `tool_result`, and every exit
 * code is dropped by `omitempty`. The record reads "ran ten commands, none of
 * which are known to have failed" — the #147 symptom one level up.
 *
 * Two quantities, deliberately distinct:
 *
 * - `retainedIndexedCount` — of the entries the exit record will actually
 *   carry, how many could ever bind an exit code. This is what the check
 *   asks, because it is a property of the record being written.
 * - `correlatedExits` / `capturedTotal` — lifetime tallies, reported as
 *   supporting data. Neither can decide the question: correlation races the
 *   kill, and a lifetime count says nothing about the ten retained rows.
 *
 * (#302)
 */
describe("RecentBashRing — indexed-ness and the lifetime tallies (#302)", () => {
  const use = (ring: RecentBashRing, command: string, id?: string) =>
    ring.observeToolUse("Bash", { command }, id);

  it("counts NO retained entry as indexed when every tool_use was id-less", () => {
    const ring = new RecentBashRing();
    use(ring, "npm run -w nightgauge-vscode vitest run");
    use(ring, "go test ./...");

    expect(ring.size).toBe(2);
    expect(ring.retainedIndexedCount).toBe(0);
  });

  it("counts an entry as indexed the moment its id is written, before any result", () => {
    // Indexed-ness is about reachability, not arrival: an entry with an id is
    // one a tool_result CAN find, whether or not the stage lived long enough
    // for it to. Conflating the two is what misdiagnoses a mid-command kill.
    const ring = new RecentBashRing();
    use(ring, "flutter test integration_test/", "t1");

    expect(ring.retainedIndexedCount).toBe(1);
    expect(ring.correlatedExits).toBe(0);
  });

  it("tracks indexed-ness per entry in a mixed stage", () => {
    const ring = new RecentBashRing();
    use(ring, "go build ./...", "t1");
    use(ring, "go vet ./...");
    use(ring, "go test ./...", "t3");

    expect(ring.size).toBe(3);
    expect(ring.retainedIndexedCount).toBe(2);
  });

  it("drops indexed-ness with the entry it belonged to, because the window is the record", () => {
    // The partial-drift shape in miniature: the one good entry evicts, and
    // what remains — which is what gets written — is entirely unreachable.
    const ring = new RecentBashRing();
    use(ring, "first", "t0");
    ring.observeToolResult("t0", false);
    for (let i = 1; i <= RECENT_BASH_MAX_ENTRIES; i++) use(ring, `cmd-${i}`);

    expect(ring.size).toBe(RECENT_BASH_MAX_ENTRIES);
    expect(ring.retainedIndexedCount).toBe(0);
    // The lifetime tally still remembers that correlation once worked …
    expect(ring.correlatedExits).toBe(1);
    // … which is exactly why it cannot be the predicate.
  });

  it("counts every command it ever saw, not just the retained window", () => {
    const ring = new RecentBashRing();
    for (let i = 0; i < RECENT_BASH_MAX_ENTRIES * 3; i++) use(ring, `cmd-${i}`, `t${i}`);

    expect(ring.size).toBe(RECENT_BASH_MAX_ENTRIES);
    expect(ring.capturedTotal).toBe(RECENT_BASH_MAX_ENTRIES * 3);
  });

  it("does not count a rejected or duplicate observation toward the lifetime total", () => {
    const ring = new RecentBashRing();
    ring.observeToolUse("Read", { command: "rm -rf /" }, "t1");
    ring.observeToolUse("Bash", {}, "t2");
    use(ring, "go build ./...", "t3");
    use(ring, "go build ./...", "t3"); // same tool_use, second delivery shape

    expect(ring.capturedTotal).toBe(1);
  });

  it("never leaks indexed-ness into the serialised record", () => {
    // `recent_bash` crosses IPC into the persisted run record; a stray
    // diagnostic field would be schema drift.
    const ring = new RecentBashRing();
    use(ring, "go build ./...", "t1");
    ring.observeToolResult("t1", false);
    use(ring, "go vet ./...");

    expect(ring.snapshot()).toEqual([{ cmd: "go build ./...", exit: 0 }, { cmd: "go vet ./..." }]);
    for (const entry of ring.snapshot()) {
      expect(Object.prototype.hasOwnProperty.call(entry, "indexed")).toBe(false);
    }
  });

  it("reports ZERO correlated exits for a stage whose tool_uses were all id-less", () => {
    const ring = new RecentBashRing();
    use(ring, "npm run -w nightgauge-vscode vitest run");
    use(ring, "go test ./...");
    // The results still arrive — they just have nothing to bind to.
    ring.observeToolResult("toolu_01", true);
    ring.observeToolResult("toolu_02", false);

    // Commands captured (so `size` cannot report the gap) …
    expect(ring.size).toBe(2);
    // … and not one exit bound to any of them.
    expect(ring.correlatedExits).toBe(0);
    expect(ring.snapshot().every((e) => e.exit === undefined)).toBe(true);
  });

  it("counts each result that joins its command", () => {
    const ring = new RecentBashRing();
    use(ring, "go build ./...", "t1");
    use(ring, "go test ./...", "t2");
    ring.observeToolResult("t1", false);
    expect(ring.correlatedExits).toBe(1);
    ring.observeToolResult("t2", true);
    expect(ring.correlatedExits).toBe(2);
  });

  it("does not count a result for a command it never retained", () => {
    const ring = new RecentBashRing();
    use(ring, "go build ./...", "t1");
    ring.observeToolResult("t-unknown", true);

    expect(ring.correlatedExits).toBe(0);
  });

  it("counts the join once when the same result is delivered twice", () => {
    // A repeat delivery is not a second correlated exit; counting it would let
    // one chatty result mask a stage where nothing else correlated at all.
    const ring = new RecentBashRing();
    use(ring, "gh pr checks", "t1");
    ring.observeToolResult("t1", false);
    ring.observeToolResult("t1", false);

    expect(ring.correlatedExits).toBe(1);
  });

  it("keeps the tally across eviction — it answers 'did correlation ever work'", () => {
    const ring = new RecentBashRing();
    use(ring, "first", "t0");
    ring.observeToolResult("t0", false);
    for (let i = 1; i <= RECENT_BASH_MAX_ENTRIES; i++) use(ring, `cmd-${i}`, `t${i}`);

    // `first` is long gone from the window; the tally still reports that
    // correlation worked at some point in this stage. That is all it claims —
    // the warning quotes it as supporting data and decides on the retained
    // window instead, because "it worked once" says nothing about the ten rows
    // the record is about to carry.
    expect(ring.size).toBe(RECENT_BASH_MAX_ENTRIES);
    expect(ring.correlatedExits).toBe(1);
  });
});

/**
 * The self-check itself. It runs inside `runStageSkillHeadless`, which spawns a
 * subprocess and cannot be exercised cheaply — the same constraint that put
 * `extractBashCommand` and `RecentBashRing` out here in #147/#156. An
 * untestable detector is how the original gap survived 2,533 records.
 *
 * Arm 2's predicate is "none of the RETAINED entries is indexed", not "the
 * lifetime correlated count is zero". The four shapes below are why; each is
 * one of them, and two of the four are cases the lifetime predicate gets
 * wrong in opposite directions.
 */
describe("describeForensicCaptureGap — the stage's self-check (#147/#302)", () => {
  const use = (ring: RecentBashRing, command: string, id?: string) =>
    ring.observeToolUse("Bash", { command }, id);

  const check = (ring: RecentBashRing, parsedToolEventCount: number) =>
    describeForensicCaptureGap({
      stage: "feature-validate",
      parsedToolEventCount,
      retainedCommands: ring.size,
      retainedIndexedCommands: ring.retainedIndexedCount,
      capturedTotal: ring.capturedTotal,
      correlatedExits: ring.correlatedExits,
    });

  it("says nothing when the stage parsed no tool events at all", () => {
    // A stage that genuinely ran nothing is a different (already-reported)
    // condition, not a capture gap.
    expect(check(new RecentBashRing(), 0)).toBeUndefined();
  });

  it("warns when tool events were parsed but no Bash command was captured (#147)", () => {
    const warning = check(new RecentBashRing(), 214);

    expect(warning).toBeDefined();
    expect(warning).toContain("[forensic-capture-gap]");
    expect(warning).toContain("feature-validate");
    expect(warning).toContain("parsed 214 tool event(s)");
    expect(warning).toContain("(Issue #147)");
  });

  // ── Shape 1: the all-id-less stage — the record #302 exists to catch ──
  it("warns when NONE of the retained commands carried a usable id (#302)", () => {
    // `size > 0` suppresses the #147 arm, so without a second arm this stage
    // reports nothing and its exit record — populated `recent_bash`, every
    // exit absent — reads as healthy and terse.
    const ring = new RecentBashRing();
    use(ring, "npm run -w nightgauge-vscode vitest run");
    use(ring, "go test ./...");
    // The results still arrive; they have nothing to bind to.
    ring.observeToolResult("toolu_01", true);

    const warning = check(ring, 214);

    expect(warning).toBeDefined();
    expect(warning).toContain("[forensic-capture-gap]");
    expect(warning).toContain("feature-validate");
    // Composed fragments, not bare digits: `toContain("2")` passed on the "2"
    // inside "214" and would have survived the count being wrong entirely.
    expect(warning).toContain("captured 2 Bash command(s)");
    expect(warning).toContain("parsed 214 tool event(s)");
    expect(warning).toContain("(0 exit(s) correlated over the stage)");
    expect(warning).toContain("NONE of the 2 command(s) retained");
    expect(warning).toContain("(Issue #302)");
  });

  // ── Shape 2: killed mid-command, id present — NOT a capture gap ──
  it("says nothing when the only command had a good id and the stage died first", () => {
    // Zero correlated exits, and nothing wrong: the stage was killed while its
    // first-and-only Bash command was still running. The lifetime-correlation
    // predicate fires here and blames the parser — a confident wrong diagnosis
    // that sends a retro looking for shape drift that does not exist.
    const ring = new RecentBashRing();
    use(ring, "flutter test integration_test/app_e2e/scoring_test.dart", "toolu_01");

    expect(ring.correlatedExits).toBe(0);
    expect(check(ring, 61)).toBeUndefined();
  });

  // ── Shape 3: partial drift — correlation worked once, then stopped ──
  it("warns when the one correlated command evicted and the retained ten are id-less", () => {
    // Correlation demonstrably worked (lifetime tally 1), so the
    // lifetime-correlation predicate stays SILENT — while the record actually
    // being written is ten commands with no exit codes and no way to have had
    // any. This is the "healthy and terse" record, and it is the shape the
    // check is for.
    const ring = new RecentBashRing();
    use(ring, "npm ci", "toolu_00");
    ring.observeToolResult("toolu_00", false);
    for (let i = 1; i <= RECENT_BASH_MAX_ENTRIES; i++) use(ring, `drifted-cmd-${i}`);

    expect(ring.correlatedExits).toBe(1); // the shape that used to suppress it
    const warning = check(ring, 133);

    expect(warning).toBeDefined();
    expect(warning).toContain("(Issue #302)");
    expect(warning).toContain(`NONE of the ${RECENT_BASH_MAX_ENTRIES} command(s) retained`);
    // The lifetime figures are reported as supporting data, and are honestly
    // labelled — they describe the stage, not the retained window.
    expect(warning).toContain(`captured ${RECENT_BASH_MAX_ENTRIES + 1} Bash command(s)`);
    expect(warning).toContain("(1 exit(s) correlated over the stage)");
  });

  // ── Shape 4: healthy / partially correlated — one indexed entry is enough ──
  it("says nothing while any retained command could still bind an exit", () => {
    // Partial correlation is normal: the stage can be killed with a command
    // still in flight. One indexed entry proves the shapes still agree.
    const ring = new RecentBashRing();
    use(ring, "go build ./...", "t1");
    ring.observeToolResult("t1", false);
    use(ring, "go test ./...", "t2"); // still running at exit

    expect(check(ring, 87)).toBeUndefined();
  });

  it("says nothing on a fully healthy stage", () => {
    const ring = new RecentBashRing();
    use(ring, "go build ./...", "t1");
    ring.observeToolResult("t1", false);

    expect(check(ring, 12)).toBeUndefined();
  });

  it("says nothing when a single id-less command sits among indexed ones", () => {
    // Occasional id-less events are not a divergence; the record still carries
    // exit codes. Only a wholly unreachable window is.
    const ring = new RecentBashRing();
    use(ring, "go build ./...", "t1");
    use(ring, "echo interstitial");
    ring.observeToolResult("t1", false);

    expect(check(ring, 40)).toBeUndefined();
  });
});
