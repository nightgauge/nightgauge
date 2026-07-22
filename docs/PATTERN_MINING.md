# Pattern Mining

**Feature**: Codebase pattern mining subagent for the feature-planning pipeline
stage.

**Issue**: #20 **Status**: Implemented **Version**: 1.0.0

---

## Problem Statement

The feature-planning skill reads documentation and explores code independently,
potentially duplicating existing patterns or reimplementing functionality already
present in the codebase. This wastes tokens, time, and produces suboptimal
designs.

---

## Solution Overview

A **pattern mining subagent** (`nightgauge-pattern-mining`) runs as a `Task`
during feature-planning (Phase 3.0.5) to discover existing codebase patterns
before the plan is produced. It uses deterministic keyword matching via Glob and
Grep — no model invocation.

```
feature-planning
    │
    ├─ Phase 2: Complexity Assessment
    │
    ├─ Phase 3.0.5: Pattern Mining (NEW)
    │   ├─ Extract keywords from issue requirements
    │   ├─ Search codebase for matching implementations (Glob + Grep)
    │   ├─ Extract and classify patterns (naming, structural, interface, idiom)
    │   ├─ Find similar issues via pattern overlap
    │   └─ Return JSON: { patterns_found[], similar_issues[], recommendations[] }
    │
    ├─ Phase 3: Documentation-First Analysis
    │   └─ Uses pattern results to prioritize docs to read
    │
    └─ Phase 5: Write Planning Context
        └─ Includes pattern_mining_results in planning-{N}.json
```

---

## Pattern Types

| Type                       | Category Examples       | Description                                               |
| -------------------------- | ----------------------- | --------------------------------------------------------- |
| `naming_convention`        | file_naming, var_naming | File, variable, and function naming patterns              |
| `structural`               | directory_organization  | Directory structure, file placement, module organization  |
| `implementation_interface` | service_pattern         | Method signatures, return types, class/interface patterns |
| `idiom`                    | builder, factory        | Recurring code idioms and design patterns                 |

---

## Keyword Matching Strategy

Pattern mining uses a **semantic keyword hierarchy** to generate targeted search
queries from issue requirements.

### Keyword Hierarchy

| Domain   | Keywords                                      | Search Patterns                                 |
| -------- | --------------------------------------------- | ----------------------------------------------- |
| API      | endpoint, route, handler, REST, HTTP          | `routes/`, `*Handler.ts`, `*Controller.ts`      |
| Service  | service, provider, manager, repository        | `*Service.ts`, `*Provider.ts`, `*Repository.ts` |
| Auth     | auth, login, session, token, permission       | `auth/`, `*Auth*.ts`, `*Session*.ts`            |
| Database | database, schema, migration, model, ORM       | `schema/`, `*Model.ts`, `migrations/`           |
| UI       | component, view, page, widget, render         | `components/`, `views/`, `*.tsx`, `*.vue`       |
| Testing  | test, spec, mock, fixture, assert             | `*.test.ts`, `*.spec.ts`, `__tests__/`          |
| Config   | config, setting, option, environment          | `config/`, `*.config.ts`, `*.yaml`              |
| Pipeline | pipeline, stage, context, handoff, orchestrat | `pipeline/`, `*Orchestrator.ts`, `*Stage.ts`    |

### Search Algorithm

1. **Extract keywords** from issue title, description, and acceptance criteria
2. **Map keywords** to search patterns via the hierarchy table
3. **Execute searches** using Glob (file patterns) and Grep (content patterns)
4. **Limit results**: First 50 matches per keyword; stop after 3 keywords with
   no results
5. **Deduplicate**: Merge overlapping file matches across keywords

---

## Pattern Extraction

### Evidence Credibility

A pattern requires **n >= 2 evidence files** to be considered credible. A single
implementation could be an outlier or anti-pattern.

| Evidence Count | Credibility | Action                             |
| -------------- | ----------- | ---------------------------------- |
| 1              | Low         | Excluded from results              |
| 2-4            | Medium      | Included with frequency noted      |
| 5+             | High        | Included as established convention |

### Classification Rules

1. **Naming convention**: File names follow a consistent suffix/prefix pattern
   (e.g., `*Service.ts` in `src/services/`)
2. **Structural**: Files are organized in a predictable directory structure
   (e.g., `src/routes/[resource].ts`)
