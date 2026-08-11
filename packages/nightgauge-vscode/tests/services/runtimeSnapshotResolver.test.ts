/**
 * runtimeSnapshotResolver.test.ts
 *
 * The TypeScript half of ADR-017 Decision 8 (#370): resolving `which snapshot
 * is issue #N's run?` from `runtime-{issue}-{runId}.json` files.
 *
 * WHY THIS FILE EXISTS. The resolver's only caller converts a null into `{}`,
 * and every caller of THAT falls through to a legacy heuristic — so a resolver
 * that returns null on every call, or the wrong sibling on every call, produces
 * no exception, no log, and a full green suite. What the operator sees instead
 * is a false `[gate-not-invoked]` line for gates that all ran, pointing at the
 * gates rather than at the reader. The function shipped with zero tests; this
 * is the red bar.
 *
 * FIXTURE PROVENANCE — every snapshot body below is VERBATIM OUTPUT of the Go
 * production writer, not hand-authored JSON. They were produced by running
 * `state.NewRuntimeState(...).AppendStageGateResult(...).Persist(dir)` inside
 * `internal/state` (the producer is archived alongside the step-1 evidence
 * file) and copying the resulting bytes. That is what makes the camelCase key
 * names, the `terminal`-absent-when-false shape, and — critically for the DST
 * case — the RFC3339-WITH-OFFSET `startedAt` encoding real rather than assumed.
 *
 * @see docs/decisions/017-runtime-identity-keying.md
 */

import { describe, it, expect, beforeEach, afterEach } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import {
  resolveRuntimeSnapshotPath,
  isSnapshotName,
} from "../../src/utils/runtimeSnapshotResolver";

/** Go `state.Persist` output, verbatim. See the provenance note above. */
const GO_SNAPSHOTS: Record<string, string> = {
  // #700 — one live run.
  "runtime-700-019fe6f0-14da-7470-93cf-4dfc9e88e1e8.json":
    '{"repo":"acme/app","issueNumber":700,"itemId":"item","runId":"019fe6f0-14da-7470-93cf-4dfc9e88e1e8","stage":"","startedAt":"2026-08-09T10:00:00Z","stageStart":"0001-01-01T00:00:00Z","inputTokens":0,"outputTokens":0,"totalCostUsd":0,"completedStages":[],"skippedStages":[],"phaseHistory":[],"stageErrors":{},"stageGateResults":{"feature-dev":[{"gate_name":"single-live","passed":true,"reason":"","timestamp":""}]}}',

  // #701 — a live OLDER run and a terminal NEWER one. Note the terminal file is
  // the only one carrying `"terminal":true`: Go omits the key when false, which
  // is why the reader tests `blob?.terminal === true` rather than truthiness.
  "runtime-701-019fe6f0-14e0-7aaa-8111-000000000001.json":
    '{"repo":"acme/app","issueNumber":701,"itemId":"item","runId":"019fe6f0-14e0-7aaa-8111-000000000001","stage":"","startedAt":"2026-08-09T09:00:00Z","stageStart":"0001-01-01T00:00:00Z","inputTokens":0,"outputTokens":0,"totalCostUsd":0,"completedStages":[],"skippedStages":[],"phaseHistory":[],"stageErrors":{},"stageGateResults":{"feature-dev":[{"gate_name":"live-older","passed":true,"reason":"","timestamp":""}]}}',
  "runtime-701-019fe6f0-14e1-7bbb-9222-000000000002.json":
    '{"repo":"acme/app","issueNumber":701,"itemId":"item","runId":"019fe6f0-14e1-7bbb-9222-000000000002","terminal":true,"terminalAt":"2026-08-09T15:30:55.686717Z","terminalOutcome":"complete","stage":"","startedAt":"2026-08-09T12:00:00Z","stageStart":"0001-01-01T00:00:00Z","inputTokens":0,"outputTokens":0,"totalCostUsd":0,"completedStages":[],"skippedStages":[],"phaseHistory":[],"stageErrors":{},"stageGateResults":{"feature-dev":[{"gate_name":"terminal-newer","passed":true,"reason":"","timestamp":""}]}}',

  // #702 — two live runs, the re-run steady state.
  "runtime-702-019fe6f0-14e2-7ccc-a333-000000000003.json":
    '{"repo":"acme/app","issueNumber":702,"itemId":"item","runId":"019fe6f0-14e2-7ccc-a333-000000000003","stage":"","startedAt":"2026-08-09T09:00:00Z","stageStart":"0001-01-01T00:00:00Z","inputTokens":0,"outputTokens":0,"totalCostUsd":0,"completedStages":[],"skippedStages":[],"phaseHistory":[],"stageErrors":{},"stageGateResults":{"feature-dev":[{"gate_name":"live-older","passed":true,"reason":"","timestamp":""}]}}',
  "runtime-702-019fe6f0-14e3-7ddd-b444-000000000004.json":
    '{"repo":"acme/app","issueNumber":702,"itemId":"item","runId":"019fe6f0-14e3-7ddd-b444-000000000004","stage":"","startedAt":"2026-08-09T11:00:00Z","stageStart":"0001-01-01T00:00:00Z","inputTokens":0,"outputTokens":0,"totalCostUsd":0,"completedStages":[],"skippedStages":[],"phaseHistory":[],"stageErrors":{},"stageGateResults":{"feature-dev":[{"gate_name":"live-newer","passed":true,"reason":"","timestamp":""}]}}',

  // #703 — the DST fall-back pair. `01:30:00-04:00` is 05:30Z; `01:15:00-05:00`
  // is 06:15Z, i.e. 45 minutes LATER. As strings the order inverts, because
  // "01:30" > "01:15". This is exactly the population `localeCompare` got wrong.
  "runtime-703-019fe6f0-14e7-7eee-8555-000000000005.json":
    '{"repo":"acme/app","issueNumber":703,"itemId":"item","runId":"019fe6f0-14e7-7eee-8555-000000000005","stage":"","startedAt":"2026-11-01T01:30:00-04:00","stageStart":"0001-01-01T00:00:00Z","inputTokens":0,"outputTokens":0,"totalCostUsd":0,"completedStages":[],"skippedStages":[],"phaseHistory":[],"stageErrors":{},"stageGateResults":{"feature-dev":[{"gate_name":"dst-older-instant","passed":true,"reason":"","timestamp":""}]}}',
  "runtime-703-019fe6f0-14f4-750b-9666-000000000006.json":
    '{"repo":"acme/app","issueNumber":703,"itemId":"item","runId":"019fe6f0-14f4-750b-9666-000000000006","stage":"","startedAt":"2026-11-01T01:15:00-05:00","stageStart":"0001-01-01T00:00:00Z","inputTokens":0,"outputTokens":0,"totalCostUsd":0,"completedStages":[],"skippedStages":[],"phaseHistory":[],"stageErrors":{},"stageGateResults":{"feature-dev":[{"gate_name":"dst-newer-instant","passed":true,"reason":"","timestamp":""}]}}',
};

