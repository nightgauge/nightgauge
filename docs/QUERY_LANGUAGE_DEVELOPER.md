# GQL Query Language — Developer Guide

This document describes the architecture and implementation of the GQL (GitHub
Query Language) query engine in `packages/nightgauge-sdk/src/query/`.

For **user documentation** (syntax reference, examples, saved queries), see
[QUERY_LANGUAGE.md](QUERY_LANGUAGE.md).

---

## Architecture Overview

The query engine follows a classic compiler pipeline:

```
Query String
    │
    ▼
┌─────────┐
│  Lexer  │  Tokenize: string → Token[]
└─────────┘
    │  Token[]
    ▼
┌─────────┐
│  Parser │  Parse: Token[] → AST
└─────────┘
    │  ASTNode
    ▼
┌───────────┐
│ Evaluator │  Evaluate: AST × Issue[] → QueryResult
└───────────┘
    │  QueryResult
    ▼
Matched Issues
```

### Source Files

| File           | Purpose                                       |
| -------------- | --------------------------------------------- |
| `lexer.ts`     | Tokenizes query strings into `Token[]`        |
| `parser.ts`    | Parses token streams into AST nodes           |
| `evaluator.ts` | Evaluates AST against issue data              |
| `types.ts`     | TypeScript type definitions (no runtime code) |
| `schemas.ts`   | Zod validation schemas + field definitions    |
| `errors.ts`    | Error class hierarchy                         |
| `index.ts`     | Public re-exports                             |

---

## Lexer

**File**: `packages/nightgauge-sdk/src/query/lexer.ts`

The `Lexer` class tokenizes a query string into a flat `Token[]` array. Each
token has a `type`, `value`, and `position` (byte offset for error reporting).

### Token Types

| Type       | Example                  | Description                          |
| ---------- | ------------------------ | ------------------------------------ |
| `FIELD`    | `status`, `priority`     | Field name (followed by an operator) |
| `OPERATOR` | `:`, `!=`, `>=`          | Comparison operator                  |
| `VALUE`    | `ready`, `"hello world"` | Comparison value                     |
| `AND`      | `AND`                    | Boolean AND keyword                  |
| `OR`       | `OR`                     | Boolean OR keyword                   |
| `NOT`      | `NOT`                    | Boolean NOT keyword                  |
| `LPAREN`   | `(`                      | Left parenthesis                     |
| `RPAREN`   | `)`                      | Right parenthesis                    |
| `EOF`      | _(end)_                  | End of input sentinel                |

### How Field vs Value Is Determined

The lexer distinguishes `FIELD` from `VALUE` tokens based on lookahead:

```typescript
// After reading an identifier, skip whitespace then peek
this.skipWhitespace();
const isField = isOperatorStart(this.current());
```

If the identifier is immediately followed by an operator character (`:=!><~`),
it is a `FIELD`. Otherwise it is a `VALUE`. This covers cases like:

- `status:ready` → `FIELD "status"`, `OP ":"`, `VALUE "ready"`
- `NOT status:done` → `NOT`, `FIELD "status"`, `OP ":"`, `VALUE "done"`

### Security Constraints

- **Max query length**: 2000 characters (throws `QueryTooLongError`). Prevents
  DoS through excessive parsing time.
- **Invalid characters**: Null bytes and control characters throw
  `InvalidCharacterError`. Whitespace is allowed.

---

## Parser

**File**: `packages/nightgauge-sdk/src/query/parser.ts`

The `Parser` class implements a recursive descent parser with the following
grammar (precedence low → high):

```
query       := or_expr
or_expr     := and_expr ('OR' and_expr)*
and_expr    := not_expr ('AND' not_expr)*
not_expr    := 'NOT'? atom
atom        := '(' query ')' | comparison
comparison  := field operator value
```

This gives **OR < AND < NOT** precedence, matching Jira JQL.

### Example: Precedence

```
status:ready AND priority:P0 OR priority:P1

Parsed as:
    OR
   /  \
 AND  priority:P1
 / \
status:ready  priority:P0
```

Parentheses override:

```
status:ready AND (priority:P0 OR priority:P1)

Parsed as:
    AND
   /   \
status:ready   OR
              /  \
       priority:P0  priority:P1
```

### Error Handling

The parser returns a `ParseResult` with `{ ast, errors }`. On failure, `ast` is
`null` and `errors` contains position-annotated `QueryError` objects. The parser
does not panic — it recovers where possible.

