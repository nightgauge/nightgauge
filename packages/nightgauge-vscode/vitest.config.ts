import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    globals: true,
    environment: "node",
    // The full hosted-runner suite exercises nearly 11,000 orchestration tests.
    // Individual async tests can exceed Vitest's 5-second default under shared
    // runner load even when their assertions are healthy; retain a bounded
    // timeout while avoiding false failures and cascading shared-state errors.
    testTimeout: 15_000,
    // #173 — kill the `onUserConsoleLog` RPC at its source rather than racing
    // it. By default vitest intercepts console output in the worker and
    // forwards each line to the main process over RPC so it can attribute the
    // line to a test file. Across ~11,000 tests that is a very high-volume
    // channel, and a line emitted close to a worker's teardown leaves an RPC
    // in flight when the environment closes:
    //
    //   EnvironmentTeardownError: [vitest-worker]: Closing rpc while
    //   "onUserConsoleLog" was pending
    //
    // Zero failing assertions, non-zero exit. Widening `teardownTimeout` was
    // tried first and measured at 3/10 runs still failing — it bounds how long
    // teardown HOOKS may take, not how long an in-flight RPC has to drain, so
    // it never addressed this. With interception off, console output goes
    // straight to the terminal (unattributed to a test file, but not lost) and
    // the RPC that races teardown is never created.
    disableConsoleIntercept: true,
    // Retained as a safety margin for a suite this size — slow teardown hooks
    // under runner load are a real (separate) failure mode. NOT the #173 fix.
    teardownTimeout: 10_000,
    include: ["tests/**/*.test.ts"],
    // Browser-driven webview tests live under tests/playwright/ and use the
    // *.playwright.ts naming convention exclusively (never *.test.ts) — see
    // playwright.config.ts's testMatch. Excluding the whole directory keeps
    // that tier out of vitest's collection even if a stray *.test.ts ever
    // lands there; scripts/check-test-runner-coverage.sh (#744) is what
    // actually catches that case and fails CI on it.
    exclude: ["tests/playwright/**"],
    setupFiles: ["tests/setup.ts"],
    // #1400 — provision the gitignored packaging artifacts (dist/ data files,
    // THIRD_PARTY_NOTICES) that tests/integration/*Packaging.test.ts assert on,
    // so a fresh worktree starts green instead of with five red tests nobody
    // can act on. globalSetup rather than `pretest` because the failing
    // invocation is a bare `npx vitest run`, which never runs npm lifecycle
    // scripts; and it runs once in the MAIN process, so workers cannot race the
    // same copy. It provisions only what is ABSENT — see tests/globalSetup.ts
    // for why rebuilding unconditionally would gut the #436 stale-dist guard.
    globalSetup: ["tests/globalSetup.ts"],
    coverage: {
      provider: "v8",
      reporter: ["text", "json", "html"],
      include: ["src/**/*.ts"],
      exclude: [
        // VSCode API entry point - requires heavy mocking with no coverage value
        "src/extension.ts",
        // HTML template generators - pure string builders with no logic to test
        "src/views/**/*Html.ts",
      ],
      // Baseline thresholds to prevent coverage regression
      thresholds: {
        lines: 50,
        functions: 50,
      },
    },
  },
});
