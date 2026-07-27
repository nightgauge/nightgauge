import { describe, expect, it } from "vitest";

import {
  LAST_BASH_COMMAND_MAX_CHARS,
  RECENT_BASH_MAX_ENTRIES,
  RecentBashRing,
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