```typescript
const result = parse("status:badop priority:P0");
// { ast: null, errors: [{ message: '...', position: 7, length: 1 }] }
```

---

## Evaluator

**File**: `packages/nightgauge-sdk/src/query/evaluator.ts`

The evaluator takes a parsed `ASTNode` and a list of `QueryableIssue[]` objects,
returning a `QueryResult`.

### Field Extraction

`getFieldValue(issue, field)` maps field names to issue properties:

| Field       | Issue Property                            | Notes               |
| ----------- | ----------------------------------------- | ------------------- |
| `status`    | `issue.status`                            | String or null      |
| `priority`  | `issue.priority`                          | "P0"–"P3" or null   |
| `size`      | `issue.size`                              | "XS"–"XL" or null   |
| `assignee`  | `issue.assignee`                          | String or null      |
| `title`     | `issue.title`                             | Always present      |
| `number`    | `issue.number.toString()`                 | Converted to string |
| `updated`   | `issue.updatedAt`                         | ISO string or null  |
| `created`   | `issue.createdAt`                         | ISO string or null  |
| `labels`    | `issue.labels`                            | String array        |
| `component` | labels filtered by `component:` prefix    | String array        |
| `type`      | first label with `type:` prefix, stripped | String or null      |

### Null Field Handling

When a field is `null` (not set on the issue):

