/**
 * runtimeStubSweep - classify stale / cross-contaminated pipeline runtime stubs.
 *
 * Startup restore (bootstrap/services.ts, Issue #2008) scans
 * `.nightgauge/pipeline/runtime-<N>.json` files to resurrect paused runs. In a
 * multi-repo workspace, concurrent dispatch used to strand orphan stubs in the
 * WRONG repo: the Go IPC server persisted a run's first "initialized" snapshot —
 * before the run's repo slug was seeded — into the shared launch root, leaving a
 * `runtime-<N>.json` with empty `repo`/`stage` in a repo that never ran the
 * issue (Issue #307). Those stubs are never cleaned and risk zombie-run
 * restoration on the next window reload.
 *
 * This module is the pure decision layer, split out so it is unit-testable
 * without the VSCode/bootstrap surface. The Go-side fix stops NEW empty-repo
 * stubs from being written; this sweep removes any that predate the fix or slip
 * through, and deletes runtime files whose `repo` does not match the repo that
 * contains them.
 *
 * @see Issue #307 - Multi-repo concurrent run state cross-contamination
 * @see Issue #2008 - Restore paused pipeline state on activation
 */

import { RUN_IDENTITY_SHAPE } from "@nightgauge/sdk";

// LOUD BEATS SILENT. `RUN_IDENTITY_SHAPE` can arrive `undefined` at runtime, and
// not hypothetically: the extension resolves @nightgauge/sdk from `dist/`, and a
// dist built before #424 added the export has no such key — through esbuild's
// CJS interop that is a plain `undefined`, not a link error. Interpolating it
// yields the STRING "undefined" inside ANY_RUNTIME_FILE, which would then refuse
// every new-scheme snapshot: activation silently stops offering live runs for
// restore. Fail at module load instead, where the message names the cause and
// the fix. (`pretest`/`prebuild:bundle` run scripts/check-sdk-freshness.sh to
// catch the same staleness one step earlier; this is the runtime backstop.)
if (typeof RUN_IDENTITY_SHAPE !== "string" || RUN_IDENTITY_SHAPE.length === 0) {
  throw new Error(
    "runtimeStubSweep: a stale or mismatched @nightgauge/sdk dist did not " +
      "provide RUN_IDENTITY_SHAPE — rebuild it (npm run build -w @nightgauge/sdk)"
  );
}

/** The subset of runtime-<N>.json fields the sweep inspects. */
export interface RuntimeStubFields {
  repo?: string | null;
  stage?: string | null;
  issueNumber?: number;
  paused?: boolean;
}

/** Verdict for a single runtime file. */
export type RuntimeStubVerdict =
  | { action: "keep"; reason?: string }
  | { action: "delete"; reason: "empty-identity" | "repo-mismatch" };

/**
 * The pre-ADR-017 snapshot name — `runtime-{issue}.json`. THE ONLY NAME THE
 * SWEEP MAY DELETE.
 */
export const LEGACY_RUNTIME_FILE = /^runtime-(\d+)\.json$/;

/**
 * Every snapshot name the activation scan READS: the legacy one plus ADR-017's
 * `runtime-{issue}-{runId}.json` (#370). Reading and prompting are
 * non-destructive, so this is deliberately wider than the sweep's pattern.
 * Issue number is capture 1 in both, so callers parse it the same way.
 *
 * The identity fragment is INTERPOLATED from the one TypeScript definition
 * (`RUN_IDENTITY_SHAPE`, @nightgauge/sdk) rather than transcribed here (#424),
 * and WRAPPED in `(?:…)` per its documented embedding contract — the same way Go
 * groups `IdentityPattern` at every embed site. The wrap is what makes the outer
 * `(?:-…)?` optional group mean "an OPTIONAL identity suffix" rather than
 * "optionally the fragment's first alternative, or else its others unguarded".
 *
 * That the fragment contributes NO capture groups — which is what keeps the
 * issue number at capture 1 here — is pinned in the SDK, by the group-count arm
 * in `packages/nightgauge-sdk/src/__tests__/runIdentity.test.ts`. It cannot be
 * pinned from this file: `(\d+)` opens before the interpolation, so group 1 is
 * structurally immovable and an assertion about it here is a tautology.
 */
