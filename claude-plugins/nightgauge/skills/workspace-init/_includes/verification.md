# Verification & Summary (Phases 5–6)

Procedural detail for proving the manifest works and reporting the result.

## Phase 5 — Verify via sync-payload

The manifest is correct when `nightgauge workspace sync-payload`, run from
the workspace root, returns a non-empty `repos` array containing every detected
member. A non-empty payload is the exact signal the extension's
`WorkspaceManager` consumes to render the shared board (AC #1).

```bash
cd "$WORKSPACE_ROOT"
PAYLOAD="$(nightgauge workspace sync-payload 2>/dev/null)"
REPO_COUNT="$(echo "$PAYLOAD" | jq '.repos | length')"
DISPLAY_NAME="$(echo "$PAYLOAD" | jq -r '.workspace.display_name // ""')"

echo "sync-payload: repos=$REPO_COUNT display_name=\"$DISPLAY_NAME\""

if [ "$REPO_COUNT" -lt "${#MEMBERS[@]}" ]; then
  echo "ERROR: sync-payload returned $REPO_COUNT repos but ${#MEMBERS[@]} members were detected." >&2
  echo "A member's .nightgauge/config.yaml likely resolves an empty owner/repo." >&2
  echo "Inspect the payload and re-run repo-init in the offending member:" >&2
  echo "$PAYLOAD" >&2
  exit 1
fi
```

Assert the `display_name` has **no embedded quote characters** (the #3859
quote-strip heal — AC #3):

```bash
case "$DISPLAY_NAME" in
  *\"*|*\'*)
    echo "WARNING: display_name contains quote characters: $DISPLAY_NAME" >&2
    echo "Update the binary (sync_payload.go stripQuotes) — see #3859." >&2
    ;;
esac
```

Run `workspace doctor` and surface fatal validation errors (non-fatal warnings
do not block):

```bash
nightgauge workspace doctor || \
  echo "Note: workspace doctor reported issues (above) — review before relying on autonomous routing."
```

## Phase 6 — Summary report

Print a clear, human-readable summary:

```
── Workspace Init Complete ─────────────────────────────────────────
  Workspace:   <WORKSPACE_NAME>
  Members:     <N> registered (<member-a>, <member-b>, ...)
  Project:     #<SHARED_PROJECT> (shared)
  Manifest:    .vscode/nightgauge-workspace.yaml
  Verified:    sync-payload → <REPO_COUNT> repos, display_name "<DISPLAY_NAME>"

Next step:
  Open <WORKSPACE_ROOT> in VSCode — the Repositories view now renders the
  shared board across all member repos.
```

If this was a `--dry-run`, the summary instead states that no file was written
and shows the previewed manifest path.
