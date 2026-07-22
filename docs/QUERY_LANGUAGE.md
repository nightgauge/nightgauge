# Query Language (GQL)

Nightgauge provides a JQL-style query language for filtering GitHub Project
items. This document describes the syntax, supported fields, and operators.

## Overview

GQL (GitHub Query Language) allows you to filter issues from your GitHub Project
board using powerful boolean expressions and field comparisons. Use it from the
CLI or VSCode extension to quickly find issues matching specific criteria.

### Quick Examples

```
# Find high-priority ready issues
status:ready AND priority:P0

# Find small or extra-small issues
size:S OR size:XS

# Find issues updated in the last week
updated<7d

# Complex query with grouping
(status:ready OR status:backlog) AND priority:P0 AND NOT type:epic
```

## Usage

### VSCode Extension

1. Open Command Palette (`Cmd+Shift+P` / `Ctrl+Shift+P`)
2. Run "Nightgauge: Query Project Items"
3. Enter your query in the input box
4. View results in the Query Results tree view

### CLI

```bash
# Execute a query
npx @nightgauge/sdk query "status:ready AND priority:P0"

# Output as JSON
npx @nightgauge/sdk query "size:M OR size:L" --format json

# Export to CSV
npx @nightgauge/sdk query "updated<7d" --export results.csv

# Save a query for reuse
npx @nightgauge/sdk query "status:ready AND priority:P0" --save "sprint-backlog"

# List saved queries
npx @nightgauge/sdk query --list

# Run a saved query
npx @nightgauge/sdk query --run "sprint-backlog"
```

## Query Syntax

### Basic Comparison

The simplest query compares a field to a value:

```
field:value
field=value
```

Both `:` and `=` are equivalent for equality comparison.

### Operators

| Operator | Description           | Example        |
| -------- | --------------------- | -------------- |
| `:`      | Equals                | `status:ready` |
| `=`      | Equals                | `priority=P0`  |
| `!=`     | Not equals            | `status!=done` |
| `>`      | Greater than          | `size>M`       |
| `<`      | Less than             | `updated<7d`   |
| `>=`     | Greater than or equal | `size>=L`      |
| `<=`     | Less than or equal    | `number<=100`  |
| `~`      | Wildcard match        | `title~*auth*` |

### Boolean Operators

Combine comparisons using boolean operators:

| Operator | Description | Example                        |
| -------- | ----------- | ------------------------------ |
| `AND`    | Both true   | `status:ready AND priority:P0` |
| `OR`     | Either true | `size:S OR size:XS`            |
| `NOT`    | Negation    | `NOT type:epic`                |

### Operator Precedence

1. `NOT` (highest)
2. `AND`
3. `OR` (lowest)

Use parentheses to override precedence:

```
# Without parentheses: a OR (b AND c)
a:1 OR b:2 AND c:3

# With parentheses: (a OR b) AND c
(a:1 OR b:2) AND c:3
```

### Quoted Values

Use quotes for values containing spaces or special characters:

```
title:"Fix authentication bug"
assignee:"john doe"
```

## Supported Fields

### Status Fields

| Field    | Type          | Operators      | Values                                                 |
| -------- | ------------- | -------------- | ------------------------------------------------------ |
| `status` | single_select | `:`, `=`, `!=` | `ready`, `in-progress`, `in-review`, `done`, `backlog` |

### Priority Fields

| Field      | Type          | Operators      | Values                 |
| ---------- | ------------- | -------------- | ---------------------- |
| `priority` | single_select | `:`, `=`, `!=` | `P0`, `P1`, `P2`, `P3` |

### Size Fields

| Field  | Type          | Operators                            | Values                    |
| ------ | ------------- | ------------------------------------ | ------------------------- |
| `size` | single_select | `:`, `=`, `!=`, `>`, `<`, `>=`, `<=` | `XS`, `S`, `M`, `L`, `XL` |

Size comparisons use the order: XS < S < M < L < XL

### Label Fields

| Field       | Type  | Operators      | Values                         |
| ----------- | ----- | -------------- | ------------------------------ |
| `labels`    | array | `:`, `=`, `!=` | Any label text                 |
| `type`      | label | `:`, `=`, `!=` | `bug`, `feature`, `epic`, etc. |
| `component` | label | `:`, `=`, `!=` | Component names                |

Label matching is case-insensitive and supports partial matching. For example,
`labels:bug` matches issues with a `type:bug` label.

### Text Fields

| Field      | Type | Operators           | Values            |
| ---------- | ---- | ------------------- | ----------------- |
| `title`    | text | `:`, `=`, `!=`, `~` | Any text          |
| `assignee` | text | `:`, `=`, `!=`      | Username or `@me` |

### Numeric Fields

| Field    | Type   | Operators                            | Values       |
| -------- | ------ | ------------------------------------ | ------------ |
| `number` | number | `:`, `=`, `!=`, `>`, `<`, `>=`, `<=` | Issue number |

### Date Fields

