#!/usr/bin/env bash
# Seed: a working cart module with NO tests for the model to cover.
set -euo pipefail
mkdir -p src
cat > package.json <<'PKG'
{ "name": "fixture-cart", "private": true, "type": "module",
  "scripts": { "test": "vitest run", "coverage": "vitest run --coverage" },
  "devDependencies": { "vitest": "^2.0.0", "@vitest/coverage-v8": "^2.0.0" } }
PKG
cat > src/cart.ts <<'SRC'
export interface Line { sku: string; qty: number; priceUsd: number }
export class Cart {
  lines: Line[] = [];
  addItem(sku: string, qty: number, priceUsd: number) {
    if (qty <= 0) throw new Error("qty must be > 0");
    const existing = this.lines.find((l) => l.sku === sku);
    if (existing) existing.qty += qty; else this.lines.push({ sku, qty, priceUsd });
  }
  removeItem(sku: string) {
    const before = this.lines.length;
    this.lines = this.lines.filter((l) => l.sku !== sku);
    if (this.lines.length === before) throw new Error(`no such item: ${sku}`);
  }
  applyDiscount(pct: number) { for (const l of this.lines) l.priceUsd *= 1 - pct; }
  total() { return this.lines.reduce((s, l) => s + l.qty * l.priceUsd, 0); }
}
SRC
