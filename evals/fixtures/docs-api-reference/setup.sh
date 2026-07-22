#!/usr/bin/env bash
# Seed: a tasks REST router the model must document (no docs yet).
set -euo pipefail
mkdir -p src docs/api
cat > package.json <<'PKG'
{ "name": "fixture-docs-tasks", "private": true, "type": "module" }
PKG
cat > src/tasksRouter.ts <<'SRC'
// GET /tasks -> Task[]; POST /tasks {title} -> 201 Task; PATCH /tasks/:id {done} -> Task; DELETE /tasks/:id -> 204
export interface Task { id: string; title: string; done: boolean }
export function registerTaskRoutes(app: {
  get: (p: string, h: unknown) => void; post: (p: string, h: unknown) => void;
  patch: (p: string, h: unknown) => void; delete: (p: string, h: unknown) => void;
}) { void app; /* handlers omitted in fixture */ }
SRC
