# Feature Assessment Engine

Reference framework for scoring Claude Code features and changes against structured criteria to determine pipeline integration value. Consumed by Phase 5 (scoring) of the `nightgauge-release-watch` skill and available for standalone use.

## Overview

The Feature Assessment Engine evaluates new Claude Code capabilities across six weighted dimensions, producing a 0-100 composite score that determines integration priority. Assessments are **human-guided, not fully automated** — the scoring framework provides structure, but assessment requires contextual judgment about pipeline impact.

**Design principle:** The framework answers "How valuable is this feature for our pipeline?" not "Is this a good feature in general?"

---

## The Six Dimensions

### Dimension 1: Pipeline Stage Impact (0–30 points)

**What it measures:** Does this feature enable or enhance capability in any of the six pipeline stages?

For each stage, score 0–5:

- **0** — No impact on this stage
- **1** — Minor convenience improvement (saves <5 min per run)
- **3** — Meaningful capability enhancement (enables new use case)
- **5** — Transformative (enables something previously impossible)

**Calculation:** Sum scores across 6 stages, cap at 30.

**Examples:**

| Feature              | issue-pickup | feature-planning | feature-dev            | feature-validate  | pr-create | pr-merge | **Total** |
| -------------------- | ------------ | ---------------- | ---------------------- | ----------------- | --------- | -------- | --------- |
| **Computer Use**     | 0            | 0                | 1                      | 5 (iOS Simulator) | 2         | 0        | **8**     |
| **Agent Teams**      | 0            | 2                | 4 (parallel subagents) | 3                 | 1         | 0        | **10**    |
| **Scheduled Tasks**  | 0            | 0                | 0                      | 0                 | 0         | 0        | **0**     |
| **Permission Relay** | 0            | 1                | 2                      | 3                 | 1         | 0        | **7**     |

**Why this dimension:** Different features benefit different stages. A feature weak in one stage but transformative in another still scores high overall.

**Assessment note:** Stage impact is objective — read the feature docs and trace through each stage's current workflow.

---

### Dimension 2: Automation Potential (0–20 points)

**What it measures:** Does this feature reduce manual intervention or enable more autonomous operation?

Scoring:

- **0** — No automation value; purely optional convenience
- **5** — Eliminates one manual step in a workflow
- **10** — Eliminates multiple manual steps or enables one fully-autonomous sub-workflow
- **15** — Enables fully autonomous operation of one major workflow (e.g., all tests pass → merge automatically)
- **20** — Transforms the entire pipeline's autonomy model

**Examples:**

| Feature                  | Score | Reasoning                                                          |
| ------------------------ | ----- | ------------------------------------------------------------------ |
| **Scheduled Tasks**      | 18    | Enables daily autonomous release monitoring without human trigger  |
| **Auto-Merge (PR)**      | 10    | Eliminates manual merge step; still requires review approval       |
| **Computer Use**         | 3     | Enables testing but still requires interpretation of results       |
| **Connectors (Slack)**   | 5     | Eliminates manual notification checking; core automation unchanged |
| **Permission Deny-List** | 2     | Minor convenience; doesn't reduce steps                            |

**Assessment note:** Ask "If this feature broke tomorrow, would we need to re-hire someone, or would it just slow us down?" High = former, Low = latter.

---

### Dimension 3: Safety & Reliability (0–15 points)

**What it measures:** Does this improve pipeline safety or reliability?

Scoring:

- **0** — No impact on safety; potential new risks
- **5** — Minor reliability improvement (e.g., better error messages)
- **10** — Significant safety enhancement (e.g., new rollback mechanism, better access control)
- **15** — Addresses a critical safety gap (e.g., permission model that prevents runaway execution)

**Examples:**

| Feature                       | Score | Reasoning                                                                     |
| ----------------------------- | ----- | ----------------------------------------------------------------------------- |
| **Permission Modes**          | 12    | `bypassPermissions` flag enables sandboxed execution without sandbox overhead |
| **Computer Use Safety Tiers** | 8     | View-only and click-only modes reduce risk of unintended actions              |
| **Activity Audit Trail**      | 10    | Retroactive accountability improves debugging of autonomous failures          |
| **Budget Enforcement**        | 12    | Prevents runaway token usage; critical for cost control                       |
| **Hooks (pre/post)**          | 6     | Better breakpoint control; minor improvement over current gates               |

**Assessment note:** Safety in autonomy context — not "is it a safe feature," but "does it help us run autonomously without fear?"

---

### Dimension 4: Developer Experience (0–15 points)

**What it measures:** Does this improve the experience of developing with or for the pipeline?

Scoring:

