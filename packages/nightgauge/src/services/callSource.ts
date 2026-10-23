/**
 * GitHub-API call-source attribution (#360).
 *
 * `IpcClientBase.call()` logs every GitHub-API method with a `src=` label so a
 * refresh storm can be traced to its caller. The label lives here — in a tiny
 * standalone module with no heavy dependencies — rather than as a static on
 * IpcClientBase, so call sites can tag their fetches without importing the
 * IpcClient module (which unit tests routinely mock, and vitest makes accessing
 * a mock-omitted named export throw). IpcClientBase reads the value from here.
 */

let _activeCallSource: string | undefined;

/** The label to attribute the current GitHub-API call to, or undefined. */
export function getActiveCallSource(): string | undefined {
  return _activeCallSource;
}

/** Set the active source label (e.g. "user-refresh"). Cleared by passing undefined. */
export function setActiveCallSource(source: string | undefined): void {
  _activeCallSource = source;
}

/**
 * Run `fn` with the active call-source set to `source`, restoring the previous
 * value afterwards (even if `fn` throws). The label is read synchronously at
 * the top of `IpcClientBase.call()` — before the first await — so wrapping a
 * fetch that issues `github.rateLimit` + `board.list` tags both. `finally`
 * restores the prior label so nested/sequential callers don't leak a stale one.
 */
export async function withCallSource<T>(source: string, fn: () => Promise<T>): Promise<T> {
  const previous = _activeCallSource;
  _activeCallSource = source;
  try {
    return await fn();
  } finally {
    _activeCallSource = previous;
  }
}
