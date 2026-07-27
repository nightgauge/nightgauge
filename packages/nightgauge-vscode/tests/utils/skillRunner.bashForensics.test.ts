import { describe, expect, it } from "vitest";

import { LAST_BASH_COMMAND_MAX_CHARS, extractBashCommand } from "../../src/utils/skillRunner";

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