- **0** — No UX impact; orthogonal to developer workflow
- **5** — Minor convenience (saves <5 min per session)
- **10** — Meaningful workflow improvement (saves 15–30 min, or reduces cognitive load)
- **15** — Fundamentally better experience (changes how developers think about the pipeline)

**Examples:**

| Feature                           | Score | Reasoning                                                             |
| --------------------------------- | ----- | --------------------------------------------------------------------- |
| **Visual Diff Review (Desktop)**  | 10    | Inline PR review in VSCode is faster than browser context-switch      |
| **Voice Mode**                    | 3     | Niche for pipeline work; most stages are text-driven (planning, code) |
| **Better Error Messages**         | 8     | Reduces debugging time; improves DX without changing behavior         |
| **Inline Issue Linking**          | 12    | Enables richer context during feature planning and PR creation        |
| **Model Routing (faster models)** | 6     | Slight speedup, but doesn't change workflow fundamentally             |

**Assessment note:** This is subjective. Ask "Would this change how often developers interact with the pipeline differently?" High = structural change (e.g., shift from CLI to UI), Low = incremental (e.g., faster completion).

---

### Dimension 5: Implementation Complexity (0–10 points, INVERSE)

**What it measures:** How hard is it to integrate into the pipeline?

**Important:** This dimension is **inverted** — higher score = easier, lower = harder. The rationale: high-complexity features are harder to benefit from, so we need to discount their value.

Scoring:

- **10** — Drop-in: Just enable a flag or use a new API endpoint; no code changes needed
- **7** — Moderate: Requires skill updates, new config option, or minor SDK changes
- **4** — Significant: Requires new SDK modules, Go binary changes, or test infrastructure
- **1** — Massive: Requires architectural changes across multiple layers (e.g., new permission model, new context schema)

**Examples:**

| Feature                   | Score | Implementation Path                                                                                                           |
| ------------------------- | ----- | ----------------------------------------------------------------------------------------------------------------------------- |
| **Agent Teams**           | 4     | New SDK module (`calculateWaves()`, `detectDependencies()`), new Go binary commands, updates to skills                        |
| **Scheduled Tasks**       | 7     | Config option in `.nightgauge/config.yaml`, thin wrapper in SDK to trigger orchestrator                                       |
| **Computer Use**          | 1     | Requires new safety infrastructure, dedicated machine setup, changes to validation stage, potential risk mitigation framework |
| **Permission Relay**      | 7     | New SKILL.md frontmatter field, SDK validation, minimal code changes                                                          |
| **Better Error Messages** | 10    | Vendor feature; no pipeline code needed                                                                                       |

**Assessment note:** Low complexity features = quick wins. High complexity features need strong justification on other dimensions.

---

### Dimension 6: Cross-Repo Applicability (0–10 points)

**What it measures:** Is this feature useful across multiple repositories, or single-repo specific?

Scoring:

- **0** — Single-repo only; applies only to nightgauge or to a specific cross-repo
- **5** — Useful in 2–3 repos (e.g., applicable to Flutter and platform, but not Angular)
- **8** — Universally applicable to most repos with minor config (e.g., works in all 4 repos with different settings)
- **10** — Universal and identical across all repos (e.g., improved error handling benefits everywhere equally)

**Examples:**

| Feature                | Score | Reasoning                                                                                           |
| ---------------------- | ----- | --------------------------------------------------------------------------------------------------- |
| **Computer Use**       | 5     | iOS Simulator useful for Flutter; helpful for VSCode extension testing; not for platform or Angular |
| **Agent Teams**        | 8     | Applicable to all repos; each repo has different parallelization opportunities                      |
| **Permission Relay**   | 10    | Benefits all repos equally; permission rules apply universally                                      |
| **Scheduled Tasks**    | 10    | Release monitoring, backlog grooming, health checks useful in all repos                             |
| **Connectors (Slack)** | 8     | Valuable for all repos; some repos may not need certain integrations                                |

**Assessment note:** Ask "If I implement this in nightgauge, how much rework to support other repos?" Low = one-off, High = portable.

---

## Composite Score Calculation

```
TOTAL = Dimension1 + Dimension2 + Dimension3 + Dimension4 + Dimension5 + Dimension6
MAX = 30 + 20 + 15 + 15 + 10 + 10 = 100
```

All dimensions are summed directly. There are no additional multipliers or curve-fitting in the base framework (though organizations can customize weighting if desired).

---

## Classification Thresholds

| Score     | Classification | Action                                                     | Decision Trigger |
| --------- | -------------- | ---------------------------------------------------------- | ---------------- |
| **≥ 70**  | **High**       | Auto-create epic/issues; immediate integration value       | Fast-track       |
| **40–69** | **Medium**     | Add to backlog; worth tracking for next planning cycle     | Candidate        |
| **< 40**  | **Low**        | Log for historical reference; monitor for future relevance | Archive          |

