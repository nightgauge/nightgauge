/**
 * runtimeSnapshotResolver — resolve the on-disk runtime snapshot for an issue.
 *
 * ADR-017 Decision 8 (#370) renamed the pipeline's crash-recovery snapshot from
 * `runtime-{issue}.json` to `runtime-{issue}-{runId}.json`. The run identity is
 * NOT derivable from the issue number — concurrent dispatches of one issue
 * coexist — so an issue-addressed reader can no longer compose the filename and
 * must scan the directory instead. This module is the TypeScript half of
 * `internal/state.FindPersistedStatesForIssue` + `PickPersistedStateForIssue`,
 * and it must agree with them: the two halves of the pipeline answering "which
 * run is #N?" differently is the wrong-run-pick class ADR-017 exists to close.
 *
 * It lives in `utils/` rather than inside HeadlessOrchestrator because the
 * failure it guards against is SILENT: every caller of the resolved path falls
 * back to `{}` or `[]` and then to a legacy heuristic, so a resolver that
 * returns null on every call produces a misleading `[gate-not-invoked]`
 * diagnostic and no red test. A pure function over a directory can be pinned by
 * a red bar; a private method reached through nothing cannot.
 *
 * @see docs/decisions/017-runtime-identity-keying.md
 */

import { RUN_IDENTITY_SHAPE } from "@nightgauge/sdk";
import * as fs from "fs";
import * as path from "path";

// LOUD BEATS SILENT. `RUN_IDENTITY_SHAPE` can arrive `undefined` at runtime, and
// not hypothetically: the extension resolves @nightgauge/sdk from `dist/`, and a
// dist built before #424 added the export has no such key — through esbuild's
// CJS interop that is a plain `undefined`, not a link error. Interpolating it
// yields the STRING "undefined" inside the pattern, so this resolver would match
// ZERO snapshots forever — and every caller turns its null into `{}` and then
// into a legacy heuristic, so the operator's only symptom is a false
// `[gate-not-invoked]` (the exact silence this module was split out to end).
// Fail at module load instead, where the message names the cause and the fix.
if (typeof RUN_IDENTITY_SHAPE !== "string" || RUN_IDENTITY_SHAPE.length === 0) {
  throw new Error(
    "runtimeSnapshotResolver: a stale or mismatched @nightgauge/sdk dist did " +
      "not provide RUN_IDENTITY_SHAPE — rebuild it (npm run build -w @nightgauge/sdk)"
  );
}

/** The subset of the snapshot body this resolver reads. */
interface SnapshotHeader {
  terminal?: boolean;
  startedAt?: string;
}

interface Candidate {
  file: string;
  terminal: boolean;
  /** Epoch ms, or NaN when `startedAt` is absent/unparseable. */
  startedAtMs: number;
}

/**
 * The canonical snapshot name for one issue: `runtime-{issue}-{runId}.json`
 * where runId is a canonical lowercase UUIDv7. Mirrors the Go side's
 * `snapshotFilePattern`, itself built from `runstate.IdentityPattern`.
 *
 * The identity fragment is INTERPOLATED from the one TypeScript definition
 * (`RUN_IDENTITY_SHAPE`, @nightgauge/sdk) rather than transcribed here (#424):
 * a discovery regex that drifts from the validator is an id that passes
 * validation and then cannot be found on disk — the phantom-snapshot shape
 * ADR-017 Decision 1 exists to make impossible.
 *
 * The fragment is WRAPPED in `(?:…)` per its documented embedding contract, the
 * same way Go groups `IdentityPattern` at every embed site. A bare
 * interpolation is only accidentally correct: a top-level alternation inside the
 * fragment would re-associate this pattern so `^runtime-N-` and `\.json$` bound
 * different branches, and `<anything>.json.tmp` would read as a snapshot name.
 */
function snapshotNamePattern(issueNumber: number): RegExp {
  return new RegExp(`^runtime-${issueNumber}-(?:${RUN_IDENTITY_SHAPE})\\.json$`);
}

