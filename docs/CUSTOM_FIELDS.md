# Custom Field Types

This document describes how to configure custom GitHub Project field types
beyond the built-in Status, Priority, and Size fields.

## Overview

Nightgauge supports syncing arbitrary GitHub Project fields using a
label-based mapping system. This enables teams to sync custom fields like:

- **Component/Team assignment** (e.g., `component:frontend`,
  `component:backend`)
- **Customer/Account tags** (e.g., `customer:acme-corp`)
- **Release version targets** (e.g., `release:v2.0`)
- **Effort estimates** (number fields)
- **Target dates** (date fields)

## Quick Start

### 1. Add Custom Field Configuration

Edit `.nightgauge/config.yaml` to add your custom fields:

```yaml
project:
  number: 1
  id: "PVT_..."

  # Built-in fields (unchanged)
  status_field_id: "PVTSSF_..."
  priority_field_id: "PVTSSF_..."
  size_field_id: "PVTSSF_..."

  # Custom fields configuration
  custom_fields:
    - name: "Component"
      field_id: "PVTSSF_..."
      label_prefix: "component"
      type: "single_select"
      mappings:
        frontend: "Frontend"
        backend: "Backend"
        infra: "Infrastructure"
```

### 2. Discover Field IDs

Run the init script with the `--custom-field` flag to discover field IDs:

```bash
# Discover a specific custom field
scripts/init-nightgauge-config.sh --project 1 --custom-field "Component"

# Or query all fields manually
gh project field-list 1 --owner your-org --format json
```

### 3. Create Labels

Create labels matching your configuration:

```bash
# Create component labels
gh label create "component:frontend" --description "Frontend component"
gh label create "component:backend" --description "Backend component"
gh label create "component:infra" --description "Infrastructure component"
```

### 4. Test the Sync

```bash
# Add an issue with a custom field label
gh issue edit 42 --add-label "component:frontend"

# Run add-to-project to sync
claude-plugins/nightgauge/hooks/lib/add-to-project.sh 42
```

## Configuration Schema

### config.yaml Extension

```yaml
project:
  # ... existing configuration ...

  # Custom fields configuration (array)
  custom_fields:
    # Single select field example
    - name: "Component" # GitHub Project field name (exact match)
      field_id: "PVTSSF_..." # GraphQL field ID
      label_prefix: "component" # Creates labels like component:frontend
      type: "single_select" # Field type
      mappings: # Label suffix → Field option value
        frontend: "Frontend" # component:frontend → "Frontend"
        backend: "Backend" # component:backend → "Backend"
        infra: "Infrastructure" # component:infra → "Infrastructure"

    # Text field example (no mappings needed)
    - name: "Customer"
      field_id: "PVTF_..."
      label_prefix: "customer"
      type: "text" # Label suffix synced as text value

    # Number field example
    - name: "Effort"
      field_id: "PVTF_..."
      label_prefix: "effort"
      type: "number" # Labels like effort:5 sync as number 5
```

### config.yaml Custom Fields Extension

Custom fields are added to the `project.fields` section of `config.yaml`:

```yaml
project:
  fields:
    status: { ... }
    priority: { ... }
    size: { ... }
    # Custom fields below
    component:
      id: PVTSSF_...
      type: single_select
      label_prefix: component
      options:
        frontend: abc123
        backend: def456
        infrastructure: ghi789
    customer:
      id: PVTF_...
      type: text
      label_prefix: customer
```

## Supported Field Types

| Type            | Description                   | Label Format    | Example              |
| --------------- | ----------------------------- | --------------- | -------------------- |
| `single_select` | Single selection from options | `prefix:suffix` | `component:frontend` |
| `text`          | Free-form text                | `prefix:value`  | `customer:acme`      |
| `number`        | Numeric value                 | `prefix:N`      | `effort:5`           |

### Single Select Fields

Single select fields require explicit mappings between label suffixes and field
option values:

```yaml
- name: "Component"
  field_id: "PVTSSF_..."
  label_prefix: "component"
  type: "single_select"
  mappings:
    frontend: "Frontend" # Label suffix → Option name
    backend: "Backend"
    api: "Backend" # Multiple labels can map to same option
```

The mapping works bidirectionally:

- **Label → Field**: `component:frontend` label sets field to "Frontend"

### Text Fields

Text fields sync the label suffix as-is:

```yaml
- name: "Customer"
  field_id: "PVTF_..."
  label_prefix: "customer"
  type: "text"
```

- Label `customer:acme-corp` → Field value "acme-corp"
- Label `customer:big-company` → Field value "big-company"

**Note**: Custom field sync is one-directional (labels → fields only).

### Number Fields

Number fields parse the label suffix as a numeric value:

```yaml
- name: "Effort"
  field_id: "PVTF_..."
  label_prefix: "effort"
  type: "number"
```

- Label `effort:5` → Field value 5
- Label `effort:13` → Field value 13

## Label Prefix Validation

### Reserved Prefixes

The `status:` prefix is reserved and cannot be used for custom fields (Status is
a built-in project board field managed by the pipeline).

`priority:` and `size:` labels may exist in GitHub for legacy compatibility, but
these are **not** used for built-in field mapping — Priority and Size are set
directly as project board fields via GraphQL at issue creation. Do not use
`priority:` or `size:` as custom field label prefixes.

### Conflict Detection