- `=` and `:` operators → `false` (null doesn't equal anything)
- `!=` operator → `true` (null is not equal to the query value)
- All other operators → `false`

### Array Field Operators

For `labels` and `component` (which return `string[]`):

- `:` / `=` → any array element contains the query value (case-insensitive
  substring match)
- `!=` → no array element matches
- `~` → any array element matches the wildcard pattern

### Date Comparison

Date fields (`updated`, `created`) support two value formats:

- **Relative**: `7d`, `30d`, `365d` — means "N days ago from now"
- **ISO**: `2026-01-15` — literal date

Comparison semantics:

```
updated>7d  → issue.updatedAt > (now - 7 days)  → updated recently
updated<7d  → issue.updatedAt < (now - 7 days)  → not updated recently
```

### Size Comparison

`size` field uses ordinal ordering:

```
XS(1) < S(2) < M(3) < L(4) < XL(5)
```

`size>M` matches `L` and `XL`.

### Wildcard Matching

The `~` operator converts the value to a regex where `*` → `.*`:

```typescript
const escapedPattern = pattern.replace(/[.+?^${}()|[\]\\]/g, "\\$&").replace(/\*/g, ".*");
const regex = new RegExp(`^${escapedPattern}$`, "i");
```

Matching is case-insensitive and anchored to the full string.

### Timeout Support

The `evaluate()` function accepts an optional `timeoutMs` parameter. When set,
it checks the deadline after each issue evaluation. If the deadline passes, it
throws an `EvaluationError` with a descriptive message.

```typescript
// Timeout after 30 seconds
const result = executeQuery("status:ready", issues, 30000);
```

---

## Adding a New Field

To add a new queryable field (e.g., `milestone`):

1. **`types.ts`**: Add `"milestone"` to the `FieldName` union.

2. **`schemas.ts`**: Add to `ALLOWED_FIELDS`, `FIELD_DEFINITIONS`, and
   `FieldNameSchema`:

   ```typescript
   milestone: {
     name: "milestone",
     type: "text",
     allowedOperators: [":", "!=", "~"],
   },
   ```

3. **`evaluator.ts`**: Add to the `getFieldValue` switch:

   ```typescript
   case "milestone":
     return issue.milestone ?? null;
   ```

4. **`types.ts`** (`QueryableIssue`): Add the property:

   ```typescript
   milestone?: string;
   ```

5. **Tests**: Add cases in `lexer.test.ts`, `parser.test.ts`, and
   `evaluator.test.ts` for the new field.

---

## Adding a New Operator

To add a new operator (e.g., `^=` for starts-with):

1. **`types.ts`**: Add `"^="` to `ComparisonOperator`.

2. **`schemas.ts`**: Add `"^="` to `ComparisonOperatorSchema` and to the
   `allowedOperators` arrays for applicable fields.

3. **`lexer.ts`**: Handle the new operator character(s) in `readOperator()`.

4. **`evaluator.ts`**: Add a case in `evaluateOperator()`:

   ```typescript
   case "^=":
     return fieldValue.toLowerCase().startsWith(queryValue.toLowerCase());
   ```

5. **Tests**: Add cases in all relevant test files.

---

## Testing Patterns

### Unit Test Structure

Tests live alongside source in `__tests__/`:

```
src/query/
├── __tests__/
│   ├── lexer.test.ts          # Tokenization tests
│   ├── parser.test.ts         # Basic parsing + error cases
│   ├── parser-complex.test.ts # Deep nesting + precedence stress
│   ├── evaluator.test.ts      # Per-operator evaluation
│   ├── evaluator-real-data.test.ts  # Sprint dataset integration
│   ├── edge-cases.test.ts     # Schemas, errors, unicode, limits
│   └── integration.test.ts    # End-to-end pipeline tests
```

### Helper Pattern

All evaluator tests use a `createIssue()` helper for concise test data:

```typescript
function createIssue(overrides: Partial<QueryableIssue> = {}): QueryableIssue {
  return {
    number: 1,
    title: "Test issue",
    labels: [],
    priority: null,
    size: null,
    url: "https://github.com/test/repo/issues/1",
    ...overrides,
  };
}
```

### Parsing Helper

```typescript
function getAST(query: string): ASTNode {
  const result = parse(query);
  if (!result.ast) {
    throw new Error(`Failed to parse: ${result.errors.map((e) => e.message).join(", ")}`);
  }
  return result.ast;
}
```

---

## SDK Export Surface

The query module exports from `packages/nightgauge-sdk/src/index.ts`:

```typescript
// Functions
export { tokenize } from "./query/lexer.js";
export { parse, validate, isValid } from "./query/parser.js";
export { evaluate, evaluateNode, evaluateWithTimeout, executeQuery } from "./query/evaluator.js";

// Types
export type {
  Token,
  TokenType,
  FieldName,
  ComparisonOperator,
  ASTNode,
  QueryableIssue,
  QueryResult,
  SavedQuery,
  SavedQueriesFile,
  QueryPaginationOptions,
  PaginatedQueryResult,
  QueryExecutionOptions,
} from "./query/types.js";

// Schemas + validators
export {
  SavedQuerySchema,
  SavedQueriesFileSchema,
  FIELD_DEFINITIONS,
  ALLOWED_FIELDS,
  isValidField,
  getFieldDefinition,
  isValidOperatorForField,
  getAllowedOperators,
} from "./query/schemas.js";
```

---

## VSCode Integration

The query engine is consumed by the VSCode extension through two services:

### QueryService (`src/services/QueryService.ts`)

Bridges the SDK with VSCode UI state:

- Maintains `QueryContext` (state machine: idle → parsing → executing → complete/error)
- Fetches issues from `ProjectBoardService` and converts them to `QueryableIssue[]`
- Fires `onQueryStateChanged` and `onQueryComplete` events for tree view updates
- Persists query history to workspace state

### QueryResultsTreeProvider (`src/views/QueryResultsTreeProvider.ts`)

Implements `vscode.TreeDataProvider<BaseTreeItem>` and listens to
`QueryService.onQueryStateChanged`:

- **idle** → shows "Run a query" prompt
- **parsing/executing** → shows spinner
- **error** → shows error message with retry action
- **complete** → shows `QueryResultSummaryItem` + `QueryResultIssueItem[]`

Tree item classes live in `src/views/QueryResultsTreeItem.ts`:

| Class                     | Purpose                                |
| ------------------------- | -------------------------------------- |
| `QueryResultSummaryItem`  | Match count + execution time header    |
| `QueryResultIssueItem`    | Single matched issue                   |
| `QueryResultGroupItem`    | Group header (status/priority buckets) |
| `QueryResultActionItem`   | Status messages and prompts            |
| `QueryResultErrorItem`    | Error display with retry command       |
| `QueryResultLoadMoreItem` | Pagination "Load More" control         |

---

## Performance Characteristics

| Dataset size | Typical evaluation time |
| ------------ | ----------------------- |
| 50 items     | <1ms                    |
| 500 items    | <10ms                   |
| 5,000 items  | <100ms                  |
| 50,000 items | ~1s (use timeout)       |

Query timeout (default: none, recommended: 30,000ms) protects against
runaway evaluation on large corpora.

Pagination (`QueryPaginationOptions`) allows incremental display of large result
sets without loading all items into the tree view at once.
