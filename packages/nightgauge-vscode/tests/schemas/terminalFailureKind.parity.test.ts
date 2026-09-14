/**
 * Parity test: Go terminal kinds ↔ the extension's TerminalFailureKindSchema.
 *
 * The Go scheduler writes `terminal_failure_kind` into the V3 run record from
 * the `TerminalKind*` constants in internal/orchestrator/failure_handler.go.
 * A value this Zod enum does not list fails the V3 schema, so the record
 * falls through AnyRunRecordSchema to V2 and loses its kind without an error.
 * The SDK union has its own parity test (failureClassifier.parity.test.ts);
 * this one reads the same Go source and holds the extension's enum to it, in
 * both directions, so deleting one enum entry, or adding a Go kind without
 * one, fails here (#1631).
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";
import { TerminalFailureKindSchema } from "../../src/schemas/executionHistory";

const REPO_ROOT = path.resolve(__dirname, "../../../..");
const GO_FAILURE_HANDLER = path.join(REPO_ROOT, "internal/orchestrator/failure_handler.go");

function goTerminalKinds(): Set<string> {
  const source = readFileSync(GO_FAILURE_HANDLER, "utf-8");
  const kinds = new Set<string>();
  for (const match of source.matchAll(/TerminalKind\w+\s*=\s*"([a-z_]+)"/g)) {
    kinds.add(match[1]);
  }
  return kinds;
}

describe("TerminalFailureKindSchema mirrors the Go terminal kinds (#1631)", () => {
  const goKinds = goTerminalKinds();
  const schemaKinds = new Set<string>(TerminalFailureKindSchema.options);

  it("reads a non-trivial set of Go kinds (a sanity check on the regex)", () => {
    expect(goKinds.size).toBeGreaterThan(40);
  });

  it("accepts every Go terminal kind", () => {
    const missing = [...goKinds].filter((k) => !schemaKinds.has(k)).sort();
    expect(missing).toEqual([]);
    for (const kind of goKinds) {
      expect(TerminalFailureKindSchema.safeParse(kind).success).toBe(true);
    }
  });

  it("lists no kind Go does not declare", () => {
    const extra = [...schemaKinds].filter((k) => !goKinds.has(k)).sort();
    expect(extra).toEqual([]);
  });

  it("accepts the parked kinds a local-model run records", () => {
    for (const kind of [
      "context_window_exceeded",
      "adapter_permission_rejected",
      "adapter_incompatible",
    ]) {
      expect(TerminalFailureKindSchema.safeParse(kind).success).toBe(true);
    }
  });
});
