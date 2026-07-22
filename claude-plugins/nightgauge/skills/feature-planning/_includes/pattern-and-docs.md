# Pattern Mining and Documentation-First Analysis (Phases 2.5, 3)

This reference carries the procedural detail for pattern mining (Phase 2.5) and
documentation-first analysis (Phase 3). Follow the section matching the phase
you are currently in.

## Contents

- [Phase 2.5: Pattern Mining](#phase-25-pattern-mining)
- [Phase 3: Documentation-First Analysis](#phase-3-documentation-first-analysis)

## Phase 2.5: Pattern Mining

**PURPOSE**: Discover existing codebase patterns before documentation analysis
so that doc reading can be shortlisted and the plan references real conventions.

Launch a `Task` subagent to run the pattern mining skill. The subagent
uses only Glob, Grep, and Read tools — no model invocation.

```
Task (model: "haiku"):
  "You are a codebase pattern mining subagent. Follow the instructions in
  skills/nightgauge-pattern-mining/SKILL.md exactly.

  Input:
  - issue_number: {ISSUE_NUMBER}
  - issue_title: {ISSUE_TITLE}
  - requirements: {REQUIREMENTS_SUMMARY}
  - documentation_scope: {SCOPE}
  - excluded_paths: [node_modules/, dist/, .git/, coverage/]

  Search the codebase for existing implementation patterns relevant to this
  issue. Extract naming conventions, structural patterns, implementation
  interfaces, and idioms. Find similar past issues via plan file overlap.

  Return ONLY valid JSON matching the PatternMiningResultSchema. Exact types required:

  {
    "patterns_found": [                    // ARRAY OF OBJECTS (not strings)
      {
        "pattern_type": "structural",      // string: naming_convention|structural|implementation_interface|idiom
        "category": "TypeScript",          // string: optional sub-category
        "pattern": "description here",     // string: required — human-readable description
        "evidence": ["file.ts:10"],        // string[]: file paths where pattern was observed
        "frequency": 3,                    // integer: count of occurrences
        "example_implementations": ["..."] // string[]: file:line references
      }
    ],
    "similar_issues": ["#42"],            // array of strings OR objects
    "pattern_classifications": {           // OBJECT WITH INTEGER COUNTS (not array, not strings)
      "naming_conventions": 2,             // int >= 0
      "structural_patterns": 3,            // int >= 0
      "interface_patterns": 1,             // int >= 0
      "idioms": 2                          // int >= 0
    },
    "search_queries_used": ["query"],     // string[]
    "coverage_ratio": 0.75,              // float 0.0–1.0
    "token_cost_estimate": 5000,         // integer
    "recommendations": ["..."]           // string[]
  }

  WRONG: patterns_found: ["flexEnum pattern", "z.preprocess idiom"]  (strings, not objects)
  WRONG: pattern_classifications: ["naming_conventions", "structural"] (array, not object)
  WRONG: pattern_classifications: {naming: 2, structural: 3}  (wrong key names — use naming_conventions, structural_patterns, interface_patterns, idioms)"
```

After the subagent completes, merge results into the planning context:

```bash
# Parse pattern mining results
PATTERNS_FOUND=$(echo "$PATTERN_RESULT" | jq -c '.patterns_found // []')
SIMILAR_ISSUES=$(echo "$PATTERN_RESULT" | jq -c '.similar_issues // []')
PATTERN_RECOMMENDATIONS=$(echo "$PATTERN_RESULT" | jq -c '.recommendations // []')
PATTERN_CLASSIFICATIONS=$(echo "$PATTERN_RESULT" | jq -c '.pattern_classifications // {}')

# Use pattern recommendations to shortlist docs in Phase 3
PATTERN_COUNT=$(echo "$PATTERNS_FOUND" | jq 'length')
if [ "$PATTERN_COUNT" -gt 0 ]; then
  echo "Pattern mining found $PATTERN_COUNT patterns — using to prioritize doc reading"
fi
```

**Fallback — subagent failed**: If the pattern mining subagent fails or
returns invalid JSON, continue without pattern context. Pattern mining is
best-effort; planning must not fail because mining failed.

**Fallback — no subagent capability (#55)**: If your host cannot launch `Task`
subagents (non-Claude adapters: codex, gemini, copilot), run the mining
**inline** instead of skipping it: open
`skills/nightgauge-pattern-mining/SKILL.md` and follow its instructions
yourself with the same inputs, using only Glob/Grep/Read, then write the same
`patterns-{N}.json` output. Inline mining costs main-context tokens, so cap
yourself to the skill's documented limits (top 10 patterns, no file over 200
lines read in full). Only if inline mining is also impractical (e.g. severe
context pressure), continue without pattern context as above.

## Phase 3: Documentation-First Analysis

### Greenfield Detection (#1319)

Before reading docs, check if this is a greenfield repo:

```bash
# Greenfield detection: if key docs are missing or skeletal
ARCH_LINES=$(wc -l < docs/ARCHITECTURE.md 2>/dev/null || echo 0)
STANDARDS_LINES=$(wc -l < docs/CODE_STANDARDS.md 2>/dev/null || echo 0)

if [ "$ARCH_LINES" -lt 20 ] && [ "$STANDARDS_LINES" -lt 20 ]; then
  GREENFIELD=true
fi
```

### Greenfield path (establish-patterns mode)

When `GREENFIELD=true`:

1. Read CLAUDE.md and AGENTS.md for project conventions and tech stack.
2. Read `.nightgauge/config.yaml` for any configured patterns.
3. Check existing code (if any) to infer emerging patterns.
4. For each requirement, note whether it **establishes** a new pattern or
   **follows** an existing one.
5. Include in the plan a "Patterns Established" section listing conventions this
   issue creates (e.g., "Establishes the Drizzle schema file naming convention:
   `packages/db/src/schema/{table}.ts`").
6. Include a task in the plan to update `docs/ARCHITECTURE.md` and/or
   `docs/CODE_STANDARDS.md` with the new patterns so future issues can follow
   them.

### Brownfield path (standard documentation-first)

When `GREENFIELD=false` (existing docs are substantial):

#### Parallel Documentation Gathering (standard/extended scope)

When documentation scope is `standard` or `extended`, launch parallel `Task`
subagents to gather documentation context simultaneously. This eliminates the
sequential bottleneck of reading 5-10+ doc files one at a time.

**Activation**: `documentation_scope` in (`standard`, `extended`). For `minimal`
and `targeted` scopes, the doc set is small enough that sequential reading is
faster than subagent coordination overhead.

Launch ALL of the following `Task` subagents in a **single message**:

```
Task (model: "haiku"):
  "Read and summarize the architecture and context documentation for this
  project. Read these files and extract key patterns, component boundaries,
  and data flow relevant to the feature being planned:
  - docs/ARCHITECTURE.md
  - docs/CONTEXT_ARCHITECTURE.md
  - docs/PIPELINE_EXECUTION.md (if scope=extended)
  Return a structured summary: { \"category\": \"architecture\",
  \"patterns\": [...], \"components\": [...], \"data_flow\": [...],
  \"relevant_to_feature\": string }. Output ONLY the JSON."

Task (model: "haiku"):
  "Read and summarize the coding standards and testing documentation for
  this project. Read these files and extract conventions, naming rules,
  test patterns, and quality requirements:
  - docs/CODE_STANDARDS.md (or standards/ directory)
  - docs/TESTING.md
  - docs/GIT_WORKFLOW.md
  Return a structured summary: { \"category\": \"standards\",
  \"naming_conventions\": [...], \"test_patterns\": [...],
  \"quality_requirements\": [...] }. Output ONLY the JSON."

Task (model: "haiku"):
  "Analyze the source code files most relevant to this feature. The issue
  requires: {REQUIREMENT_SUMMARY}. Find and read the source files most
  likely to be modified or extended. Look for existing patterns, imports,
  types, and interfaces that the implementation should follow. Files to
  start with: {FILES_HINTED_BY_ISSUE_BODY}.
  Return a structured summary: { \"category\": \"source_analysis\",
  \"existing_patterns\": [...], \"key_interfaces\": [...],
  \"files_to_modify\": [...], \"files_to_read\": [...] }.
  Output ONLY the JSON."
```

**Model selection**: All subagents use `haiku` — doc reading and summarization
is a comprehension task, not a generation task. This keeps cost low while
parallelizing the most time-consuming part of planning.

After all subagents complete, merge their outputs into the planning context:

```bash
# Merge parallel doc gathering results
ARCH_PATTERNS=$(echo "$ARCH_RESULT" | jq -r '.patterns')
STANDARDS=$(echo "$STANDARDS_RESULT" | jq -r '.naming_conventions')
SOURCE_FILES=$(echo "$SOURCE_RESULT" | jq -r '.files_to_modify')
FILES_TO_READ=$(echo "$SOURCE_RESULT" | jq -r '.files_to_read')
```

**Fallback**: If any subagent fails, fall through to sequential doc reading for
that category. The parallel phase is best-effort.

#### Sequential path (minimal/targeted scope or fallback)

1. Read core docs first (architecture, standards, workflow).
2. Map each requirement to documented patterns.
3. Record unknowns and only then inspect code for undocumented behavior.
4. Keep exploration targeted to files directly related to unknowns.