/**
 * True when `fileName` is issue `issueNumber`'s canonical snapshot name.
 *
 * Exported for one reason: {@link snapshotNamePattern} is reached only through
 * `fs.readdirSync` output, so every property of it — the `^`/`$` anchors, and
 * that the identity component is the VALIDATOR's shape rather than any UUID —
 * was previously provable only by staging files on disk, and four widening
 * mutations of the pattern survived the whole resolver suite green. This is the
 * thin seam that lets those be red bars (#424 review). Its sibling
 * `ANY_RUNTIME_FILE` in runtimeStubSweep.ts is exported for the same reason.
 */
export function isSnapshotName(issueNumber: number, fileName: string): boolean {
  return snapshotNamePattern(issueNumber).test(fileName);
}

/** The pre-ADR-017 name, matched ONLY to diagnose the mixed-version window. */
function legacyNamePattern(issueNumber: number): RegExp {
  return new RegExp(`^runtime-${issueNumber}\\.json$`);
}

/**
 * Resolve the snapshot path for `issueNumber` under `pipelineDir`, or null when
 * the issue has none.
 *
 * THE PICK IS THE GO SIDE'S STANDARD PICK: a non-terminal (live) snapshot beats
 * a terminal one even when the terminal one is newer, then newest `startedAt`
 * wins.
 *
 * ORDERING IS BY INSTANT, NOT BY STRING. Go marshals `time.Time` as RFC3339
 * WITH the local zone offset, so `localeCompare` over two stamps that straddle
 * an offset change — the repeated hour at a DST fall-back, or a laptop whose
 * timezone changed between two runs — ranks them by their wall-clock text and
 * picks the OLDER run. `state.FindPersistedStatesForIssue` compares instants
 * (`StartedAt.After`) and is correct; this must too. An absent or unparseable
 * stamp sorts LAST rather than winning by accident.
 *
 * @param onLegacyOnly Invoked when zero new-scheme snapshots exist BUT a legacy
 *   `runtime-{issue}.json` does. That is the mixed-version window — an old
 *   `nightgauge serve` daemon still writing the pre-ADR-017 name under a new
 *   extension bundle — and without this callback the whole window is silent:
 *   the reader returns null, the caller returns `{}`, and the operator sees
 *   only a false `[gate-not-invoked]`. It resolves on restart of the daemon.
 */
export function resolveRuntimeSnapshotPath(
  pipelineDir: string,
  issueNumber: number,
  onLegacyOnly?: (legacyFile: string) => void
): string | null {
  const namePattern = snapshotNamePattern(issueNumber);
  const legacyPattern = legacyNamePattern(issueNumber);

  let entries: string[];
  try {
    entries = fs.readdirSync(pipelineDir);
  } catch {
    return null;
  }

  const candidates: Candidate[] = [];
  let legacyFile: string | null = null;
  for (const file of entries) {
    if (legacyPattern.test(file)) {
      legacyFile = file;
      continue;
    }
    if (!namePattern.test(file)) continue;
    try {
      const blob = JSON.parse(
        fs.readFileSync(path.join(pipelineDir, file), "utf-8")
      ) as SnapshotHeader;
      candidates.push({
        file,
        terminal: blob?.terminal === true,
        startedAtMs: typeof blob?.startedAt === "string" ? Date.parse(blob.startedAt) : Number.NaN,
      });
    } catch {
      // A snapshot mid-atomic-rename or corrupt is skipped, not fatal: the
      // scan still answers for its siblings.
    }
  }

  if (candidates.length === 0) {
    if (legacyFile && onLegacyOnly) onLegacyOnly(legacyFile);
    return null;
  }

  candidates.sort((a, b) => {
    if (a.terminal !== b.terminal) return a.terminal ? 1 : -1;
    // NaN-safe: an unparseable/absent stamp is treated as infinitely old, so it
    // never displaces a run that told us when it started.
    const aMs = Number.isNaN(a.startedAtMs) ? -Infinity : a.startedAtMs;
    const bMs = Number.isNaN(b.startedAtMs) ? -Infinity : b.startedAtMs;
    if (aMs !== bMs) return bMs - aMs;
    // Total order for a deterministic answer when two runs share an instant.
    return a.file < b.file ? 1 : a.file > b.file ? -1 : 0;
  });
  return path.join(pipelineDir, candidates[0].file);
}
