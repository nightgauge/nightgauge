# Spike Scaffold Tests

Tests for Phase 2.X (Spike Artifact Path Selection) of the
`nightgauge-issue-create` skill. All test cases are behavioral — they
describe the expected skill output given controlled inputs. Use these as
acceptance test specifications when verifying the skill manually or via
automated harness.

## Setup Assumptions

- A temporary directory is used in place of the real `docs/spikes/`,
  `docs/decisions/`, and `docs/research/` directories. Tests must not depend on
  the actual state of the repository's spike artifacts.
- The GitHub issue number is provided as a fixed value (e.g., `9999`) so paths
  are deterministic regardless of when tests run.
- YAML scaffold content is verified by parsing the generated body with a YAML
  parser and checking the resulting structure against the spike contract schema.

---

## TC-1: Default Spike Path — Auto-Numbering with Issue Number

**Scenario**: Create a `type:spike` issue with no artifact type specified.

**Input**:

- Issue number: `9999`
- Issue title: `spike: evaluate caching layer for API responses`
- Artifact type: (not specified — defaults to `spike`)
- Existing files in `docs/spikes/`: none

**Expected**:

- Artifact type defaults to `spike`
- Slug generated from title (sans `spike:` prefix): `evaluate-caching-layer-for-api-responses`
- Artifact path: `docs/spikes/9999-evaluate-caching-layer-for-api-responses.md`
- Issue body contains: `**Artifact**: [\`docs/spikes/9999-evaluate-caching-layer-for-api-responses.md\`](...)`
- Issue body contains a fenced ` ```yaml recommendations ` block with `spike: 9999`
- No collision error (file does not exist)

**Verification**:

```bash
# Verify path construction
echo "docs/spikes/9999-evaluate-caching-layer-for-api-responses.md" | \
  grep -qE '^docs/spikes/[0-9]+-[a-z0-9-]+\.md$' && echo PASS || echo FAIL

# Verify YAML scaffold is parseable
python3 -c "
import yaml
body = open('<generated-issue-body-file>').read()
# Extract yaml recommendations block
import re
m = re.search(r'\`\`\`yaml recommendations\n(.*?)\`\`\`', body, re.DOTALL)
assert m, 'No yaml recommendations block found'
data = yaml.safe_load(m.group(1))
assert data['spike'] == 9999, 'spike field must match issue number'
assert 'recommendations' in data, 'recommendations field required'
print('YAML scaffold: PASS')
"
```

---

## TC-2: ADR Path — Sequential NNN Prefix

**Scenario**: Create a `type:spike` issue where the artifact type is explicitly
`adr` (Architecture Decision Record).

**Input**:

- Issue number: `9999`
- Issue title: `ADR: adopt event-driven dispatch for the scheduler`
- Artifact type: `adr` (inferred from title containing "ADR")
- Existing files in `docs/decisions/`: `001-foo.md`, `002-bar.md`, `005-baz.md`

**Expected**:

- Artifact type resolves to `adr`
- Slug: `adopt-event-driven-dispatch-for-the-scheduler`
- `LAST_NUM` scan finds `005` → `NEXT_NUM` = `006`
- Artifact path: `docs/decisions/006-adopt-event-driven-dispatch-for-the-scheduler.md`
- Issue body contains the `**Artifact**:` link with the ADR path
- YAML scaffold has `spike: 9999`

**Verification**:

```bash
# Simulate the LAST_NUM scan
ls /tmp/test-decisions/ 2>/dev/null | grep -oE '^[0-9]+' | sort -n | tail -1
# Expected: 5

NEXT_NUM=$(printf '%03d' $((5 + 1)))
echo "$NEXT_NUM"
# Expected: 006

echo "docs/decisions/006-adopt-event-driven-dispatch-for-the-scheduler.md" | \
  grep -qE '^docs/decisions/[0-9]+-[a-z0-9-]+\.md$' && echo PASS || echo FAIL
