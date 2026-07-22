# GQL Query Language — Implementation Guide

Issue: #138 — JQL-style query language for filtering project items.

## Overview

GQL (GitHub Query Language) is a JQL-inspired query language for filtering
GitHub Project board items. It supports field comparisons, boolean logic,
operator precedence, and parenthesized grouping.

## Architecture

The implementation follows a three-stage pipeline:

```
Query String → Lexer → Token[] → Parser → AST → Evaluator → QueryResult
```

### SDK Layer (`packages/nightgauge-sdk/src/query/`)

| File           | Purpose                                           |
| -------------- | ------------------------------------------------- |
| `types.ts`     | Token, AST node, and result type definitions      |
| `errors.ts`    | Error class hierarchy (lexer, parser, evaluation) |
| `schemas.ts`   | Field definitions, Zod schemas, validation fns    |
| `lexer.ts`     | Tokenizer — string → Token[]                      |
| `parser.ts`    | Recursive descent parser — Token[] → AST          |
| `evaluator.ts` | AST evaluator — AST × Issue[] → QueryResult       |
| `index.ts`     | Public API barrel exports                         |

### VSCode Extension Layer

| File                                | Purpose                                    |
| ----------------------------------- | ------------------------------------------ |
| `services/QueryService.ts`          | State machine for query execution          |
| `services/SavedQueriesService.ts`   | Saved query CRUD + file watcher            |
| `commands/queryProjectItems.ts`     | QuickPick UI for query input               |
| `commands/saveQuery.ts`             | Save current/arbitrary queries             |
| `commands/loadSavedQuery.ts`        | Load, delete, manage saved queries         |
| `views/QueryResultsTreeProvider.ts` | Tree view for query results                |
| `types/QueryTypes.ts`               | Extension-specific types, built-in queries |

### CLI Layer

| File                    | Purpose                         |
| ----------------------- | ------------------------------- |
| `cli/commands/query.ts` | CLI command with format options |

## Query Language Reference

### Fields

| Field       | Type          | Operators                  | Notes                                |
| ----------- | ------------- | -------------------------- | ------------------------------------ |
| `status`    | single_select | `:` `!=`                   | ready, in-progress, done, backlog    |
| `priority`  | single_select | `:` `!=`                   | P0, P1, P2, P3                       |
| `size`      | ordinal       | `:` `!=` `>` `<` `>=` `<=` | XS < S < M < L < XL                  |
| `assignee`  | string        | `:` `!=`                   | GitHub username or `@me`             |
| `title`     | string        | `:` `!=` `~`               | `~` supports `*` wildcards           |
| `number`    | number        | `:` `!=` `>` `<` `>=` `<=` |                                      |
| `labels`    | array         | `:` `!=`                   | Partial match on label text          |
| `type`      | label_prefix  | `:` `!=`                   | Matches `type:` prefixed labels      |
| `component` | label_prefix  | `:` `!=`                   | Matches `component:` prefixed labels |
| `updated`   | date          | `<` `>` `<=` `>=`          | ISO dates or relative (`7d`)         |
| `created`   | date          | `<` `>` `<=` `>=`          | ISO dates or relative (`7d`)         |

### Operators

- **Equality** `:` — exact match (case-insensitive)
- **Not equal** `!=` — negated match
- **Comparison** `>` `<` `>=` `<=` — numeric, size ordinal, or date comparison
- **Wildcard** `~` — glob-style pattern with `*` (use quoted values)

### Boolean Operators

Precedence (highest to lowest): `NOT` → `AND` → `OR`

```
status:ready AND priority:P0 OR priority:P1
→ parsed as: (status:ready AND priority:P0) OR priority:P1
```

Use parentheses to override:

```
status:ready AND (priority:P0 OR priority:P1)
```

### Example Queries

```
status:ready                              # Simple equality
status:ready AND priority:P0              # AND condition
priority:P0 OR priority:P1               # OR condition
NOT status:done                           # Negation
title~"auth*"                             # Wildcard match
updated<7d                                # Updated within 7 days
(status:ready OR status:in-progress) AND priority:P0   # Grouped
number>100 AND size<=M                    # Numeric + ordinal
```

