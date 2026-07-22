---
name: pattern-mining
description: Deterministic codebase pattern mining subagent. Discovers existing
  implementation patterns, naming conventions, and structural organization
  before planning. Invoked by feature-planning as a Task subagent — not
  user-facing.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Glob Grep
disable-model-invocation: true
---

# Codebase Pattern Mining

> Utility subagent launched by `/nightgauge-feature-planning` during Phase
> 3.0.5. Not invoked directly by users.

## Purpose

Discover existing codebase patterns before plan production so that:

1. Feature plans reference real conventions instead of inventing new ones
2. Feature-dev receives example implementations to follow
3. Documentation reading is shortlisted based on pattern evidence
4. Similar past issues are surfaced to avoid duplicate work

## Constraints

- **No model invocation** — all logic is deterministic keyword matching
- **Tools**: Glob, Grep, Read only (no Bash for security)
- **Token budget**: < 5,000 tokens total
- **Credibility threshold**: n >= 2 evidence files per pattern
- **Result limit**: First 50 matches per keyword; stop after 3 keywords with no
  results

## Input

Received from feature-planning as Task subagent arguments:

| Field                 | Type   | Description                            |
| --------------------- | ------ | -------------------------------------- |
| `issue_number`        | number | GitHub issue number                    |
| `issue_title`         | string | Issue title (primary keyword source)   |
| `requirements`        | string | Issue requirements/acceptance criteria |
| `documentation_scope` | string | minimal/targeted/standard/extended     |
| `excluded_paths`      | array  | Paths to exclude (from config)         |

## Output

Returns JSON matching `PatternMiningResultSchema` from
`packages/nightgauge-sdk/src/context/schemas/pattern-mining.ts`.

See [docs/PATTERN_MINING.md](../../docs/PATTERN_MINING.md) for full schema
documentation.

---

## Workflow

### Phase 0: Environment Preflight

<!-- include: ../_shared/PREFLIGHT.md -->

---

### Phase 1: Keyword Extraction

Extract semantic keywords from the issue title and requirements. Map each
keyword to the keyword hierarchy.

**Keyword Hierarchy**:

| Domain   | Keywords                                      | File Patterns                                   |
| -------- | --------------------------------------------- | ----------------------------------------------- |
| API      | endpoint, route, handler, REST, HTTP          | `routes/`, `*Handler.ts`, `*Controller.ts`      |
| Service  | service, provider, manager, repository        | `*Service.ts`, `*Provider.ts`, `*Repository.ts` |
| Auth     | auth, login, session, token, permission       | `auth/`, `*Auth*.ts`, `*Session*.ts`            |
| Database | database, schema, migration, model, ORM       | `schema/`, `*Model.ts`, `migrations/`           |
| UI       | component, view, page, widget, render         | `components/`, `views/`, `*.tsx`, `*.vue`       |
| Testing  | test, spec, mock, fixture, assert             | `*.test.ts`, `*.spec.ts`, `__tests__/`          |
| Config   | config, setting, option, environment          | `config/`, `*.config.ts`, `*.yaml`              |
| Pipeline | pipeline, stage, context, handoff, orchestrat | `pipeline/`, `*Orchestrator.ts`, `*Stage.ts`    |
| Skill    | skill, command, plugin, slash                 | `skills/`, `SKILL.md`, `commands/`              |
| Schema   | schema, type, interface, validation, zod      | `schemas/`, `*.schema.ts`, `types/`             |

**Algorithm**:

1. Tokenize the issue title and requirements into words
2. Match each word against the hierarchy keywords (case-insensitive)
3. Collect the corresponding file patterns and content patterns
4. If fewer than 2 keywords match, use the issue title words as literal Grep
   patterns

### Phase 2: Search Execution

For each matched keyword group, execute searches:

#### File Pattern Search (Glob)

```
For each file_pattern from matched keywords:
  Glob: pattern = file_pattern
  Collect matching file paths (max 50 per pattern)
  Exclude paths matching excluded_paths
```

#### Content Search (Grep)

```
For each content_pattern from matched keywords:
  Grep: pattern = content_pattern, output_mode = "files_with_matches"
  Collect matching file paths (max 50 per pattern)
  Exclude paths matching excluded_paths
```

**Early termination**: If 3 consecutive keyword groups produce 0 results, stop
searching. The issue likely uses terminology not present in the codebase.

### Phase 3: Pattern Extraction

