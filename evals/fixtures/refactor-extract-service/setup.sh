#!/usr/bin/env bash
# Seed: a fat controller mixing HTTP + validation + persistence + email.
set -euo pipefail
mkdir -p src
cat > package.json <<'PKG'
{ "name": "fixture-fat-controller", "private": true, "type": "module",
  "scripts": { "build": "tsc --noEmit", "test": "vitest run" },
  "devDependencies": { "typescript": "^5.9.0", "vitest": "^2.0.0" } }
PKG
cat > tsconfig.json <<'TS'
{ "compilerOptions": { "strict": true, "module": "ESNext", "moduleResolution": "Bundler", "target": "ES2022", "noEmit": true },
  "exclude": ["**/*.test.ts", "node_modules"] }
TS
cat > src/userController.ts <<'SRC'
// Fat controller: HTTP + validation + persistence + email all inline.
const db: Record<string, { id: string; email: string }> = {};
let nextId = 1;
export function createUser(req: { body: { email?: string } }, res: { status: (n: number) => { json: (b: unknown) => void } }) {
  const email = req.body.email ?? "";
  if (!email.includes("@")) return res.status(400).json({ error: "invalid email" });
  if (Object.values(db).some((u) => u.email === email)) return res.status(409).json({ error: "exists" });
  const id = String(nextId++);
  db[id] = { id, email };
  // pretend-send welcome email
  void `welcome ${email}`;
  return res.status(201).json(db[id]);
}
SRC
