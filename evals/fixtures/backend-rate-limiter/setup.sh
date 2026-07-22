#!/usr/bin/env bash
# Seed: a TS project skeleton for a token-bucket middleware.
set -euo pipefail
mkdir -p src
cat > package.json <<'PKG'
{ "name": "fixture-rate-limiter", "private": true, "type": "module",
  "scripts": { "build": "tsc --noEmit", "typecheck": "tsc --noEmit", "test": "vitest run" },
  "devDependencies": { "typescript": "^5.9.0", "vitest": "^2.0.0" } }
PKG
cat > tsconfig.json <<'TS'
{ "compilerOptions": { "strict": true, "module": "ESNext", "moduleResolution": "Bundler", "target": "ES2022", "noEmit": true },
  "exclude": ["**/*.test.ts", "node_modules"] }
TS
cat > src/rateLimiter.ts <<'SRC'
// TODO(model): implement a time-based token-bucket rate limiter middleware.
export interface RateLimiterOptions { capacity: number; refillPerSecond: number; keyOf?: (req: unknown) => string; now?: () => number }
export function rateLimiter(_opts: RateLimiterOptions): unknown { throw new Error("not implemented"); }
SRC