```

---

## TC-3: Research Path — Issue Number as Prefix

**Scenario**: Create a `type:spike` issue with artifact type `research`.

**Input**:

- Issue number: `9999`
- Issue title: `spike: investigate lm-studio compatibility with openai sdk`
- Artifact type: `research` (explicitly provided via `--spike-type research`)
- Existing files in `docs/research/`: none

**Expected**:

- Artifact path: `docs/research/9999-investigate-lm-studio-compatibility-with-openai-sdk.md`
- Path matches allowed-directory check (`docs/research/`)
- Well-formed path regex passes

**Verification**:

```bash
echo "docs/research/9999-investigate-lm-studio-compatibility-with-openai-sdk.md" | \
  grep -qE '^docs/(spikes|decisions|research)/[0-9]+-[a-z0-9-]+\.md$' && echo PASS || echo FAIL
```

---

## TC-4: Collision Rejection

**Scenario**: A file already exists at the computed artifact path.

**Input**:

- Issue number: `1065`
- Issue title: `spike: evaluate agent sdk tool calling`
- Existing file: `docs/spikes/1065-evaluate-agent-sdk-tool-calling.md` ← already exists

**Expected**:

- Skill emits `ERROR: artifact path collision` to stderr
- Skill exits with non-zero status code
- Error message includes the computed path: `docs/spikes/1065-evaluate-agent-sdk-tool-calling.md`
- Error message includes remediation options (use `--spike-slug`, check for duplicate)
- No GitHub issue is created

**Verification**:

```bash
# Simulate collision check
ARTIFACT_PATH="docs/spikes/1065-evaluate-agent-sdk-tool-calling.md"
touch "$ARTIFACT_PATH"  # create the colliding file

if [ -f "$ARTIFACT_PATH" ]; then
  echo "ERROR: artifact path collision"
  echo "  Computed path: $ARTIFACT_PATH"
  echo "  Already exists: YES"
  EXIT_CODE=1
fi

[ $EXIT_CODE -eq 1 ] && echo PASS || echo FAIL
rm -f "$ARTIFACT_PATH"
```

---

## TC-5: Disallowed Directory Rejection

**Scenario**: A user provides a custom artifact path outside the three allowed
directories (e.g., via a future `--artifact-path` flag or by editing the
auto-generated path).

**Input**:

- Artifact path override: `docs/rfcs/9999-some-rfc.md`

**Expected**:

- Skill emits `ERROR: artifact path must be under one of: docs/spikes/ docs/decisions/ docs/research/`
- Skill exits with non-zero status code
- No GitHub issue is created

**Verification**:

```bash
ARTIFACT_PATH="docs/rfcs/9999-some-rfc.md"
ALLOWED_DIRS="docs/spikes/ docs/decisions/ docs/research/"
IS_ALLOWED=false
for DIR in $ALLOWED_DIRS; do
  echo "$ARTIFACT_PATH" | grep -q "^$DIR" && IS_ALLOWED=true && break
done

[ "$IS_ALLOWED" = "false" ] && echo "PASS: rejected disallowed dir" || echo "FAIL: accepted disallowed dir"
```

---

## TC-6: YAML Scaffold Validity — Matches Spike Contract Schema

**Scenario**: Verify that the YAML scaffold produced by Phase 2.X.5 is valid
YAML and satisfies the structural requirements of `docs/SPIKE_CONTRACT.md`.

**Input**: The raw YAML scaffold template from the skill (before author fills it in).

**Expected**:

- `spike` field is an integer matching the issue number
- `recommendations` field is a non-empty sequence
- Each recommendation entry has all required fields: `id`, `action`, `title`,
  `type`, `priority`, `size`
- `action`, `type`, `priority`, `size` values are within allowed enums
- `id` value is kebab-case (passes `[a-z0-9-]+`)
- The YAML block parses without error

**Verification**:

```python
import yaml, re

