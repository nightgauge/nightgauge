/**
 * globalSetup.ts — provision the packaging artifacts the integration suite
 * asserts on, so a fresh worktree starts green (#1400).
 *
 * THE PROBLEM THIS SOLVES. Three integration suites assert on build outputs
 * that are gitignored and that nothing in the test path produces:
 *
 *   tests/integration/failureTaxonomyPackaging.test.ts  -> dist/failure-taxonomy.yaml
 *   tests/integration/modelRegistryPackaging.test.ts    -> dist/model-registry.json
 *   tests/integration/claudeAgentSdkPackaging.test.ts   -> THIRD_PARTY_NOTICES
 *
 * `dist/` and `THIRD_PARTY_NOTICES` are both gitignored, and the only things
 * that write them are `npm run build:assets` and `npm run build:notices`. CI
 * and scripts/ci-local.sh both run the full build BEFORE vitest, so the suites
 * pass there — and a fresh clone or worktree starts with five red tests that
 * mean nothing.
 *
 * The cost of that is judgement, not minutes: five permanent failures teach
 * every reader to wave a red integration suite through as "environmental",
 * which is exactly the reflex that lets a real regression ride along.
 *
 * WHY globalSetup AND NOT `pretest`. The failing invocation is a bare
 * `npx vitest run` — the issue's own reproduce, and what IDE gutters and
 * single-file runners use. `pretest` is npm-only, so it never runs on that
 * path (and in a fresh worktree it exits 1 on check-sdk-freshness.sh before
 * reaching the suite at all). `globalSetup` runs once per vitest process
 * regardless of file filters, and in the MAIN process — which also stops three
 * workers racing the same `cp`.
 *
 * WHY IT PROVISIONS ONLY WHAT IS ABSENT — and why that is a deliberate
 * deviation from the issue's own verification wording. #1400 asked to "rebuild
 * rather than reuse whatever is there". Doing that would break the guard this
 * file sits next to: `build:assets` IS `cp <sdk source> dist/`, and
 * modelRegistryPackaging.test.ts's "dist copy deep-equals the SDK source"
 * assertion (#436) exists precisely to catch a dist copy that has gone stale
 * against that source. Re-copying before every run makes the two sides equal
 * by construction, so that guard could never fail again — a test that cannot
 * go red, produced by the fix for tests that could not go green.
 *
 * So the predicate is FRESH-TREE, not stale-tree: provision when the artifact
 * is absent, and never touch one that exists. A half-built tree (dist/ present
 * but a data file inside it missing or stale) is a real defect, and the
 * existing assertions — with their "run `npm run build` first" messages — are
 * the correct, honest failure for it.
 *
 * SCOPE. This makes a fresh WORKTREE green. A fresh CLONE additionally needs
 * the SDK built (`npm run build --workspace=@nightgauge/sdk`): 118 test files
 * import `@nightgauge/sdk`, whose `main` is `dist/index.js`, and `npm install`
 * does not build it. That is a separate, louder failure — a module resolution
 * error, not five quiet assertion failures — and is out of scope here.
 */
import { execFileSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

/**
 * The data files `build:assets` copies into `dist/`, as [destination, source].
 *
 * Mirrors the two `cp`s in package.json's `build:assets`. The marketplace half
 * of that script is deliberately not reproduced: no vitest assertion reads it
 * (the `dist/skills` and `dist/claude-plugins` hits in the skillRunner tests
 * are synthetic string paths), so provisioning it would be work the suite does
 * not need.
 */
const PACKAGED_DATA_FILES = [
  ["dist/failure-taxonomy.yaml", "../nightgauge-sdk/src/analysis/failure-taxonomy.yaml"],
  ["dist/model-registry.json", "../nightgauge-sdk/src/eval/model-registry.json"],
] as const;

/** One provisioning command, with the reason it is being run. */
export interface PackagingStep {
  /** Why this step is needed — surfaced in the log line before it runs. */
  reason: string;
  command: string;
  args: string[];
  cwd: string;
}

/** Runs a step. Injected so tests can drive the planner without shelling out. */
export type StepRunner = (step: PackagingStep) => void;

/**
 * Decide which packaging artifacts are missing from `pkgDir`.
 *
 * Pure: it only reads the filesystem and returns a plan. Returns `[]` when both
 * artifacts are present — see the header for why an existing artifact is never
 * rebuilt.
 *
 * @param pkgDir - the nightgauge-vscode package directory
 */
export function planPackagingArtifacts(pkgDir: string): PackagingStep[] {
  const repoRoot = resolve(pkgDir, "..", "..");
  const steps: PackagingStep[] = [];

  // Gate PER DATA FILE, not on the `dist/` directory.
  //
  // The first version gated on the directory, reasoning that a present dist/
  // with a missing file was a half-built tree the suite should report rather
  // than repair. That is wrong about how dist/ comes to exist: `build:types`
  // (`tsc --emitDeclarationOnly`, outDir dist/) and `build:bundle` (esbuild
  // --outfile=dist/extension.cjs) each CREATE dist/ with neither data file, and
  // `build:types` runs FIRST in the full `build` chain — so an interrupted or
  // failed build, or a bare `npm run watch`, leaves exactly that state. The
  // planner then did nothing and four of the five tests stayed red.
  //
  // COPIED PER FILE RATHER THAN BY SHELLING TO `build:assets`, and that is the
  // load-bearing detail. `build:assets` recopies BOTH data files, so invoking
  // it because ONE is missing would silently overwrite a present-but-stale
  // sibling — healing exactly the drift modelRegistryPackaging's #436
  // deep-equal assertion exists to expose. Gating per file is not enough;
  // copying per file is what keeps that guard able to fail.
  for (const [dest, src] of PACKAGED_DATA_FILES) {
    // A present file is never touched, so #436 stays able to fail.
    if (existsSync(join(pkgDir, dest))) continue;
    steps.push({
      reason: `${dest} is absent — the packaging suites assert on it`,
      command: "bash",
      args: [
        "-c",
        `mkdir -p "$(dirname "$1")" && cp "$2" "$1"`,
        "--",
        join(pkgDir, dest),
        resolve(pkgDir, src),
      ],
      cwd: pkgDir,
    });
  }

  if (!existsSync(join(pkgDir, "THIRD_PARTY_NOTICES"))) {
    steps.push({
      reason:
        "THIRD_PARTY_NOTICES is absent — claudeAgentSdkPackaging asserts the Agent SDK is not " +
        "listed as redistributed software",
      command: "node",
      args: [join(repoRoot, "scripts", "generate-third-party-notices.mjs")],
      cwd: repoRoot,
    });
  }

  return steps;
}

/** The default runner: shell out, inherit stdio, throw on a non-zero exit. */
const execStep: StepRunner = (step) => {
  execFileSync(step.command, step.args, { cwd: step.cwd, stdio: "inherit" });
};

/**
 * Provision whatever `planPackagingArtifacts` says is missing.
 *
 * A failing step throws, which aborts the whole vitest run. That is on purpose:
 * degrading to "carry on and let the assertions fail" would reproduce the exact
 * five-red-tests state this exists to remove, only with a warning above it.
 *
 * @param pkgDir - the nightgauge-vscode package directory
 * @param run - step executor; injected by tests
 */
export function ensurePackagingArtifacts(pkgDir: string, run: StepRunner = execStep): void {
  for (const step of planPackagingArtifacts(pkgDir)) {
    console.log(`[packaging] ${step.reason} — running \`${step.command} ${step.args.join(" ")}\``);
    run(step);
  }
}

export default function setup(): void {
  ensurePackagingArtifacts(resolve(dirname(fileURLToPath(import.meta.url)), ".."));
}