---

## Override Rules

Exceptional cases that override base scoring:

**1. Breaking Changes** → Minimum 60 points

- **Why:** Breaking changes require assessment and mitigation planning, even if low-scoring otherwise
- **Example:** "Removed legacy hooks API" scores low on automation/UX, but minimum 60 forces planning

**2. Deprecations Affecting Our Usage** → Minimum 70 points

- **Why:** We must migrate or update affected code before sunset date
- **Example:** "Deprecated `--bare` flag" affects our scripting workflow; force high priority

**3. Security-Related Changes** → Minimum 50 points

- **Why:** Security decisions need formal review even if low-impact otherwise
- **Example:** "Permission model change" scores medium on safety, but minimum 50 ensures discussion

**4. Model Availability / Capacity Changes** → Minimum 60 points

- **Why:** Changes to available models directly impact cost/performance tuning
- **Example:** "Claude 3.5 Sonnet deprecated" forces cost/latency reassessment

These overrides ensure critical features don't slip through on technicalities.

---

## Assessment Template Structure

Each assessment follows this format:

```markdown
# Feature Assessment: [Feature Name]

**Version:** [Claude Code release version, e.g., 2.1.81]
**Date:** [YYYY-MM-DD when assessed]
**Assessor:** [Name or "auto" if generated]

## Feature Description

[Concise explanation of what this feature does, why it exists, and key capabilities]

## Dimension Scores

| Dimension                 | Score | Max     | Rationale                  |
| ------------------------- | ----- | ------- | -------------------------- |
| Pipeline Stage Impact     | X     | 30      | [Stage-by-stage breakdown] |
| Automation Potential      | X     | 20      | [How it affects autonomy]  |
| Safety & Reliability      | X     | 15      | [Safety implications]      |
| Developer Experience      | X     | 15      | [UX impact]                |
| Implementation Complexity | X     | 10      | [Integration effort]       |
| Cross-Repo Applicability  | X     | 10      | [Usefulness across repos]  |
| **TOTAL**                 | **X** | **100** | **[Summary]**              |

## Classification: [High/Medium/Low]

## Stage-by-Stage Impact

| Stage            | Impact (0–5) | Notes                                  |
| ---------------- | ------------ | -------------------------------------- |
| issue-pickup     | ?            | [How it affects this stage, if at all] |
| feature-planning | ?            |                                        |
| feature-dev      | ?            |                                        |
| feature-validate | ?            |                                        |
| pr-create        | ?            |                                        |
| pr-merge         | ?            |                                        |

## Implementation Approach

[Concrete steps to integrate this feature into the pipeline]

## Risks & Concerns

[Known limitations, safety implications, or potential blockers]

## Integration Priority

[Recommendation: adopt / defer / skip / monitor]

## Decision Rationale

[Why the team should/shouldn't prioritize this feature right now]
```

---

## Using Assessments in Release-Watch

The `nightgauge-release-watch` skill uses assessment data to drive automatic issue creation:

**Phase 5 (Scoring) in release-watch:**

1. Parse release notes, classify changes (feature, fix, breaking, deprecation, improvement)
2. **Quick-pass filtering:** Score each change with simple keyword matching (existing release-watch logic)
   - If max score ≥ 50, flag for full assessment
3. **Full assessment:** For high-scoring changes, apply framework from this document
   - Determine which dimensions are relevant
   - Score each dimension
   - Calculate composite score
4. **Decision:** If composite ≥ 70, auto-create GitHub issue with assessment as context

**Example flow:**

```
Release: Claude Code v2.1.82

Change: "Added `--bare` flag for scripted invocations"
Quick-pass score: 50+ (matches keyword "--bare", "scripted")
↓
Full Assessment:
  • Dimension 1 (stage impact): 4 (helps with issue-pickup and feature-validate stages)
  • Dimension 2 (automation): 8 (reduces manual invocation setup)
  • Dimension 3 (safety): 3 (no safety impact)
  • Dimension 4 (DX): 6 (slight convenience, not structural change)
  • Dimension 5 (complexity): 8 (just a new flag, minimal integration)
  • Dimension 6 (cross-repo): 9 (applies to all repos equally)
  Total: 38 points → **MEDIUM** (40-69 range)
↓
Action: Add to backlog for next planning cycle, monitor for future relevance
```

---

## Real-World Assessment Examples

### Assessment 1: Computer Use Feature

**Score breakdown:**

- Pipeline Stage Impact: 8 (high value in feature-validate, low elsewhere)
- Automation Potential: 3 (enables testing, not automation)
- Safety & Reliability: 5 (good intent, but high risk without safety infrastructure)
- Developer Experience: 10 (enables new visual workflows)
- Implementation Complexity: 1 (massive: requires safety framework, dedicated hardware)
- Cross-Repo Applicability: 5 (niche to Flutter + VSCode extension)

