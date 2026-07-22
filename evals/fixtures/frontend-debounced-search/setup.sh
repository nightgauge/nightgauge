#!/usr/bin/env bash
# Seed: a TS project with a DebouncedSearch stub + a failing spec (fake timers +
# controllable promises) pinning debounce, stale-response cancellation, and state.
set -euo pipefail
mkdir -p src
cat > package.json <<'PKG'
{ "name": "fixture-debounced-search", "private": true, "type": "module",
  "scripts": { "build": "tsc --noEmit", "typecheck": "tsc --noEmit", "test": "vitest run" },
  "devDependencies": { "typescript": "^5.9.0", "vitest": "^2.0.0" } }
PKG
cat > tsconfig.json <<'TS'
{ "compilerOptions": { "strict": true, "module": "ESNext", "moduleResolution": "Bundler", "target": "ES2022", "lib": ["ES2022", "DOM"], "noEmit": true },
  "exclude": ["**/*.test.ts", "node_modules"] }
TS
cat > src/debouncedSearch.ts <<'SRC'
// TODO(model): implement DebouncedSearch<T>. See debouncedSearch.test.ts.
export interface SearchState<T> {
  query: string;
  results: T[];
  loading: boolean;
  error: string | null;
}

export interface DebouncedSearchOptions<T> {
  /** Runs the search; receives an AbortSignal for the in-flight request. */
  fetcher: (query: string, signal: AbortSignal) => Promise<T[]>;
  /** Debounce window in ms before a fetch is issued. */
  debounceMs: number;
  /** Called with a fresh state snapshot whenever state changes. */
  onChange: (state: SearchState<T>) => void;
}

export class DebouncedSearch<T> {
  constructor(_options: DebouncedSearchOptions<T>) {
    throw new Error("not implemented");
  }
  /** Set the query; debounced, coalesced, and race-safe. Empty clears. */
  setQuery(_query: string): void {
    throw new Error("not implemented");
  }
  /** Cancel any pending timer and in-flight request; stop emitting. */
  dispose(): void {
    throw new Error("not implemented");
  }
}
SRC
cat > src/debouncedSearch.test.ts <<'SRC'
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { DebouncedSearch, type SearchState } from "./debouncedSearch.js";

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (err: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const flush = () => Promise.resolve();

describe("DebouncedSearch", () => {
  it("coalesces rapid setQuery calls into one fetch of the latest query", async () => {
    const seen: string[] = [];
    const fetcher = vi.fn(async (q: string, _s: AbortSignal): Promise<string[]> => {
      seen.push(q);
      return [q];
    });
    const s = new DebouncedSearch<string>({ fetcher, debounceMs: 300, onChange: () => {} });
    s.setQuery("a");
    s.setQuery("ab");
    s.setQuery("abc");
    await vi.advanceTimersByTimeAsync(299);
    expect(fetcher).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(seen).toEqual(["abc"]);
  });

  it("emits loading while fetching then results on resolve", async () => {
    const d = deferred<string[]>();
    const fetcher = vi.fn((_q: string, _s: AbortSignal) => d.promise);
    const states: SearchState<string>[] = [];
    const s = new DebouncedSearch<string>({
      fetcher,
      debounceMs: 100,
      onChange: (st) => states.push({ ...st }),
    });
    s.setQuery("x");
    await vi.advanceTimersByTimeAsync(100);
    expect(states.at(-1)).toMatchObject({ query: "x", loading: true, error: null });
    d.resolve(["r1", "r2"]);
    await d.promise;
    await flush();
    expect(states.at(-1)).toMatchObject({ results: ["r1", "r2"], loading: false, error: null });
  });

  it("discards a stale response when a newer query superseded it", async () => {
    const first = deferred<string[]>();
    const second = deferred<string[]>();
    const fetcher = vi
      .fn((_q: string, _s: AbortSignal): Promise<string[]> => Promise.resolve([]))
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise);
    const states: SearchState<string>[] = [];
    const s = new DebouncedSearch<string>({
      fetcher,
      debounceMs: 100,
      onChange: (st) => states.push({ ...st }),
    });
    s.setQuery("a");
    await vi.advanceTimersByTimeAsync(100); // request #1 (a) in flight
    s.setQuery("b");
    await vi.advanceTimersByTimeAsync(100); // request #2 (b) in flight

    second.resolve(["B"]); // newer resolves first
    await second.promise;
    await flush();
    first.resolve(["A"]); // older resolves later — must be ignored
    await first.promise;
    await flush();

    expect(states.at(-1)).toMatchObject({ query: "b", results: ["B"], loading: false });
    expect(states.some((st) => st.results.includes("A"))).toBe(false);
  });

  it("captures fetch errors and clears loading", async () => {
    const d = deferred<string[]>();
    const fetcher = vi.fn((_q: string, _s: AbortSignal) => d.promise);
    const states: SearchState<string>[] = [];
    const s = new DebouncedSearch<string>({
      fetcher,
      debounceMs: 100,
      onChange: (st) => states.push({ ...st }),
    });
    s.setQuery("x");
    await vi.advanceTimersByTimeAsync(100);
    d.reject(new Error("network down"));
    await d.promise.catch(() => {});
    await flush();
    expect(states.at(-1)).toMatchObject({ query: "x", loading: false, error: "network down" });
  });

  it("clears immediately on empty query without fetching", async () => {
    const fetcher = vi.fn(async (_q: string, _s: AbortSignal): Promise<string[]> => ["r"]);
    const states: SearchState<string>[] = [];
    const s = new DebouncedSearch<string>({
      fetcher,
      debounceMs: 100,
      onChange: (st) => states.push({ ...st }),
    });
    s.setQuery("");
    await vi.advanceTimersByTimeAsync(100);
    expect(fetcher).not.toHaveBeenCalled();
    expect(states.at(-1)).toMatchObject({ query: "", results: [], loading: false, error: null });
  });

  it("dispose cancels a pending debounced fetch", async () => {
    const fetcher = vi.fn(async (_q: string, _s: AbortSignal): Promise<string[]> => ["r"]);
    const s = new DebouncedSearch<string>({ fetcher, debounceMs: 100, onChange: () => {} });
    s.setQuery("x");
    s.dispose();
    await vi.advanceTimersByTimeAsync(200);
    expect(fetcher).not.toHaveBeenCalled();
  });
});
SRC