scaffold = """
spike: 9999
recommendations:
  - id: <kebab-case-id>
    action: adopt
    title: "<Issue title>"
    type: feature
    priority: medium
    size: M
    labels: []
    body: |
      Optional body text.
    depends_on: []
"""

# Parsing must not raise
data = yaml.safe_load(scaffold)

assert isinstance(data['spike'], int), "spike must be int"
assert isinstance(data['recommendations'], list), "recommendations must be list"
assert len(data['recommendations']) > 0, "recommendations must be non-empty"

entry = data['recommendations'][0]
for field in ['id', 'action', 'title', 'type', 'priority', 'size']:
    assert field in entry, f"required field missing: {field}"

assert entry['action'] in ('adopt', 'defer', 'skip'), "invalid action"
assert entry['type'] in ('feature', 'bug', 'docs', 'chore', 'spike'), "invalid type"
assert entry['priority'] in ('critical', 'high', 'medium', 'low'), "invalid priority"
assert entry['size'] in ('XS', 'S', 'M', 'L', 'XL'), "invalid size"

print("TC-6: PASS")
```

---

## TC-7: Path Determinism — Same Inputs Produce Same Output

**Scenario**: Running Phase 2.X twice with the same inputs produces identical
artifact paths (no randomness, no timestamp-based suffix, no temp file residue).

**Input** (run twice in sequence with same filesystem state):

- Issue number: `9999`
- Issue title: `spike: evaluate caching layer`
- Artifact type: `spike`
- `docs/spikes/`: empty both times

**Expected**:

- Run 1 artifact path: `docs/spikes/9999-evaluate-caching-layer.md`
- Run 2 artifact path: `docs/spikes/9999-evaluate-caching-layer.md`
- Paths are identical
- No temporary files left in `docs/spikes/` after either run (the skill
  validates the path but does NOT create the file at creation time)

**Verification**:

```bash
# Run slug generation twice
for i in 1 2; do
  SLUG=$(echo "spike: evaluate caching layer" | \
    sed 's/^spike:[[:space:]]*//' | \
    tr '[:upper:]' '[:lower:]' | \
    sed 's/[^a-z0-9]/-/g' | \
    sed 's/--*/-/g' | \
    sed 's/^-//;s/-$//' | \
    cut -c1-60)
  echo "Run $i: docs/spikes/9999-${SLUG}.md"
done
# Both lines must be identical

# Verify no files created
ls /tmp/test-spikes/ 2>/dev/null | wc -l
# Expected: 0
```

---

## TC-8: Slug Truncation — Titles Exceeding 60 Characters

**Scenario**: An issue title that produces a slug longer than 60 characters
must be truncated at 60.

**Input**:

- Issue title: `spike: evaluate the comprehensive long-term impact of switching from synchronous to asynchronous task processing pipelines`

**Expected**:

- Raw slug (before truncation): `evaluate-the-comprehensive-long-term-impact-of-switching-from-synchronous-to-asynchronous-task-processing-pipelines`
- Truncated slug (60 chars): `evaluate-the-comprehensive-long-term-impact-of-switching-fro`
- Artifact path: `docs/spikes/9999-evaluate-the-comprehensive-long-term-impact-of-switching-fro.md`
- Path still passes well-formed regex check

**Verification**:

```bash
SLUG=$(echo "spike: evaluate the comprehensive long-term impact of switching from synchronous to asynchronous task processing pipelines" | \
  sed 's/^spike:[[:space:]]*//' | \
  tr '[:upper:]' '[:lower:]' | \
  sed 's/[^a-z0-9]/-/g' | \
  sed 's/--*/-/g' | \
  sed 's/^-//;s/-$//' | \
  cut -c1-60)

