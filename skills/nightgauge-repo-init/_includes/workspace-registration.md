# Multi-Repo Workspace Registration (Phase 5.5)

Procedural detail for Phase 5.5: detect a multi-repo workspace and register the
target repository in the workspace configuration so the extension detects it
immediately.

**Detection logic:**

1. Check if a `*.code-workspace` file exists in the parent directory of the
   target repo
2. If found, scan sibling repos for `.vscode/nightgauge-workspace.yaml`
3. If a workspace YAML exists, check whether the target repo is already listed
   under `repositories:`

```bash
# Find the workspace YAML (usually in the primary repo)
WORKSPACE_YAML=""
PARENT_DIR="$(dirname "$REPO_ROOT")"

for sibling in "$PARENT_DIR"/*/; do
  candidate="$sibling.vscode/nightgauge-workspace.yaml"
  if [ -f "$candidate" ]; then
    WORKSPACE_YAML="$candidate"
    break
  fi
done

if [ -z "$WORKSPACE_YAML" ]; then
  echo "  — No multi-repo workspace detected (standalone repo)"
else
  REPO_BASENAME="$(basename "$REPO_ROOT")"
  if grep -q "name: $REPO_BASENAME" "$WORKSPACE_YAML"; then
    echo "  ✓ Already registered in workspace: $WORKSPACE_YAML"
  else
    echo "  + Registering $REPO_BASENAME in workspace config"
    # Registration requires adding to repositories list and optionally
    # adding routing patterns for the repo's domain keywords
  fi
fi
```

**N:1 topology detection**: After locating the workspace YAML, query the
project to see if multiple repos are already linked:

```bash
# Detect shared project number from the workspace YAML
SHARED_PROJECT=$(grep "shared_project_number:" "$WORKSPACE_YAML" 2>/dev/null | awk '{print $2}')

if [ -n "$SHARED_PROJECT" ]; then
  # Query linked repos to check if the target repo is already linked to the project
  LINKED_REPOS=$(nightgauge workspace repos-from-project \
    --project "$SHARED_PROJECT" --json 2>/dev/null || echo "[]")
  ALREADY_LINKED=$(echo "$LINKED_REPOS" | jq -r --arg name "$REPO_BASENAME" \
    '[.[] | select(.name == $name)] | length')

  if [ "$ALREADY_LINKED" -gt 0 ]; then
    echo "  ✓ Repo is already linked to Project #$SHARED_PROJECT — will be auto-derived"
    echo "    No manifest entry needed (N:1 auto-derivation path)"
    SKIP_MANIFEST_ENTRY=true
  else
    echo "  + Repo not yet linked to Project #$SHARED_PROJECT"
    echo "    Link it in GitHub: Settings → Linked Repositories"
  fi
fi
```

When a shared project is detected with >1 linked repo, offer to scaffold an N:1
manifest instead of per-repo entries:

```
Multiple repositories are linked to Project #6.
Generate N:1 manifest with shared_project_number: 6? (Y/n)
```

If the user accepts, scaffold:

```yaml
workspace:
  name: "YourWorkspace"
  shared_project_number: 6

repositories: [] # Auto-derived from ProjectV2.repositories at runtime
```

Otherwise fall back to per-repo entries as described below.

> **Note for Phase 6**: Per-repo `.nightgauge/config.yaml` MUST still have
> `project.number` set regardless of whether the workspace uses N:1 topology.
> The workspace manifest controls the Repositories view; config.yaml controls
> pipeline stage routing (`issue-pickup`, `feature-planning`, etc.).

**If the repo is NOT registered**, add it to the workspace YAML:

1. Append a new entry under `repositories:` with:
   - `name:` — the repo directory name
   - `path:` — relative path from the workspace YAML's repo to the target repo
     (e.g., `../acme-mobile`)
   - `role: primary`
   - `project_number:` — the project number from this repo's config.yaml (if available)
2. If the repo has a clear domain (detected from language, framework, or
   component labels chosen in Phase 2), add routing patterns under
   `routing.patterns:` with relevant keywords
3. Update the `workspace.description` field to include the new repo

**Ask before modifying** — the workspace YAML lives in a different repo:

```
The multi-repo workspace at nightgauge/.vscode/nightgauge-workspace.yaml
does not include this repository.

Add acme-mobile to the workspace config? (Y/n)
```

**Summary line** for Phase 7 report:

```
── Workspace ──────────────────────────────────────────────────────
  + registered: acme-mobile in nightgauge/.vscode/nightgauge-workspace.yaml
```

Or if standalone:

```
── Workspace ──────────────────────────────────────────────────────
  — standalone: no multi-repo workspace detected
```
