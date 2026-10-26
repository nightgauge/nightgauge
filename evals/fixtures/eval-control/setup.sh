#!/usr/bin/env bash
# Seed for the pinned control task (#2117): a small TypeScript ledger package
# with green tests and a CI workflow. Run in an empty directory; idempotent.
# The commit this produces in nightgauge/eval-control-fixture is FROZEN — see
# README.md before changing anything here.
set -euo pipefail
mkdir -p src tests .github/workflows
cat > package.json <<'PKG'
{
  "name": "eval-control-fixture",
  "private": true,
  "type": "module",
  "scripts": { "typecheck": "tsc --noEmit", "test": "vitest run" },
  "devDependencies": { "typescript": "5.9.3", "vitest": "2.1.9" }
}
PKG
cat > tsconfig.json <<'TS'
{
  "compilerOptions": {
    "strict": true,
    "module": "ESNext",
    "moduleResolution": "Bundler",
    "target": "ES2022",
    "noEmit": true,
    "skipLibCheck": true
  },
  "include": ["src", "tests"]
}
TS
cat > .gitignore <<'GI'
node_modules/
GI
cat > src/ledger.ts <<'SRC'
/** A single ledger entry. `amountCents` is positive for income, negative for spend. */
export interface Transaction {
  id: string;
  /** ISO-8601 instant, e.g. "2026-01-31T23:30:00Z". */
  occurredAt: string;
  amountCents: number;
  category: string;
}

/** Sum of all transaction amounts, in cents. */
export function balance(transactions: readonly Transaction[]): number {
  return transactions.reduce((sum, t) => sum + t.amountCents, 0);
}

/** Transactions whose category matches `category` exactly. */
export function byCategory(
  transactions: readonly Transaction[],
  category: string
): Transaction[] {
  return transactions.filter((t) => t.category === category);
}
SRC
cat > src/index.ts <<'SRC'
export { balance, byCategory, type Transaction } from "./ledger.js";
SRC
cat > tests/ledger.test.ts <<'TST'
import { describe, expect, it } from "vitest";
import { balance, byCategory, type Transaction } from "../src/index.js";

const txs: Transaction[] = [
  { id: "1", occurredAt: "2026-01-05T10:00:00Z", amountCents: 250000, category: "salary" },
  { id: "2", occurredAt: "2026-01-06T12:00:00Z", amountCents: -4599, category: "groceries" },
  { id: "3", occurredAt: "2026-01-09T18:30:00Z", amountCents: -1250, category: "groceries" },
];

describe("balance", () => {
  it("sums all amounts", () => {
    expect(balance(txs)).toBe(244151);
  });
  it("is zero for no transactions", () => {
    expect(balance([])).toBe(0);
  });
});

describe("byCategory", () => {
  it("filters by exact category", () => {
    expect(byCategory(txs, "groceries").map((t) => t.id)).toEqual(["2", "3"]);
  });
});
TST
cat > .github/workflows/ci.yml <<'CI'
name: CI
on:
  push:
    branches: [main]
  pull_request:
permissions:
  contents: read
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          node-version: 22
      - run: npm install --no-audit --no-fund
      - run: npm run typecheck
      - run: npm test
CI
cat > README.md <<'MD'
# eval-control-fixture

A frozen fixture for Nightgauge model evaluation. Its first commit is the pinned
base of the eval control task; open issues here are eval inputs, not bug
reports. Please do not edit the control issue or push to `main`.
MD