[ ${#SLUG} -le 60 ] && echo "PASS: slug length ${#SLUG}" || echo "FAIL: slug too long (${#SLUG})"
echo "docs/spikes/9999-${SLUG}.md" | grep -qE '^docs/spikes/[0-9]+-[a-z0-9-]+\.md$' && echo "PASS: well-formed" || echo "FAIL: malformed"
```

---

## TC-9: Non-Spike Issue — Phase 2.X Does Not Run

**Scenario**: Creating an implementation (`type:feature`) issue must not trigger
Phase 2.X and must not add spike scaffolding to the issue body.

**Input**:

- Issue title: `feat: add caching layer for API responses`
- Issue type: `feature`

**Expected**:

- Phase 2.X is skipped entirely
- Issue body contains no `**Artifact**:` link
- Issue body contains no `yaml recommendations` fenced block
- `ARTIFACT_PATH` is unset

**Verification**:

Check that the generated issue body for a non-spike issue contains neither:

- `**Artifact**:`
- ` ```yaml recommendations`

Both strings must be absent.

---

## TC-10: Same-Repo Spike Dependents Default to Path A

**Scenario**: Create an epic with a spike and two same-repo implementation
tickets whose technical notes depend on the spike artifact.

**Input**:

- Spike issue number: `329`
- Spike title: `spike: design workspace data model`
- Spike artifact path: `docs/spikes/329-design-workspace-data-model.md`
- Dependent tickets:
  - `workspace: implement CRUD API`
  - `dashboard: add workspace selector`
- All issues target `acme/dashboard`

**Expected**:

- Routing choice defaults to `Path A — Recommendations`
- Dependent tickets are removed from the sibling creation list
- Spike YAML contains one `action: adopt` recommendation per dependent ticket
- Recommendation IDs are stable kebab-case values
- Decomposition preview records `Path A` and the default reason `same repo`

**Verification**:

```python
import yaml

data = yaml.safe_load("""
spike: 329
recommendations:
  - id: workspace-crud-api
    action: adopt
    title: "workspace: implement CRUD API"
    type: feature
    priority: high
    size: M
    labels: ["component:api"]
    body: |
      Implement the API using the workspace model selected by the spike.
    depends_on: []
  - id: workspace-selector
    action: adopt
    title: "dashboard: add workspace selector"
    type: feature
    priority: high
    size: M
    labels: ["component:dashboard"]
    body: |
      Add the selector after the workspace CRUD API exists.
    depends_on: ["workspace-crud-api"]
""")

ids = [entry["id"] for entry in data["recommendations"]]
assert ids == ["workspace-crud-api", "workspace-selector"]
assert all(entry["action"] == "adopt" for entry in data["recommendations"])
assert "workspace-selector" not in data["recommendations"][0].get("depends_on", [])
assert data["recommendations"][1]["depends_on"] == ["workspace-crud-api"]
print("TC-10: PASS")
```

---

## TC-11: Cross-Repo Spike Dependents Default to Path C

**Scenario**: Create an epic with a dashboard architectural decision and
implementation tickets targeting companion repositories. A natural first
ticket (workspace CRUD API) can produce both initial code and the ADR.

**Input**:

- Cross-repo dependents (no standalone spike issue under Path C):
  - `dashboard: workspace CRUD API` (`acme/dashboard`) —
    candidate ADR-bearing first ticket
  - `platform: expose workspace API` (`acme/platform`)
- Planned ADR path: `docs/decisions/042-workspace-data-model.md`
- Subsequent dependent body starts with `## Summary`

**Expected**:

- Routing choice defaults to `Path C — Spike-with-implementation`
- No standalone `type:spike` issue is created
- First ticket body is augmented with `## Architectural Decision Required`
  citing the ADR path and the question(s) to answer
- Subsequent dependent body is prepended with `## Prerequisite ADR` citing
  the ADR path and the first ticket
- Native dependency wiring is planned as `blockedBy=<first-ticket-number>`
  (not a spike number)
- Decomposition preview records `Path C` and the default reason `cross repo`

**Verification**:

```bash
ADR_PATH='docs/decisions/042-workspace-data-model.md'
FIRST_TICKET='#330'
BODY='## Summary

Implement the platform workspace API.'

PREPENDED="## Prerequisite ADR

**\`${ADR_PATH}\`** — produced by \`${FIRST_TICKET}\`'s PR.
The pipeline planning stage MUST read this file before drafting a plan. If
the file does not exist on disk, the first ticket has not merged and this
ticket is not actionable.

${BODY}"

echo "$PREPENDED" | grep -q '^## Prerequisite ADR$' && echo PASS || echo FAIL
echo "$PREPENDED" | grep -q "$ADR_PATH" && echo PASS || echo FAIL
echo "$PREPENDED" | grep -q "$FIRST_TICKET" && echo PASS || echo FAIL
```

### TC-11b: Cross-Repo Path B Opt-In Triggers Guard

**Scenario**: User explicitly opts into Path B for a cross-repo epic where
the design space is genuinely too open to commit code in a first ticket's PR.

**Input**:

- Spike issue ref: `acme/acme-web#200`
- Spike artifact path: `docs/spikes/329-design-workspace-data-model.md`
- Dependent ticket repo: `acme/platform`
- User override: Path B with rationale `design space too open: workspace
isolation strategy requires upfront research`
- Headless flag: `--accept-path-b-risk`

**Expected**:

- Path B guard (Step 2.7.2a) fires before any issues are created
- Guard records the rationale in the decomposition preview
- Without `--accept-path-b-risk` (or interactive `yes`), the skill exits
  non-zero and instructs the operator to switch to Path C
- With acknowledgement, dependent body is prepended with
  `## Prerequisite Artifact` and `blockedBy` is wired to the spike

**Verification**:

```bash
RATIONALE='design space too open: workspace isolation strategy requires upfront research'
ACK_FLAG='--accept-path-b-risk'

PREVIEW="Selected path: Path B — Concurrent siblings with auto-cite (opt-in; rationale: ${RATIONALE})"
echo "$PREVIEW" | grep -q 'opt-in' && echo PASS || echo FAIL
echo "$PREVIEW" | grep -q "$RATIONALE" && echo PASS || echo FAIL

# Without the flag, headless invocation should fail closed
HEADLESS_CMD='nightgauge-issue-create --epic 328 --route path-b'
echo "$HEADLESS_CMD" | grep -q -- "$ACK_FLAG" && echo "ACKED" || echo "GUARD_BLOCKS"
```

---

## TC-12: User Override from Path A to Path B

**Scenario**: Same-repo dependents would default to Path A, but the user chooses
Path B for visibility and immediate assignment.

**Input**:

- Spike and dependents target the same repository
- Default route: Path A
- User choice: Path B

**Expected**:

- Decomposition preview records `Path B (user override; default was Path A)`
- Dependent tickets remain siblings
- Each dependent body has the `## Prerequisite Artifact` section
- Each dependent sibling is created with `--blocked-by <spike-number>` or gets
  equivalent `nightgauge issue add-blocked-by` wiring after creation

**Verification**:

```bash
PREVIEW='Selected path: Path B — Concurrent siblings with auto-cite (user override; default was Path A)'
echo "$PREVIEW" | grep -q 'user override' && echo PASS || echo FAIL

CREATE_CMD='nightgauge issue create-sub 328 --title "workspace: implement CRUD API" --blocked-by 329'
echo "$CREATE_CMD" | grep -q -- '--blocked-by 329' && echo PASS || echo FAIL
```

---

## TC-13: Mixed Same-Repo and Cross-Repo Dependents

**Scenario**: One dependent is in the spike repo and another is cross-repo.

**Input**:

- Same-repo dependent: `dashboard: add workspace selector`
  (`acme/dashboard`)
- Cross-repo dependent: `platform: expose workspace API`
  (`acme/platform`)
- Anchor repo: `acme/dashboard`

**Expected**:

- Cross-repo subset defaults to **Path C** — first dependent ticket commits
  the ADR; subsequent cross-repo dependents `blockedBy` the first ticket
- Same-repo subset defaults to Path A unless the user overrides
- Preview calls out the mixed shape and lets the user reassign individual
  dependents
- If the user opts cross-repo dependents into Path B, the Path B guard fires
  for that subset only

**Verification**:

```bash
ANCHOR_REPO='acme/dashboard'
DEPS='acme/dashboard:dashboard-selector acme/platform:platform-api'

CROSS_REPO_DEFAULT='Path A'
for DEP in $DEPS; do
  REPO="${DEP%%:*}"
  if [ "$REPO" != "$ANCHOR_REPO" ]; then
    CROSS_REPO_DEFAULT='Path C'
    break
  fi
done

[ "$CROSS_REPO_DEFAULT" = "Path C" ] && echo PASS || echo FAIL
```

---

## TC-14: Path C Apply — First-Ticket ADR Section

**Scenario**: Path C is selected for a cross-repo epic. Verify the
ADR-bearing first ticket's body and a subsequent dependent's body are shaped
correctly.

**Input**:

- ADR-bearing first ticket: `dashboard: workspace CRUD API`
- Planned ADR path: `docs/decisions/042-workspace-data-model.md`
- ADR questions: workspace entity shape, ownership boundary, multi-tenant
  isolation strategy
- Subsequent dependent: `platform: expose workspace API`

**Expected**:

- First ticket body contains a `## Architectural Decision Required` section
  citing the ADR path and the three questions to answer
- Subsequent dependent body begins with `## Prerequisite ADR` citing the ADR
  path and the first ticket
- No `type:spike` issue is created
- No `yaml recommendations` block is emitted (Path C bypasses the
  materializer entirely)

**Verification**:

```bash
ADR='docs/decisions/042-workspace-data-model.md'
FIRST_BODY="## Architectural Decision Required

This ticket's PR is responsible for committing
\`${ADR}\`. The ADR records the decision needed by
the rest of this epic.

**Question(s) to answer in the ADR**:

- workspace entity shape
- ownership boundary
- multi-tenant isolation strategy"

echo "$FIRST_BODY" | grep -q '^## Architectural Decision Required$' && echo PASS || echo FAIL
echo "$FIRST_BODY" | grep -q "$ADR" && echo PASS || echo FAIL

DEPENDENT_BODY="## Prerequisite ADR

**\`${ADR}\`** — produced by \`#330\`'s PR."

echo "$DEPENDENT_BODY" | grep -q '^## Prerequisite ADR$' && echo PASS || echo FAIL
echo "$DEPENDENT_BODY" | grep -q "$ADR" && echo PASS || echo FAIL
```

---

## TC-15: Phase 3 Hard-Gate Rejects Spike Body Without yaml Block

**Scenario**: A `type:spike` sub-issue body is assembled but is missing the
required fenced `yaml recommendations` block. The Phase 3 hard-gate must reject
this and exit non-zero before calling `nightgauge issue create-sub`.

**Input**:

- Sub-issue label list includes `type:spike`
- Body file contains: `## Spike Contract (Path A)\n\ndocs/spikes/9999-some-spike.md\n`
  (no yaml recommendations block)

**Expected**:

- `nightgauge spike validate --body-file $BODY_FILE` exits 1
- Stderr contains: `missing a fenced`
- Phase 3 hard-gate prints: `ERROR: type:spike sub-issue body failed contract validation.`
- `nightgauge issue create-sub` is NOT called
- Skill exits non-zero

**Verification**:

```bash
BODY_FILE=$(mktemp)
cat > "$BODY_FILE" << 'EOF'
## Spike Contract (Path A)

docs/spikes/9999-some-spike.md

Some description without a yaml block.
EOF

LABEL_LIST="type:spike,priority:high"

if echo "$LABEL_LIST" | grep -q "type:spike"; then
  nightgauge spike validate --body-file "$BODY_FILE" 2>&1
  EXIT=$?
  [ $EXIT -ne 0 ] && echo "PASS: gate rejected" || echo "FAIL: gate should have rejected"
fi

rm -f "$BODY_FILE"
```

---

## TC-16: Phase 3 Hard-Gate Rejects Spike Body Without Path Declaration

**Scenario**: A `type:spike` sub-issue body has a valid yaml block but no
`## Spike Contract (Path A/B/C)` heading. The gate must reject it.

**Input**:

- Sub-issue label list includes `type:spike`
- Body file has a valid yaml recommendations block but no Path declaration heading

**Expected**:

- `nightgauge spike validate --body-file $BODY_FILE` exits 1
- Stderr contains: `path declaration`
- Phase 3 hard-gate prevents `create-sub` from running

**Verification**:

````bash
BODY_FILE=$(mktemp)
cat > "$BODY_FILE" << 'EOF'
No path declaration heading here.

docs/spikes/9999-some-spike.md

```yaml recommendations
spike: 9999
recommendations:
  - id: some-rec
    action: adopt
    title: "Some recommendation"
    type: feature
    priority: high
    size: M
````

EOF

nightgauge spike validate --body-file "$BODY_FILE" 2>&1 | grep -q "path declaration" \
&& echo "PASS: correct error" || echo "FAIL: unexpected error or missing message"

rm -f "$BODY_FILE"

````

---

## TC-17: Phase 3 Hard-Gate Rejects Spike Body With Malformed yaml

**Scenario**: A `type:spike` sub-issue body has a yaml block but it fails schema
validation (missing `spike:` field or invalid `action` value).

**Input**:

- Sub-issue label list includes `type:spike`
- Body file has `## Spike Contract (Path A)` heading and artifact path but yaml
  is missing the required `spike:` field

**Expected**:

- `nightgauge spike validate --body-file $BODY_FILE` exits 1
- Stderr contains schema validation error

**Verification**:

```bash
BODY_FILE=$(mktemp)
cat > "$BODY_FILE" << 'EOF'
## Spike Contract (Path A)

docs/spikes/9999-some-spike.md

```yaml recommendations
recommendations:
  - id: some-rec
    action: adopt
    title: "Some recommendation"
    type: feature
    priority: high
    size: M
````

EOF

nightgauge spike validate --body-file "$BODY_FILE" 2>&1 | grep -q "schema validation" \
&& echo "PASS: schema error detected" || echo "FAIL: expected schema validation error"

rm -f "$BODY_FILE"

````

---

## TC-18: Phase 3 Hard-Gate Accepts Valid Spike Body and Proceeds to create-sub

**Scenario**: A `type:spike` sub-issue body has all required elements: a valid
yaml recommendations block, a `## Spike Contract (Path A)` heading, and a
well-formed artifact path. The gate must accept it and allow `create-sub` to run.

**Input**:

- Sub-issue label list includes `type:spike`
- Body file has: `## Spike Contract (Path A)` heading, `docs/spikes/9999-evaluate-something.md`,
  and a valid yaml block with `spike: 9999` and at least one recommendation

**Expected**:

- `nightgauge spike validate --body-file $BODY_FILE` exits 0
- Stdout contains: `spike validate: OK`
- Phase 3 hard-gate allows `nightgauge issue create-sub` to proceed

**Verification**:

```bash
BODY_FILE=$(mktemp)
cat > "$BODY_FILE" << 'EOF'
## Spike Contract (Path A)

**Artifact**: `docs/spikes/9999-evaluate-something.md`

Some description.

```yaml recommendations
spike: 9999
recommendations:
  - id: evaluate-something
    action: adopt
    title: "Implement the evaluated approach"
    type: feature
    priority: high
    size: M
````

EOF

nightgauge spike validate --body-file "$BODY_FILE"
EXIT=$?
[ $EXIT -eq 0 ] && echo "PASS: gate accepted valid body" || echo "FAIL: gate rejected valid body (exit $EXIT)"

rm -f "$BODY_FILE"

```

```
