### Sprint Iteration Assignment (Optional)

**PURPOSE**: Automatically assign the current sprint iteration when picking up
an issue. Uses `sync-project-iteration.sh` for sprint assignment.

```bash
HOOKS_DIR="${CLAUDE_PLUGIN_ROOT:-claude-plugins/nightgauge}/hooks/lib"
SPRINT_ENABLED=$(yq -r '.project.sprint.enabled // "false"' .nightgauge/config.yaml 2>/dev/null || echo "false")

if [ "$SPRINT_ENABLED" = "true" ]; then
  if [ ! -x "$HOOKS_DIR/sync-project-iteration.sh" ]; then
    echo "WARNING: Optional hook script not found: $HOOKS_DIR/sync-project-iteration.sh. Sprint iteration assignment will be skipped." >&2
  else
    RESULT=$("$HOOKS_DIR/sync-project-iteration.sh" "$ISSUE_NUMBER")
  fi
fi
```

| Config Key                   | Default    | Description                            |
| ---------------------------- | ---------- | -------------------------------------- |
| `project.sprint.enabled`     | `false`    | Enable iteration field integration     |
| `project.sprint.auto_assign` | `false`    | Assign current sprint on issue-pickup  |
| `project.sprint.field_name`  | `"Sprint"` | Name of iteration field in the project |

See [docs/SPRINT_WORKFLOW.md](../../docs/SPRINT_WORKFLOW.md) for complete sprint
setup and workflow documentation.
