/**
 * Parity test: Go/SDK terminal-kind taxonomy (Issue #229)
 *
 * `internal/orchestrator/failure_handler.go` declares the `TerminalKind*`
 * constants and `packages/nightgauge-sdk/src/analysis/health/failureClassifier.ts`
 * declares the mirrored `TerminalFailureKind` union (also mirrored into the
 * Zod `TerminalFailureKindSchema` in
 * `packages/nightgauge-vscode/src/schemas/executionHistory.ts`). These three
 * places have drifted independently before (#229) — this test reads Go's
 * source directly and diffs its constant values against the SDK's runtime
 * `ALL_TERMINAL_FAILURE_KINDS` array so the next kind added to one side and
 * forgotten on the other fails a test instead of drifting silently.
 */

import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { ALL_TERMINAL_FAILURE_KINDS } from "../../../src/analysis/health/failureClassifier.js";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(__dirname, "../../../../..");
const GO_FAILURE_HANDLER_PATH = path.join(REPO_ROOT, "internal/orchestrator/failure_handler.go");

/**
 * Go declares `TerminalKind<Name> = "<value>"` for every terminal kind
 * constant. Extract every value via regex rather than parsing Go — a
 * regex diff is the agreed-minimum guarantee (see the #229 plan) without a
 * new Go->TS codegen step.
 */
function extractGoTerminalKinds(source: string): Set<string> {
  const values = new Set<string>();
  const pattern = /TerminalKind\w+\s*=\s*"([a-z_]+)"/g;
  let match: RegExpExecArray | null;
  while ((match = pattern.exec(source)) !== null) {
    values.add(match[1]);
  }
  return values;
}

describe("Go/SDK terminal-kind parity (#229)", () => {
  const goSource = readFileSync(GO_FAILURE_HANDLER_PATH, "utf-8");
  const goKinds = extractGoTerminalKinds(goSource);
  const sdkKinds = new Set(ALL_TERMINAL_FAILURE_KINDS);

  it("extracted a non-trivial set of Go terminal kinds (sanity check on the regex)", () => {
    expect(goKinds.size).toBeGreaterThan(20);
  });

  it("every Go TerminalKind constant has a matching SDK union member", () => {
    const missingFromSdk = [...goKinds].filter((k) => !sdkKinds.has(k as never)).sort();
    expect(missingFromSdk).toEqual([]);
  });

  it("every SDK TerminalFailureKind member has a matching Go constant", () => {
    const missingFromGo = [...sdkKinds].filter((k) => !goKinds.has(k)).sort();
    expect(missingFromGo).toEqual([]);
  });

  it("has no duplicate entries in the SDK's enumerable kind list", () => {
    expect(ALL_TERMINAL_FAILURE_KINDS.length).toBe(sdkKinds.size);
  });
});
