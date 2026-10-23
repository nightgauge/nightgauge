/**
 * Acquiring VSCode is the only part of the host smoke tier that talks to a
 * host we do not control: `downloadAndUnzipVSCode()` resolves the version
 * against `update.code.visualstudio.com` and then fetches the build.
 *
 * That step failed once on `main` (#770). The launcher fix that landed first
 * captured the evidence on its next occurrence — a TCP connect timeout, not a
 * slow or corrupt download:
 *
 *   ERROR: VSCode failed to launch. Underlying error:
 *   AggregateError [ETIMEDOUT]:
 *       at internalConnectMultiple (node:net:1339:18)
 *       at Timeout.internalConnectMultipleTimeout (node:net:1969:5)
 *
 * 303ms elapsed between "Resolving version…" and the error, which is the Happy
 * Eyeballs attempt timeout expiring with every candidate address unreachable.
 * The update service was simply not reachable from that runner at that moment.
 *
 * Retrying is safe *here* in a way that retrying the tier would not be, and
 * this module exists to keep the two apart: at this point no test has
 * executed, so a retry cannot turn a failing assertion into a green run.
 * `runTests()` in launch.ts is deliberately left with no retry at all, and is
 * handed the resolved executable so it performs no network I/O of its own.
 *
 * Why this is not in tests/vscode-host/: everything there needs a real
 * extension host, and scripts/check-test-runner-coverage.sh enforces that by
 * rejecting a *.test.ts in that tree. This module is plain Node — it runs
 * before VSCode exists — so it lives where vitest can collect its test.
 *
 * Every failed attempt is reported. A genuine outage then reads as three
 * logged failures rather than one silent recovery, so this absorbs a blip
 * without hiding a trend.
 */

export const ACQUIRE_ATTEMPTS = 3;

/** Delay before attempt N+1. One entry shorter than ACQUIRE_ATTEMPTS: there is
 *  no wait after the final failure. */
export const ACQUIRE_BACKOFF_MS = [2_000, 6_000];

export interface AcquireDeps {
  /** Resolves the version and downloads the build; returns the executable. */
  download: () => Promise<string>;
  sleep: (ms: number) => Promise<void>;
  log: (message: string) => void;
  warn: (message: string) => void;
}

export async function acquireVSCode(deps: AcquireDeps): Promise<string> {
  let lastErr: unknown;

  for (let attempt = 1; attempt <= ACQUIRE_ATTEMPTS; attempt++) {
    try {
      const executable = await deps.download();
      if (attempt > 1) {
        deps.log(`VSCode acquired on attempt ${attempt}/${ACQUIRE_ATTEMPTS}.`);
      }
      return executable;
    } catch (err) {
      lastErr = err;
      const detail = err instanceof Error ? err.message : String(err);
      deps.warn(`acquiring VSCode failed (attempt ${attempt}/${ACQUIRE_ATTEMPTS}): ${detail}`);

      const backoff = ACQUIRE_BACKOFF_MS[attempt - 1];
      if (attempt < ACQUIRE_ATTEMPTS && backoff !== undefined) {
        await deps.sleep(backoff);
      }
    }
  }

  throw lastErr;
}