## Implementation Details

### Lexer (`lexer.ts`)

The `Lexer` class scans the input string character-by-character:

- **Identifiers** become `FIELD` tokens when followed by an operator character,
  or `VALUE` tokens otherwise
- **Keywords** (AND, OR, NOT) are recognized case-insensitively from identifiers
- **Operators** are single or double characters: `:` `=` `!=` `<` `>` `<=` `>=` `~`
- **Quoted strings** support double and single quotes with backslash escaping
- **Special values**: `@me`, hyphenated identifiers, numeric values
- **Safety**: rejects control characters, enforces MAX_QUERY_LENGTH (2000)

### Parser (`parser.ts`)

Recursive descent parser implementing this grammar:

```
query     → or_expr
or_expr   → and_expr ("OR" and_expr)*
and_expr  → not_expr ("AND" not_expr)*
not_expr  → "NOT" not_expr | atom
atom      → "(" or_expr ")" | comparison
comparison → FIELD OPERATOR VALUE
```

The parser validates:

- Field names against `ALLOWED_FIELDS`
- Operators against each field's `allowedOperators`
- Returns `ParseResult` with `ast` (nullable) and `errors` array

### Evaluator (`evaluator.ts`)

`evaluateNode(ast, issue)` recursively evaluates:

- **Comparison nodes**: extracts field value from issue, applies operator
- **Binary nodes** (AND/OR): short-circuit evaluation
- **Unary nodes** (NOT): negation

Field value extraction handles:

- `labels` — array containment check (partial match)
- `type` / `component` — extracts from prefixed labels (`type:bug` → `bug`)
- `size` — ordinal comparison using `SIZE_ORDER` mapping
- `updated` / `created` — date parsing (ISO or relative like `7d`)
- `number` — numeric comparison
- Null fields return `false` for equality, `true` for inequality

### QueryService (`QueryService.ts`)

State machine with transitions: `idle → parsing → executing → complete|error`

- Fires `onQueryStateChanged` event on every transition
- Fetches issues from `ProjectBoardService` across all 4 status categories
- Maintains query history with deduplication
- `validate()` delegates to SDK's `parse()` for real-time feedback

### SavedQueriesService (`SavedQueriesService.ts`)

Persists queries to `.nightgauge/saved-queries.yaml`:

- File watcher auto-reloads on external changes
- Merges built-in queries (read-only) with user queries
- CRUD: `save()`, `delete()`, `rename()`, `recordUsage()`
- Fires `onQueriesChanged` event after mutations

## Test Coverage

### SDK Tests (185 tests)

| Test File             | Tests | Coverage Area                             |
| --------------------- | ----- | ----------------------------------------- |
| `lexer.test.ts`       | 28    | Tokenization, operators, quotes, errors   |
| `parser.test.ts`      | 26    | Parsing, precedence, parentheses, errors  |
| `evaluator.test.ts`   | 38    | All operators, field types, boolean logic |
| `integration.test.ts` | 17    | End-to-end pipeline, acceptance criteria  |
| `edge-cases.test.ts`  | 76    | Limits, schemas, dates, errors, unicode   |

### VSCode Extension Tests (70 tests)

| Test File                          | Tests | Coverage Area                    |
| ---------------------------------- | ----- | -------------------------------- |
| `QueryService.test.ts`             | 17    | State machine, events, history   |
| `SavedQueriesService.test.ts`      | 15    | CRUD, file watcher, built-ins    |
| `QueryResultsTreeProvider.test.ts` | 24    | All states, icons, tree contract |
| `QueryTypes.test.ts`               | 14    | Constants, conversion functions  |

## Adding New Fields

1. Add field name to `FieldName` type in `types.ts`
2. Add definition to `FIELD_DEFINITIONS` in `schemas.ts` with type and operators
3. Add field extraction logic in `evaluator.ts` `getFieldValue()`
4. Add field to `QueryableIssue` type if needed
5. Update `toQueryableIssue()` in `QueryTypes.ts` for extension conversion
