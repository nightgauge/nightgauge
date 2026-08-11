/**
 * runIdentityShapeGuard.test.ts (#424 review) — the module-load guard that makes
 * a missing `RUN_IDENTITY_SHAPE` loud.
 *
 * Both extension-side embedders build their filename regex by interpolating
 * `RUN_IDENTITY_SHAPE` from `@nightgauge/sdk`. If that value is `undefined` the
 * interpolation does not fail — it inserts the STRING "undefined" — and the
 * resulting pattern matches nothing a pipeline ever wrote. Neither module has a
 * caller that notices: the snapshot resolver's null becomes `{}` and then a
 * legacy heuristic (a false `[gate-not-invoked]` is the operator's only
 * symptom), and an ANY_RUNTIME_FILE that refuses every new-scheme name just
 * means activation silently stops offering live runs for restore. So each module
 * throws at load instead.
 *
 * WHY `undefined` IS THE FAITHFUL FIXTURE. The extension resolves the SDK from
 * `packages/nightgauge-sdk/dist/`, and a dist built before #424 added the export
 * has no such key. Through esbuild's CJS interop a missing named export reads as
 * a plain `undefined` at runtime rather than raising a link error — that is the
 * shape the guard exists for, and `{ RUN_IDENTITY_SHAPE: undefined }` is it.
 *
 * A FULL-REPLACEMENT `vi.mock("@nightgauge/sdk", () => ({}))` — which ten other
 * files in this suite use — deliberately is NOT the fixture: vitest wraps a
 * factory mock in a proxy that throws `[vitest] No "RUN_IDENTITY_SHAPE" export
 * is defined…` on the missing key, so under that mock the module already fails
 * loudly whether or not the guard exists. Asserting on it would be an
 * unexercised guard wearing a green bar (mutation-verified: with the guard
 * deleted, a `/RUN_IDENTITY_SHAPE/` assertion against that mock still passed).
 * The assertions below therefore match text only THIS guard emits.
 *
 * THIS FILE IS DELIBERATELY ITS OWN FILE. `vi.mock` is hoisted per test module,
 * so the crippled SDK and the failed-evaluation registry entries must not be
 * visible to any other suite.
 */
import { describe, it, expect, vi } from "vitest";

vi.mock("@nightgauge/sdk", () => ({ RUN_IDENTITY_SHAPE: undefined }));

/** Text unique to the guard — not to vitest's missing-export proxy. */
const GUARD = /stale or mismatched @nightgauge\/sdk dist/;

describe("the RUN_IDENTITY_SHAPE module-load guard", () => {
  it("runtimeStubSweep refuses to load without the fragment", async () => {
    await expect(import("../../src/utils/runtimeStubSweep")).rejects.toThrow(GUARD);
  });

  it("runtimeSnapshotResolver refuses to load without the fragment", async () => {
    await expect(import("../../src/utils/runtimeSnapshotResolver")).rejects.toThrow(GUARD);
  });
});
