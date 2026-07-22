### Date Field Automation (Optional)

**PURPOSE**: Automatically set GitHub Project date fields to keep the Roadmap
view current. Call date automation hooks: set Start date to today, set Target
date from milestone if unset.

| Config Key           | Default | Description                      |
| -------------------- | ------- | -------------------------------- |
| `project.auto_dates` | `false` | Enable date automation           |
| `project.number`     | -       | GitHub Project number (required) |

**Steps** (skip entire phase if `auto_dates` is not `true`):

1. Get project number and owner from config
2. Find the issue's project item ID via `gh project item-list`
3. Get date field IDs dynamically via `gh project field-list`
4. Set Start date to today:
   `gh project item-edit --field-id "$START_DATE_FIELD" --date "$TODAY"`
5. If Target date is unset, derive from milestone `dueOn`:
   `gh project item-edit --field-id "$TARGET_DATE_FIELD" --date "$MILESTONE_DUE"`