**Total: 32 points → LOW**

**Decision:** Defer autonomous pipeline usage; pilot with manual workflows first.

---

### Assessment 2: Scheduled Tasks Feature

**Score breakdown:**

- Pipeline Stage Impact: 0 (orthogonal to stage workflow)
- Automation Potential: 18 (enables fully autonomous daily monitoring)
- Safety & Reliability: 8 (improves reliability of repeated checks)
- Developer Experience: 5 (minor convenience)
- Implementation Complexity: 7 (requires orchestrator changes, moderate effort)
- Cross-Repo Applicability: 10 (applies equally to all repos)

**Total: 48 points → MEDIUM**

**Decision:** Add to the next planning cycle; useful but not critical.

---

### Assessment 3: Permission Relay Feature

**Score breakdown:**

- Pipeline Stage Impact: 7 (affects all stages, minor benefit each)
- Automation Potential: 8 (enables safer autonomous execution)
- Safety & Reliability: 12 (critical for permission governance)
- Developer Experience: 8 (reduces permission prompts)
- Implementation Complexity: 7 (config-driven, moderate integration)
- Cross-Repo Applicability: 10 (universal permission rules)

**Total: 52 points → MEDIUM**

**Decision:** Add to backlog; revisit after agent teams integration.

---

## Keyword → Dimension Mapping

When applying focus-based score boosts in the release-watch skill, quick-pass
keywords are mapped to assessment dimensions. This mapping bridges the fast
heuristic (keyword matching) and the assessment-engine semantics.

**Mapping table:**

| Keyword Group                                                                                      | Primary Dimension      | Notes                                                    |
| -------------------------------------------------------------------------------------------------- | ---------------------- | -------------------------------------------------------- |
| auth, permission, security, sandbox, privacy, scope, vulnerability, secret, encrypt, sanitize, CVE | `safety_reliability`   | Maps to Dimension 3 (Safety & Reliability, 0–15 pts)     |
| tool, mcp, agent, command, skill, context, ability, plugin, server                                 | `pipeline_stage`       | Maps to Dimension 1 (Pipeline Stage Impact, 0–30 pts)    |
| performance, speed, token, cost, cache, optimize, efficient, reduce                                | `automation_potential` | Maps to Dimension 2 (Automation Potential, 0–20 pts)     |
| ux, experience, ergonomic, friction, ui, usability, onboard, interface                             | `developer_experience` | Maps to Dimension 4 (Developer Experience, 0–15 pts)     |
| cross, multi-repo, workspace, integration, ecosystem                                               | `cross_repo`           | Maps to Dimension 6 (Cross-Repo Applicability, 0–10 pts) |

**How boosts are applied:**

The active focus lens (from `.nightgauge/focus.yaml`) defines a
`ScoringBoosts` map of `dimension → bonus_points`. When a change's text
(description + tags) matches keywords for a dimension that has a boost, the
bonus is added to the base quick-pass score.

```
adjusted_score = min(base_score + sum(ScoringBoosts[dim] for matched dims), 100)
```

**Example (security lens):**

```
ScoringBoosts: {"safety_reliability": 15, "cross_repo": 5}

Change: "Fixed authentication bypass vulnerability in permission model"
Keywords matched: auth → safety_reliability, permission → safety_reliability
Dimensions matched: {safety_reliability}
Boost: +15
Base score: 55 → Adjusted: 70
```

**Backward compatibility:** If no `focus.yaml` exists, or the active lens is
`general` (the default), no boosts are applied and scores are identical to the
pre-focus behavior.

---

## Customization & Extension

Organizations can customize this framework by:

1. **Adjusting dimension weights** — Change the max points per dimension to reflect organization priorities
2. **Adding organization-specific dimensions** — E.g., "Cost Impact" for organizations where compute cost is critical
3. **Changing thresholds** — Adjust High/Medium/Low cutoffs to match organizational planning cycles
4. **Refining scoring rubric** — Break down "5" scoring further into 5a, 5b, etc. for more granularity

See the `assessment-engine.md` customization section (future) for implementation guidance.

---

## References

- **Release-Watch Skill:** [skills/nightgauge-release-watch/SKILL.md](../SKILL.md)
- **Assessment Template:** [skills/nightgauge-release-watch/assessments/TEMPLATE.md](./assessments/TEMPLATE.md)
- **Historical Assessments:** [skills/nightgauge-release-watch/assessments/](./assessments/)
- **Scoring Decisions:** See individual assessment files for rationale behind specific scores

---

**Author:** nightgauge
**Version:** 1.0.0
**License:** Apache-2.0
**Last Updated:** 2026-07-21