For each set of matching files, analyze patterns:

#### Naming Convention Extraction

1. Group matched files by directory
2. Extract common suffixes/prefixes (e.g., `*Service.ts`, `*Controller.ts`)
3. Count frequency of each naming pattern
4. Filter: require n >= 2 files with same pattern

#### Structural Pattern Extraction

1. Analyze directory structure of matched files
2. Identify organizational patterns (e.g., `src/services/`, `src/routes/`)
3. Map file placement conventions
4. Filter: require n >= 2 directories with same structure

#### Implementation Interface Extraction

1. Read matched files (first 50 lines each)
2. Search for export patterns: `export (async )?function`, `export class`,
   `export interface`
3. Extract method signatures and return types
4. Group by signature similarity
5. Filter: require n >= 2 files with similar signatures

#### Idiom Extraction

1. Search for common code patterns in matched files:
   - Builder pattern: `return this` in method chains
   - Factory pattern: `create*`, `build*` functions
   - Error wrapping: `try { ... } catch`
   - Result pattern: `Result<T>`, `Either<L, R>`
2. Count occurrences
3. Filter: require n >= 2 files with same idiom

### Phase 4: Similar Issues Detection

1. List existing plan files: `Glob: .nightgauge/plans/*.md`
2. For each plan file, extract the issue number and title from the filename
3. For each plan, check if the issue's keywords overlap with the plan's filename
   keywords
4. Rank by number of overlapping pattern categories
5. Return top 5 similar issues with relevance scores

### Phase 5: Output Formatting

Assemble the JSON output matching `PatternMiningResultSchema` from
`packages/nightgauge-sdk/src/context/schemas/pattern-mining.ts`.

#### pattern_classifications Field

The `pattern_classifications` object MUST contain **exactly** these four fields
with **exactly** these names (matching `PatternClassificationsSchema`):

| Field                 | Type         | Description                                                                                   |
| --------------------- | ------------ | --------------------------------------------------------------------------------------------- |
| `naming_conventions`  | integer >= 0 | Count of naming convention patterns discovered (e.g., `*Service.ts`, `camelCase` vars)        |
| `structural_patterns` | integer >= 0 | Count of directory/file structure patterns (e.g., `src/routes/`, `src/services/`)             |
| `interface_patterns`  | integer >= 0 | Count of implementation interface/signature patterns (e.g., method signatures, class exports) |
| `idioms`              | integer >= 0 | Count of code idiom patterns (builder, factory, error wrapping, result pattern)               |

> **Critical**: Do NOT use alternate field names such as `naming_conventions_count`,
> `pattern_naming`, `structure_patterns`, or `interface_count`. The Zod schema
> enforces exact field names — any deviation will fail validation in feature-planning.

```json
{
  "patterns_found": [...],
  "similar_issues": [...],
  "pattern_classifications": {
    "naming_conventions": <integer>,
    "structural_patterns": <integer>,
    "interface_patterns": <integer>,
    "idioms": <integer>
  },
  "search_queries_used": [...],
  "coverage_ratio": <matched_files / total_searched>,
  "token_cost_estimate": <estimated_tokens>,
  "recommendations": [...]
}
```

Generate 1-5 recommendations based on the strongest patterns found:

- If a naming pattern has frequency >= 5: "Follow [pattern] convention"
- If a structural pattern exists: "Place new files in [directory] following
  [pattern]"
- If an implementation interface exists: "Implement using [signature pattern]
  from [example file]"
- If a similar issue exists with score >= 0.7: "Review plan from issue #N for
  related approach"

---

## Error Handling

| Condition                  | Action                                  |
| -------------------------- | --------------------------------------- |
| No keywords match          | Return empty patterns_found, 0 coverage |
| Glob/Grep timeout          | Skip remaining queries, return partial  |
| All searches return 0 hits | Return empty results with note          |
| File read fails            | Skip file, continue with remaining      |
| Too many results (> 200)   | Truncate to top 50 per pattern type     |

---

## References

- **Schema**: `packages/nightgauge-sdk/src/context/schemas/pattern-mining.ts`
- **Documentation**: `docs/PATTERN_MINING.md`
- **Config**: `.nightgauge/pattern-mining-config.yaml`
- **Consumer**: `skills/nightgauge-feature-planning/SKILL.md` (Phase 3.0.5)

---

**Author**: nightgauge **License**: Apache-2.0