3. **Implementation interface**: Exported functions/classes share a common
   signature pattern (e.g., `async method(params): Promise<Result<T>>`)
4. **Idiom**: Recurring code patterns within implementations (e.g., builder
   pattern, error wrapping)

---

## Output Schema

The pattern mining output follows the `PatternMiningResultSchema` defined in
`packages/nightgauge-sdk/src/context/schemas/pattern-mining.ts`.

```json
{
  "patterns_found": [
    {
      "pattern_type": "naming_convention",
      "category": "file_naming",
      "pattern": "Services named `*Service.ts` in `src/services/`",
      "evidence": ["src/services/PhotoService.ts", "src/services/FileService.ts"],
      "frequency": 12,
      "example_implementations": ["src/services/PhotoService.ts:1-50"]
    }
  ],
  "similar_issues": [
    {
      "issue_number": 42,
      "title": "Add user photo upload",
      "relevance_score": 0.85,
      "pattern_overlap": ["service_pattern", "api_endpoint", "file_naming"],
      "plan_file": ".nightgauge/plans/42-user-photo-upload.md"
    }
  ],
  "pattern_classifications": {
    "naming_conventions": 5,
    "structural_patterns": 3,
    "interface_patterns": 2,
    "idioms": 1
  },
  "search_queries_used": ["Service.ts", "async.*Promise", "export.*interface"],
  "coverage_ratio": 0.68,
  "token_cost_estimate": 2400,
  "recommendations": [
    "Follow service pattern from src/services/ directory",
    "Review PhotoService.ts for API endpoint design"
  ]
}
```

---

## Integration into Planning Context

Pattern mining results are stored in `planning-{N}.json` under the
`pattern_mining_results` field:

```json
{
  "schema_version": "1.5",
  "issue_number": 42,
  "pattern_mining_results": {
    "patterns_found": [...],
    "similar_issues": [...],
    "pattern_classifications": {...},
    "recommendations": [...]
  },
  "plan_file": "...",
  "approach": "..."
}
```

Feature-dev reads `patterns_found` to:

- Pre-load example files referenced in pattern evidence
- Apply extracted naming conventions
- Reuse interface patterns from similar implementations

---

## Token Cost Analysis

| Operation                          | Estimated Tokens |
| ---------------------------------- | ---------------- |
| Keyword extraction from issue      | ~200             |
| Glob/Grep searches (5-10 queries)  | ~1,500           |
| Pattern extraction and JSON output | ~500             |
| Planning context merge             | ~200             |
| **Total per issue**                | **~2,400**       |

**Budget**: < 5,000 tokens per issue.

**ROI**: Saves 3,000+ tokens in feature-planning documentation reading by
shortlisting docs based on discovered patterns, and prevents feature-dev from
reimplementing existing patterns.

---

## Limitations

1. **Keyword-only matching**: No semantic understanding — patterns are discovered
   via keyword hierarchy, not AI reasoning
2. **Current snapshot**: Patterns reflect the current codebase state; no
   historical tracking
3. **Single repository**: Does not search across sibling repositories
4. **False negatives**: Unusual naming or structure may not match keyword patterns
5. **No quality judgment**: Cannot distinguish good patterns from anti-patterns
   (mitigated by n >= 2 evidence requirement)

---

## Configuration

Optional configuration in `.nightgauge/pattern-mining-config.yaml`:

```yaml
# Pattern mining configuration (all fields optional)
pattern_mining:
  enabled: true
  max_results_per_keyword: 50
  min_evidence_count: 2
  excluded_paths:
    - node_modules/
    - dist/
    - .git/
  keyword_overrides: {}
```

---

## References

- **Skill**: `skills/nightgauge-pattern-mining/SKILL.md`
- **Schema**: `packages/nightgauge-sdk/src/context/schemas/pattern-mining.ts`
- **Config**: `.nightgauge/pattern-mining-config.yaml`
- **Context Architecture**: `docs/CONTEXT_ARCHITECTURE.md`
- **Adaptive Documentation Reading**: `docs/ADAPTIVE_DOCUMENTATION_READING.md`
- **Feature Planning**: `skills/nightgauge-feature-planning/SKILL.md`

---

**Author**: nightgauge **License**: Apache-2.0
