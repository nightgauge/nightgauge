#!/usr/bin/env bash
# Seed: a TS project with an LruCache stub + a comprehensive failing spec that
# pins LRU eviction order and per-entry TTL semantics.
set -euo pipefail
mkdir -p src
cat > package.json <<'PKG'
{ "name": "fixture-lru-cache", "private": true, "type": "module",
  "scripts": { "build": "tsc --noEmit", "typecheck": "tsc --noEmit", "test": "vitest run" },
  "devDependencies": { "typescript": "^5.9.0", "vitest": "^2.0.0" } }
PKG
cat > tsconfig.json <<'TS'
{ "compilerOptions": { "strict": true, "module": "ESNext", "moduleResolution": "Bundler", "target": "ES2022", "noEmit": true },
  "exclude": ["**/*.test.ts", "node_modules"] }
TS
cat > src/lruCache.ts <<'SRC'
// TODO(model): implement an O(1) LRU cache with per-entry TTL. See lruCache.test.ts.
export interface LruCacheOptions {
  /** Maximum live entries; the least-recently-used is evicted past this. */
  capacity: number;
  /** Optional time-to-live per entry, measured from insertion. */
  ttlMs?: number;
  /** Injected clock (ms). Defaults to Date.now when omitted. */
  now?: () => number;
}

export class LruCache<K, V> {
  constructor(_options: LruCacheOptions) {
    throw new Error("not implemented");
  }
  get(_key: K): V | undefined {
    throw new Error("not implemented");
  }
  set(_key: K, _value: V): void {
    throw new Error("not implemented");
  }
  has(_key: K): boolean {
    throw new Error("not implemented");
  }
  delete(_key: K): boolean {
    throw new Error("not implemented");
  }
  get size(): number {
    throw new Error("not implemented");
  }
}
SRC
cat > src/lruCache.test.ts <<'SRC'
import { describe, it, expect } from "vitest";
import { LruCache } from "./lruCache.js";

function makeClock(start = 0) {
  let t = start;
  return { now: () => t, advance: (ms: number) => void (t += ms) };
}

describe("LruCache", () => {
  it("stores and retrieves values, missing keys are undefined", () => {
    const c = new LruCache<string, number>({ capacity: 3 });
    c.set("a", 1);
    c.set("b", 2);
    expect(c.get("a")).toBe(1);
    expect(c.get("b")).toBe(2);
    expect(c.get("missing")).toBeUndefined();
    expect(c.size).toBe(2);
  });

  it("evicts the least-recently-used entry past capacity", () => {
    const c = new LruCache<string, number>({ capacity: 2 });
    c.set("a", 1);
    c.set("b", 2);
    c.set("c", 3); // a is LRU -> evicted
    expect(c.has("a")).toBe(false);
    expect(c.get("a")).toBeUndefined();
    expect(c.get("b")).toBe(2);
    expect(c.get("c")).toBe(3);
    expect(c.size).toBe(2);
  });

  it("get counts as a use and protects an entry from eviction", () => {
    const c = new LruCache<string, number>({ capacity: 2 });
    c.set("a", 1);
    c.set("b", 2);
    expect(c.get("a")).toBe(1); // a becomes MRU; b is now LRU
    c.set("c", 3); // evicts LRU -> b
    expect(c.has("b")).toBe(false);
    expect(c.get("a")).toBe(1);
    expect(c.get("c")).toBe(3);
  });

  it("set on an existing key updates the value and marks it most-recently-used", () => {
    const c = new LruCache<string, number>({ capacity: 2 });
    c.set("a", 1);
    c.set("b", 2);
    c.set("a", 10); // a updated + MRU; b now LRU
    c.set("c", 3); // evicts b
    expect(c.get("a")).toBe(10);
    expect(c.has("b")).toBe(false);
    expect(c.get("c")).toBe(3);
    expect(c.size).toBe(2);
  });

  it("delete removes an entry and reports whether it existed", () => {
    const c = new LruCache<string, number>({ capacity: 2 });
    c.set("a", 1);
    expect(c.delete("a")).toBe(true);
    expect(c.delete("a")).toBe(false);
    expect(c.get("a")).toBeUndefined();
    expect(c.size).toBe(0);
  });

  it("expires entries by TTL measured from insertion; expired read as absent", () => {
    const clock = makeClock();
    const c = new LruCache<string, number>({ capacity: 5, ttlMs: 1000, now: clock.now });
    c.set("a", 1);
    clock.advance(999);
    expect(c.get("a")).toBe(1); // still fresh
    clock.advance(2); // t = 1001 > 1000 -> expired
    expect(c.get("a")).toBeUndefined();
    expect(c.has("a")).toBe(false);
    expect(c.size).toBe(0); // expired entries are not counted
  });

  it("TTL is from insertion, not last access (a get does not extend life)", () => {
    const clock = makeClock();
    const c = new LruCache<string, number>({ capacity: 5, ttlMs: 1000, now: clock.now });
    c.set("a", 1);
    clock.advance(600);
    expect(c.get("a")).toBe(1); // access at 600 must not reset TTL
    clock.advance(500); // t = 1100 -> expired since insertion
    expect(c.get("a")).toBeUndefined();
  });

  it("re-setting a key resets its TTL window", () => {
    const clock = makeClock();
    const c = new LruCache<string, number>({ capacity: 5, ttlMs: 1000, now: clock.now });
    c.set("a", 1);
    clock.advance(900);
    c.set("a", 2); // reset TTL at t=900
    clock.advance(500); // 500ms since reset
    expect(c.get("a")).toBe(2);
    clock.advance(600); // 1100ms since reset -> expired
    expect(c.get("a")).toBeUndefined();
  });

  it("without ttlMs, entries never expire", () => {
    const clock = makeClock();
    const c = new LruCache<string, number>({ capacity: 5, now: clock.now });
    c.set("a", 1);
    clock.advance(1_000_000);
    expect(c.get("a")).toBe(1);
  });
});
SRC