| Field     | Type | Operators            | Values                |
| --------- | ---- | -------------------- | --------------------- |
| `updated` | date | `<`, `>`, `<=`, `>=` | Relative dates or ISO |
| `created` | date | `<`, `>`, `<=`, `>=` | Relative dates or ISO |

#### Date Values

Relative dates:

- `7d` - 7 days ago
- `14d` - 14 days ago
- `30d` - 30 days ago

ISO dates:

- `2024-01-15` - Specific date
- `2024-01-15T10:30:00Z` - Specific datetime

Examples:

```
# Issues updated in the last week
updated<7d

# Issues created more than 30 days ago
created>30d
```

## Wildcard Matching

Use `~` for wildcard matching on text fields:

| Pattern   | Matches                            |
| --------- | ---------------------------------- |
| `auth*`   | Starts with "auth"                 |
| `*bug`    | Ends with "bug"                    |
| `*auth*`  | Contains "auth"                    |
| `fix*bug` | Starts with "fix", ends with "bug" |

Example:

```
title~*authentication*
title~Fix*
```

## Special Values

### @me

Use `@me` to match the current authenticated user:

```
assignee:@me
```

### Null/Undefined

Fields with null or undefined values:

- Match with `!=` (e.g., `priority!=P0` matches issues with null priority)
- Do not match with `=` or `:` (e.g., `priority:P0` excludes null priorities)

## Saved Queries

### Saving Queries

From VSCode:

1. Run a query
2. Open Command Palette
3. Run "Nightgauge: Save Query"
4. Enter a name and optional description

From CLI:

```bash
npx @nightgauge/sdk query "status:ready AND priority:P0" --save "high-priority-ready"
```

### Managing Saved Queries

Saved queries are stored in `.nightgauge/saved-queries.yaml`:

```yaml
version: "1.0"
queries:
  - name: high-priority-ready
    query: "status:ready AND priority:P0"
    description: "High priority issues ready for pickup"
    createdAt: "2024-01-15T10:00:00Z"
    lastUsedAt: "2024-01-20T14:30:00Z"
    runCount: 5
```

### VSCode Commands

| Command                            | Description                  |
| ---------------------------------- | ---------------------------- |
| `Nightgauge: Query Project Items`  | Run a query                  |
| `Nightgauge: Save Query`           | Save current query           |
| `Nightgauge: Load Saved Query`     | Load and run a saved query   |
| `Nightgauge: Delete Saved Query`   | Delete a saved query         |
| `Nightgauge: Manage Saved Queries` | Import/export/manage queries |

## Error Messages

### Common Errors

| Error                               | Cause                        |
| ----------------------------------- | ---------------------------- |
| "Unknown field: xyz"                | Invalid field name           |
| "Operator '>' not valid for status" | Invalid operator for field   |
| "Expected value after operator"     | Missing value after `:`      |
| "Missing closing parenthesis"       | Unbalanced parentheses       |
| "Query too long (max 2000 chars)"   | Query exceeds maximum length |

### Validation

Queries are validated before execution:

- Field names must be valid
- Operators must be valid for field types
- Values must be appropriate for the field
- Parentheses must be balanced

## Examples

### Basic Queries

```
# All ready issues
status:ready

# High priority bugs
priority:P0 AND type:bug

# Small issues
size:S OR size:XS
```

### Complex Queries

```
# Ready or backlog high-priority issues, excluding epics
(status:ready OR status:backlog) AND priority:P0 AND NOT type:epic

# My assigned issues that are in progress
assignee:@me AND status:in-progress

# Recently updated medium or large issues
updated<7d AND (size:M OR size:L)

# Issues with authentication in the title
title~*auth*
```

### Sprint Planning Queries

```
# Sprint candidates: ready, sized, prioritized
status:ready AND size<=M AND (priority:P0 OR priority:P1)

# Blocked or needs-info issues
status:backlog AND (labels:blocked OR labels:needs-info)

# Unassigned ready issues
status:ready AND assignee!=@me
```

## Grammar Reference

The complete grammar in EBNF notation:

```ebnf
query           = expression ;
expression      = or_expr ;
or_expr         = and_expr ( "OR" and_expr )* ;
and_expr        = not_expr ( "AND" not_expr )* ;
not_expr        = "NOT" not_expr | primary ;
primary         = comparison | "(" expression ")" ;
comparison      = field operator value ;
field           = identifier ;
operator        = ":" | "=" | "!=" | ">" | "<" | ">=" | "<=" | "~" ;
value           = identifier | quoted_string | "@me" | relative_date ;
identifier      = [a-zA-Z][a-zA-Z0-9_-]* ;
quoted_string   = '"' [^"]* '"' ;
relative_date   = [0-9]+ "d" ;
```

## See Also

- [Configuration Reference](./CONFIGURATION.md) - Saved queries configuration
- [Architecture](./ARCHITECTURE.md) - System design overview
- [SDK Documentation](../packages/nightgauge-sdk/README.md) - Programmatic usage

---

## Author

nightgauge
