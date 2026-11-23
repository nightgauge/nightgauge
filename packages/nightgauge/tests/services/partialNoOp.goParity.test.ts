/**
 * #3487 step 3: the extension books a partial-delivery no-op with the same
 * values the Go path does. Read from the Go sources, so a rename on either side
 * fails here instead of drifting.
 */

import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";
import { GATE_KIND_PARTIAL_NO_OP } from "../../src/services/HeadlessOrchestrator";
import {
  NO_OP_CLOSE_OPTION_ID,
  NO_OP_CLOSE_VERB,
  PARTIAL_NO_OP_TERMINAL_KIND,
  slotTerminalKind,
} from "../../src/services/ConcurrentPipelineManager";

const repoRoot = path.resolve(__dirname, "../../../..");
const goSource = (rel: string) => readFileSync(path.join(repoRoot, rel), "utf-8");

/** The string a Go constant declaration assigns, or a failure naming it. */
function goConst(rel: string, name: string): string {
  const match = new RegExp(`\\b${name}\\s+(?:[A-Za-z.]+\\s+)?=\\s+"([^"]*)"`).exec(goSource(rel));
  if (!match) throw new Error(`${rel} declares no string constant ${name}`);
  return match[1];
}

describe("partial-delivery no-op: Go/TypeScript parity (#3487)", () => {
  it("books the same terminal kind", () => {
    const go = goConst(
      "internal/orchestrator/failure_handler.go",
      "TerminalKindNotPipelineActionable"
    );
    expect(PARTIAL_NO_OP_TERMINAL_KIND).toBe(go);
    expect(slotTerminalKind({ partialNoOp: {} })).toBe(go);
  });

  it("writes the same exit-record gate kind", () => {
    expect(GATE_KIND_PARTIAL_NO_OP).toBe(
      goConst("internal/diagnostics/exit_record.go", "GateKindPartialNoOp")
    );
  });

  it("wakes on the close verb the no-op card offers", () => {
    expect(NO_OP_CLOSE_OPTION_ID).toBe(
      goConst("internal/orchestrator/partial_noop.go", "PartialNoOpActionClose")
    );
    expect(NO_OP_CLOSE_VERB).toBe(
      goConst("internal/attention/verbs.go", "VerbIssueCloseRemainder")
    );
    // The card's option pairs that id with that verb.
    expect(goSource("internal/orchestrator/attention_wiring.go")).toMatch(
      /ID: "close", Label: closeRemainderLabel, Verb: attention\.VerbIssueCloseRemainder/
    );
  });
});
