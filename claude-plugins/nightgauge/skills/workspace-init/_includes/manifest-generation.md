# Existing-Manifest Handling & Manifest Generation (Phases 3–4)

Procedural detail for idempotent generation of
`.vscode/nightgauge-workspace.yaml`.

## Phase 3 — Existing manifest handling (idempotent merge)

```bash
MANIFEST="$WORKSPACE_ROOT/.vscode/nightgauge-workspace.yaml"
mkdir -p "$WORKSPACE_ROOT/.vscode"

if [ -f "$MANIFEST" ]; then
  echo "Existing manifest found — merging (idempotent)."
  # Collect repos already listed so we never duplicate an entry.
  EXISTING_REPOS="$(grep -E '^\s*- name:' "$MANIFEST" | sed -E 's/^\s*- name:\s*//; s/^[\"'\'']//; s/[\"'\'']$//')"
else
  echo "No existing manifest — generating fresh."
  EXISTING_REPOS=""
fi
```

**Merge rule** (idempotent — AC #5):

- An already-listed member (matched by `name`) is **kept as-is** — never
  duplicated, never reordered.
- A newly-detected member is **appended** under `repositories:`.
- The existing `routing.patterns` and `workspace.description` are **preserved**.

This is why we collect `EXISTING_REPOS` before writing: only members NOT in that
set are added.

## Phase 4 — Generate the manifest

Generate the YAML matching the verified working reference (the Acme
ecosystem manifest). The canonical shape:

```yaml
# Nightgauge Workspace Configuration
#
# Enables multi-repository workflows. Open this folder directly and the
# extension reads this manifest to show the shared project board across all
# member repos. Paths are relative to this file's parent (the workspace root).
# Topology: N:1 — all member repos share a single GitHub Project. Each member's
# own .nightgauge/config.yaml is canonical for owner/repo/project.

workspace:
  name: <WORKSPACE_NAME>
  description: <one-line description naming the members and shared project>

repositories:
  - name: <member-dir>
    path: <member-dir> # relative to the workspace root
    role: primary
    project_number: <SHARED_PROJECT>
  # ... one entry per member ...

routing:
  default_repository: <DEFAULT_REPO>
  patterns: [] # optional keyword→repo routing; preserved on merge

epic:
  cross_repo_tracking: true
  shared_milestones: true
```

Field rules:

- `workspace.name` — from `--name`, else prompt (interactive), else derive from
  the workspace root directory basename, title-cased. In `--dry-run`/headless,
  derive — never block.
- `repositories[].path` — the member directory name (relative to the workspace
  root; the manifest lives in `<root>/.vscode/`, paths are resolved from
  `<root>`).
- `repositories[].project_number` — the shared project number for all members.
- `role` — `primary` unless a member config marks it otherwise.

### Dry-run

```bash
if [ "$DRY_RUN" = "true" ]; then
  echo "── DRY RUN — would write $MANIFEST ──"
  cat "$GENERATED_TMP"
  echo "── (no file written) ──"
  exit 0
fi
```

### Write

Write the generated content with `Write`/`Edit`. When merging, append only the
new `repositories[]` entries beneath the existing ones; do not rewrite preserved
sections. After writing, proceed to Phase 5 verification.

> **Quote handling (#3859)**: the Go `sync-payload` parser strips a single pair
> of surrounding quotes from scalar values, so an unquoted `name: Acme
Product` and a quoted `name: "Acme Product"` both yield a clean
> `display_name`. Prefer unquoted scalars; quote only when the value contains a
> leading/trailing space or a `:` that would confuse the parser.