let dir: string;

function seed(...names: string[]): void {
  for (const name of names) {
    const body = GO_SNAPSHOTS[name];
    if (!body) throw new Error(`no Go-written fixture named ${name}`);
    fs.writeFileSync(path.join(dir, name), body, "utf-8");
  }
}

function gateNameOf(resolved: string | null): string | null {
  if (!resolved) return null;
  const blob = JSON.parse(fs.readFileSync(resolved, "utf-8"));
  return blob.stageGateResults["feature-dev"][0].gate_name;
}

beforeEach(() => {
  dir = fs.mkdtempSync(path.join(os.tmpdir(), "ng-snapshot-resolver-"));
});

afterEach(() => {
  fs.rmSync(dir, { recursive: true, force: true });
});

describe("resolveRuntimeSnapshotPath", () => {
  it("finds the single live snapshot for an issue", () => {
    seed("runtime-700-019fe6f0-14da-7470-93cf-4dfc9e88e1e8.json");
    expect(gateNameOf(resolveRuntimeSnapshotPath(dir, 700))).toBe("single-live");
  });

  it("prefers a LIVE snapshot over a NEWER terminal sibling", () => {
    // The Go picker's first tier. If this ever silently loses, `nightgauge`
    // answers "what is #N doing?" from a finished run while a live dispatch is
    // mid-flight — the wrong-run-pick class ADR-017 exists to close.
    seed(
      "runtime-701-019fe6f0-14e0-7aaa-8111-000000000001.json",
      "runtime-701-019fe6f0-14e1-7bbb-9222-000000000002.json"
    );
    expect(gateNameOf(resolveRuntimeSnapshotPath(dir, 701))).toBe("live-older");
  });

  it("picks the newest of two live snapshots", () => {
    seed(
      "runtime-702-019fe6f0-14e2-7ccc-a333-000000000003.json",
      "runtime-702-019fe6f0-14e3-7ddd-b444-000000000004.json"
    );
    expect(gateNameOf(resolveRuntimeSnapshotPath(dir, 702))).toBe("live-newer");
  });

  it("orders by INSTANT, not by string, across a zone-offset change", () => {
    // The MF5 regression pin. Both stamps are real Go output; sorted as text
    // the OLDER instant wins, which is what the shipped `localeCompare` did —
    // and the Go reader answering the same question about the same directory
    // returned the other run, so the two halves of the pipeline disagreed about
    // which run #703 is.
    seed(
      "runtime-703-019fe6f0-14e7-7eee-8555-000000000005.json",
      "runtime-703-019fe6f0-14f4-750b-9666-000000000006.json"
    );
    expect(gateNameOf(resolveRuntimeSnapshotPath(dir, 703))).toBe("dst-newer-instant");

    // And the trap itself, stated so the fixture cannot silently stop being
    // adversarial: string order and instant order genuinely disagree here.
    const older = "2026-11-01T01:30:00-04:00";
    const newer = "2026-11-01T01:15:00-05:00";
    expect(newer.localeCompare(older)).toBeLessThan(0); // string: "newer" sorts first-as-older
    expect(Date.parse(newer)).toBeGreaterThan(Date.parse(older)); // instant: it is genuinely newer
  });

  it("returns null when the issue has no snapshot, and when the dir is absent", () => {
    seed("runtime-700-019fe6f0-14da-7470-93cf-4dfc9e88e1e8.json");
    expect(resolveRuntimeSnapshotPath(dir, 999)).toBeNull();
    expect(resolveRuntimeSnapshotPath(path.join(dir, "nope"), 700)).toBeNull();
  });

  it("skips a corrupt sibling rather than failing the whole scan", () => {
    // A file caught mid-atomic-rename, or truncated. Its siblings must still
    // answer.
    seed("runtime-700-019fe6f0-14da-7470-93cf-4dfc9e88e1e8.json");
    fs.writeFileSync(
      path.join(dir, "runtime-700-019fe6f0-1111-7000-8000-000000000000.json"),
      "{not json",
      "utf-8"
    );
    expect(gateNameOf(resolveRuntimeSnapshotPath(dir, 700))).toBe("single-live");
  });

  it("ignores another issue's snapshots and the legacy name", () => {
    seed(
      "runtime-700-019fe6f0-14da-7470-93cf-4dfc9e88e1e8.json",
      "runtime-702-019fe6f0-14e3-7ddd-b444-000000000004.json"
    );
    // The legacy scheme is deliberately NOT a candidate: a file that cannot
    // name its run is not a run this tree can reason about.
    fs.writeFileSync(path.join(dir, "runtime-700.json"), '{"issueNumber":700}', "utf-8");
    expect(gateNameOf(resolveRuntimeSnapshotPath(dir, 700))).toBe("single-live");
  });

  it("reports the mixed-version window instead of going silently dark", () => {
    // Old `serve` daemon (pre-step-1 snapshot name) under a new bundle: zero
    // new-scheme candidates, a legacy file present. Without the callback this
    // is a null the caller turns into `{}` with no diagnostic anywhere.
    fs.writeFileSync(path.join(dir, "runtime-704.json"), '{"issueNumber":704}', "utf-8");
    const seen: string[] = [];
    expect(resolveRuntimeSnapshotPath(dir, 704, (f) => seen.push(f))).toBeNull();
    expect(seen).toEqual(["runtime-704.json"]);

    // Not fired when a new-scheme snapshot exists — the window is closed then.
    seed("runtime-700-019fe6f0-14da-7470-93cf-4dfc9e88e1e8.json");
    const seen2: string[] = [];
    expect(resolveRuntimeSnapshotPath(dir, 700, (f) => seen2.push(f))).not.toBeNull();
    expect(seen2).toEqual([]);
  });

  // THE NAME PATTERN'S OWN REFUSAL TABLE. Every case above reaches the pattern
  // only through `fs.readdirSync` output, and each one only ever asserts which
  // snapshot WINS — so four widening mutations of the pattern (drop `^`, drop
  // `$`, re-transcribe an any-version/any-variant identity, widen by
  // alternation) survived this whole file green. The sibling ANY_RUNTIME_FILE in
  // runtimeStubSweep kills all four because it has a refusal table; this is that
  // table, over the seam `isSnapshotName` exposes.
  it("snapshot name pattern — anchored, and the VALIDATOR's identity shape", () => {
    const live = "runtime-700-019fe6f0-14da-7470-93cf-4dfc9e88e1e8.json";
    expect(isSnapshotName(700, live)).toBe(true);
    for (const name of [
      `${live}.tmp`, // mid-atomic-rename: needs the `$`. Reading a half-written
      // file as a candidate is how a torn snapshot becomes the answer.
      `stale-${live}`, // prefixed: needs the `^`
      "runtime-700-3f2504e0-4f89-41d3-9a0c-0305e82c3301.json", // UUIDv4 — the
      // fragment must be the VALIDATOR's shape, not "any UUID". An id this
      // pattern finds but isRunIdentity refuses is the phantom snapshot.
      "runtime-700-019FE6F0-14DA-7470-93CF-4DFC9E88E1E8.json", // uppercase
      "runtime-701-019fe6f0-14da-7470-93cf-4dfc9e88e1e8.json", // another issue
    ])
      expect(isSnapshotName(700, name), name).toBe(false);
  });
});