Nightgauge validates that label prefixes don't conflict:

1. **No duplicate prefixes**: Each custom field must have a unique prefix
2. **No reserved prefixes**: Cannot use `status`, `priority`, or `size`
3. **Fail fast**: Configuration errors detected at load time

```bash
# This configuration will error:
custom_fields:
  - name: "Team"
    label_prefix: "component"    # Conflict!
  - name: "Component"
    label_prefix: "component"    # Same prefix!
```

Error message:

```
ERROR: Duplicate label prefix 'component' found in custom_fields configuration.
Each custom field must have a unique label_prefix.
```

## Label → Field Sync

Custom fields are synced from labels to project fields by `add-to-project.sh`:

1. Issue created/updated with `component:frontend` label
2. Script reads custom field configuration
3. Maps label suffix "frontend" to option "Frontend"
4. Updates project field via GraphQL

**Note:** Custom fields follow the same pattern as `priority:*` and `size:*`
labels — they are set on the issue as labels and mapped to project fields in one
direction only (labels → fields). Project fields are the source of truth for
status; labels are the source of truth for priority, size, and custom fields.

## Discovery and Setup

### Discovering Field IDs

Use the init script to discover custom field IDs:

```bash
# Discover all fields in a project
scripts/init-nightgauge-config.sh --project 1 --custom-field "Component"

# Output includes field ID and option IDs
```

Or query manually:

```bash
gh project field-list 1 --owner your-org --format json | jq '.fields[] | select(.name == "Component")'
```

### GraphQL Field Types

The init script automatically detects field types:

```graphql
query ($projectId: ID!) {
  node(id: $projectId) {
    ... on ProjectV2 {
      fields(first: 50) {
        nodes {
          ... on ProjectV2SingleSelectField {
            id
            name
            options {
              id
              name
            }
          }
          ... on ProjectV2Field {
            id
            name
            dataType
          }
        }
      }
    }
  }
}
```

## Examples

### Team Assignment

Assign issues to teams using a Component field:

```yaml
custom_fields:
  - name: "Component"
    field_id: "PVTSSF_abc123"
    label_prefix: "team"
    type: "single_select"
    mappings:
      frontend: "Frontend Team"
      backend: "Backend Team"
      platform: "Platform Team"
      devops: "DevOps Team"
```

Labels: `team:frontend`, `team:backend`, `team:platform`, `team:devops`

### Release Targeting

Track target release versions:

```yaml
custom_fields:
  - name: "Release"
    field_id: "PVTSSF_def456"
    label_prefix: "release"
    type: "single_select"
    mappings:
      v1.0: "v1.0"
      v1.1: "v1.1"
      v2.0: "v2.0"
      backlog: "Backlog"
```

Labels: `release:v1.0`, `release:v2.0`, `release:backlog`

### Customer Tracking

Track customer-specific issues:

```yaml
custom_fields:
  - name: "Customer"
    field_id: "PVTF_ghi789"
    label_prefix: "customer"
    type: "text"
```

Labels: `customer:acme-corp`, `customer:big-bank`, `customer:startup-xyz`

### Story Points (Effort)

Track effort estimates:

```yaml
custom_fields:
  - name: "Story Points"
    field_id: "PVTF_jkl012"
    label_prefix: "points"
    type: "number"
```

Labels: `points:1`, `points:2`, `points:3`, `points:5`, `points:8`, `points:13`

## Troubleshooting

### Field Not Syncing

1. **Check field ID**: Verify the field_id matches the GraphQL ID

   ```bash
   gh project field-list 1 --owner your-org --format json
   ```

2. **Check label format**: Labels must match `prefix:suffix` exactly
   - Correct: `component:frontend`
   - Wrong: `component: frontend` (space)
   - Wrong: `component-frontend` (hyphen instead of colon)

3. **Check mappings**: For single_select, verify the mapping exists

   ```yaml
   mappings:
     frontend: "Frontend" # Label suffix "frontend" → Option "Frontend"
   ```

4. **Enable debug logging**:
   ```bash
   NIGHTGAUGE_HOOKS_DEBUG=1 claude-plugins/nightgauge/hooks/lib/add-to-project.sh 42
   ```

### Option ID Not Found

If a single_select option ID is not found:

1. Run init script to refresh mappings:

   ```bash
   scripts/init-nightgauge-config.sh --project 1 --custom-field "Component" --merge
   ```

2. Check the project field options match your configuration

3. Verify option names are exact matches (case-sensitive)

### Prefix Conflict Errors

If you see a prefix conflict error:

1. Check for duplicate prefixes in custom_fields
2. Ensure no custom field uses reserved prefixes (status, priority, size)
3. Remove or rename conflicting fields

## Best Practices

1. **Use descriptive prefixes**: `team:`, `release:`, `customer:` are clear
2. **Keep mappings simple**: Avoid complex suffix-to-option relationships
3. **Document your fields**: Add comments in config.yaml explaining each field
4. **Test before production**: Use `--dry-run` when running sync scripts
5. **Create labels first**: Ensure labels exist before enabling reverse sync

## Related Documentation

- [Configuration Reference](./CONFIGURATION.md) - Full config.yaml schema
- [Architecture](./ARCHITECTURE.md) - Deterministic vs probabilistic design
- [Security](../standards/security.md) - Input validation requirements

---

## Author

nightgauge
