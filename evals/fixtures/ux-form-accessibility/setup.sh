#!/usr/bin/env bash
# Seed: a SignupForm with accessibility problems for the model to fix.
set -euo pipefail
mkdir -p src
cat > package.json <<'PKG'
{ "name": "fixture-ux-form", "private": true, "type": "module",
  "scripts": { "build": "tsc --noEmit", "test": "vitest run" },
  "devDependencies": { "typescript": "^5.9.0", "vitest": "^2.0.0" } }
PKG
cat > tsconfig.json <<'TS'
{ "compilerOptions": { "strict": true, "module": "ESNext", "moduleResolution": "Bundler", "target": "ES2022", "noEmit": true },
  "exclude": ["**/*.test.ts", "node_modules"] }
TS
cat > src/SignupForm.ts <<'SRC'
// Inaccessible seed: unlabeled inputs, no focus styles, fixed 600px width.
export const signupFormHtml = `
<form style="width:600px">
  <input type="email" placeholder="Email" />
  <input type="password" placeholder="Password" />
  <button>Sign up</button>
</form>`;
SRC
