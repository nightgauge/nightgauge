# Member Detection & Project Derivation (Phases 0–2)

Procedural detail for resolving the workspace root, detecting member repos, and
deriving the shared GitHub Project.

## Phase 0 — Resolve workspace root & parse arguments

The workspace root is the parent folder that _contains_ the member repos (not a
repo itself). Resolve it from `--root`, else the current directory.

```bash
WORKSPACE_ROOT="${ARG_ROOT:-$(pwd)}"
WORKSPACE_ROOT="$(cd "$WORKSPACE_ROOT" && pwd)"   # absolutize
DRY_RUN="${ARG_DRY_RUN:-false}"
echo "Workspace root: $WORKSPACE_ROOT"
```

Argument parsing: `--dry-run`, `--name <name>`, `--project <N>`, `--root <path>`.

## Phase 1 — Detect member repositories

A member is an **immediate subdirectory** of the workspace root that contains
`.nightgauge/config.yaml`. Scan deterministically:

```bash
MEMBERS=()
for dir in "$WORKSPACE_ROOT"/*/; do
  cfg="$dir.nightgauge/config.yaml"
  if [ -f "$cfg" ]; then
    MEMBERS+=("$(basename "$dir")")
  fi
done

echo "Detected members: ${MEMBERS[*]}"
if [ "${#MEMBERS[@]}" -lt 2 ]; then
  echo "ERROR: found ${#MEMBERS[@]} member repo(s) under $WORKSPACE_ROOT; need >= 2." >&2
  echo "A single repo does not need a workspace manifest — run repo-init in it instead." >&2
  exit 1
fi
```

For each member, extract `owner`, `repo`, and `project.number`. The Go loader
resolves owner/repo from top-level fields **or** the legacy `github:` block
(#3859), so prefer the binary over hand-parsing YAML when possible. A robust
read uses `yq` when available, falling back to grep:

```bash
read_member_field() {  # $1 = member dir, $2 = yq path, $3 = grep key
  local cfg="$WORKSPACE_ROOT/$1/.nightgauge/config.yaml"
  if command -v yq >/dev/null 2>&1; then
    yq -r "$2 // \"\"" "$cfg" 2>/dev/null
  else
    grep -E "^\s*$3:" "$cfg" | head -1 | sed -E "s/^\s*$3:\s*//; s/^[\"']//; s/[\"']$//"
  fi
}

for m in "${MEMBERS[@]}"; do
  owner="$(read_member_field "$m" '.owner // .github.owner' 'owner')"
  proj="$(read_member_field "$m" '.project.number' 'number')"
  echo "  $m → owner=$owner project=$proj"
done
```

> **Note**: owner/repo may live under the top-level keys OR a `github:` block.
> Both resolve correctly through the Go binary as of #3859 — when deriving the
> manifest you only need each member's directory name (for `path`/`name`) and
> `project.number`.

## Phase 2 — Derive the shared project

Resolution order for the shared project number:

1. Explicit `--project <N>`.
2. The members' `project.number` values — they should all agree (N:1 topology).
   If they disagree, fail and instruct the user to pass `--project`.
3. Fallback: `nightgauge workspace repos-from-project --project <N>`
   confirms which repos GitHub reports as linked to the project.

```bash
if [ -n "${ARG_PROJECT:-}" ]; then
  SHARED_PROJECT="$ARG_PROJECT"
else
  # Collect distinct project numbers across members.
  distinct="$(for m in "${MEMBERS[@]}"; do read_member_field "$m" '.project.number' 'number'; done | sort -u | grep -v '^$')"
  count="$(echo "$distinct" | grep -c . || true)"
  if [ "$count" -gt 1 ]; then
    echo "ERROR: members point at different projects:" >&2
    echo "$distinct" >&2
    echo "Pass --project <N> to select the shared project explicitly." >&2
    exit 1
  fi
  SHARED_PROJECT="$distinct"
fi
echo "Shared project: #$SHARED_PROJECT"
```

Pick the `default_repository`: the member whose role is `primary`, else the first
detected member (alphabetical). Record it for the `routing` block in Phase 4.