export const ANY_RUNTIME_FILE = new RegExp(
  `^runtime-(\\d+)(?:-(?:${RUN_IDENTITY_SHAPE}))?\\.json$`
);

/**
 * Decide what the activation sweep may do with a file, BY NAME FIRST.
 *
 * This is the gate that stands between {@link classifyRuntimeStub} and an
 * `fs.unlink`, and it is a separate exported function for one reason: the
 * obvious future cleanup is to collapse the two regexes above back into one,
 * at which point classifyRuntimeStub would run over run-identity-keyed
 * snapshots and a LIVE run whose mid-dispatch body still has `repo: ""` or
 * `stage: ""` would be classified `empty-identity` and DELETED at extension
 * activation — the crash snapshot destroyed, the run unreconcilable, and the
 * whole vitest suite green because nothing reached the branch. Now something
 * does.
 *
 * Rules:
 *  - A NEW-SCHEME name is always kept, whatever its body says.
 *  - A LEGACY name gets the classifier's verdict. **This sweep is where legacy
 *    disposition ends up, permanently.** ADR-017's Migration section planned to
 *    hand it to a one-shot Go sweep and narrow this filter to the new scheme;
 *    that Go sweep was never built, and #381 decided against building it —
 *    legacy files are abandoned unswept, because there has never been a
 *    published release that could have written the legacy name. Do not "finish"
 *    the narrowing: it would leave nothing owning legacy names at all.
 *  - Anything else is kept (the caller should not have offered it).
 *
 * @param fileName The snapshot's basename.
 * @param classify Applied only for legacy names — normally a closure over
 *   {@link classifyRuntimeStub} bound to the parsed body and containing repo.
 */
export function runtimeSweepVerdict(
  fileName: string,
  classify: () => RuntimeStubVerdict
): RuntimeStubVerdict {
  if (LEGACY_RUNTIME_FILE.test(fileName)) {
    return classify();
  }
  return {
    action: "keep",
    reason: "new-scheme snapshot — sweep is legacy-only (ADR-017; #381)",
  };
}

/**
 * Case-insensitive repo-slug match, tolerant of `owner/repo` vs short-name form
 * (mirrors {@link WorkspaceManager.findRepositoryByGitHub}). Deliberately
 * lenient: a false "match" merely keeps a file, whereas a false "mismatch"
 * would delete a legitimate runtime — so we only flag a mismatch when the
 * repository names genuinely differ.
 */
export function repoSlugsMatch(a: string, b: string): boolean {
  const norm = (s: string) => s.trim().toLowerCase();
  if (norm(a) === norm(b)) return true;
  const shortName = (s: string) => (s.includes("/") ? (s.split("/")[1] ?? s) : s);
  return norm(shortName(a)) === norm(shortName(b));
}

/**
 * Decide whether a runtime stub is stale cross-contamination that must be
 * ignored AND deleted at startup restore.
 *
 * Rules (in order):
 * 1. Empty `repo` OR empty `stage` → the never-cleaned "initialized" stub
 *    (`empty-identity`). This is the exact #307 signature.
 * 2. `repo` set but pointing at a DIFFERENT repo than the one whose
 *    `.nightgauge` directory contains the file → `repo-mismatch`. Skipped when
 *    `containingRepoSlug` is unknown (undefined), so an unresolvable container
 *    never causes a delete.
 * 3. Otherwise → keep (and let the caller run its paused-restore logic).
 *
 * @param runtime            Parsed runtime-<N>.json fields.
 * @param containingRepoSlug `owner/repo` (or short name) of the repo containing
 *                           this file, or undefined when it cannot be resolved.
 */
export function classifyRuntimeStub(
  runtime: RuntimeStubFields,
  containingRepoSlug?: string
): RuntimeStubVerdict {
  const repo = typeof runtime.repo === "string" ? runtime.repo.trim() : "";
  const stage = typeof runtime.stage === "string" ? runtime.stage.trim() : "";

  if (!repo || !stage) {
    return { action: "delete", reason: "empty-identity" };
  }

  if (containingRepoSlug && !repoSlugsMatch(repo, containingRepoSlug)) {
    return { action: "delete", reason: "repo-mismatch" };
  }

  return { action: "keep" };
}
