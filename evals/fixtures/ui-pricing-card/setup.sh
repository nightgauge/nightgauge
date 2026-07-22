#!/usr/bin/env bash
# Seed: a Vitest + TS project with an empty PricingCards stub for the model to build.
set -euo pipefail
mkdir -p src
cat > package.json <<'PKG'
{ "name": "fixture-ui-pricing-card", "private": true, "type": "module",
  "scripts": { "build": "tsc --noEmit", "test": "vitest run" },
  "devDependencies": { "typescript": "^5.9.0", "vitest": "^2.0.0" } }
PKG
cat > tsconfig.json <<'TS'
{ "compilerOptions": { "strict": true, "module": "ESNext", "moduleResolution": "Bundler", "target": "ES2022", "jsx": "preserve", "noEmit": true },
  "exclude": ["**/*.test.ts", "node_modules"] }
TS
cat > src/PricingCards.ts <<'SRC'
// TODO(model): implement PricingCards per the task instruction.
export interface Tier { name: string; priceUsd: number; features: string[]; highlighted?: boolean }
export function PricingCards(_props: { tiers: Tier[]; onSelect: (tier: Tier) => void }): unknown {
  throw new Error("not implemented");
}
SRC
