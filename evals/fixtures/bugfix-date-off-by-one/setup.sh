#!/usr/bin/env bash
# Seed: a buggy daysBetween + a failing reproduction test.
set -euo pipefail
mkdir -p src
cat > package.json <<'PKG'
{ "name": "fixture-date-bug", "private": true, "type": "module",
  "scripts": { "test": "vitest run" }, "devDependencies": { "vitest": "^2.0.0" } }
PKG
cat > src/dates.ts <<'SRC'
// BUG: integer truncation across DST/month boundaries drops a day.
export function daysBetween(start: Date, end: Date): number {
  const ms = end.getTime() - start.getTime();
  return Math.floor(ms / (1000 * 60 * 60 * 24));
}
SRC
cat > src/dates.test.ts <<'SRC'
import { describe, it, expect } from "vitest";
import { daysBetween } from "./dates.js";
describe("daysBetween", () => {
  it("same day is 0", () => expect(daysBetween(new Date("2026-03-01"), new Date("2026-03-01"))).toBe(0));
  it("spans a month boundary correctly", () =>
    expect(daysBetween(new Date("2026-01-31T00:00:00Z"), new Date("2026-03-01T00:00:00Z"))).toBe(29));
});
SRC
