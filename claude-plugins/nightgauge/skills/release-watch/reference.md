# Release Watch — Pipeline Relevance Scoring Guide

Reference data for the `nightgauge-release-watch` skill. Used to classify
Claude Code release changes by their relevance to the Nightgauge pipeline.

## Release Classification System

Each change in a Claude Code release is classified by **type** and scored by
**pipeline relevance**.

### Change Types

| Type          | Indicator             | When It Appears                | Pipeline Impact                                |
| ------------- | --------------------- | ------------------------------ | ---------------------------------------------- |
| `feature`     | "Added"               | New capabilities               | Likely high — may enable new pipeline features |
| `fix`         | "Fixed"               | Bug resolutions                | Medium — check if pipeline is affected         |
| `breaking`    | "Breaking:"           | Non-backward-compatible change | **Always high** — requires planning            |
| `deprecation` | "Deprecated:"         | Planned removal                | **Always high** — requires migration plan      |
| `improvement` | "Improved", "Changed" | Enhancement or tweak           | Medium to low — depends on area                |

---

## Relevance Scoring Rules

Pipeline relevance is a **0-100 score** based on the affected area. Multiple
criteria can combine to increase a score.

### Scoring Criteria

| Criterion                    | Points | When Applied                                                                                             | Examples                           |
| ---------------------------- | ------ | -------------------------------------------------------------------------------------------------------- | ---------------------------------- |
| Affects invocation/scripting | +20    | Keywords: `--bare`, `--channels`, `scripted`, `hook`, `api-key`                                          | New scripted execution flags       |
| New capabilities for stages  | +25    | Keywords: `tool`, `bash`, `read`, `write`, `edit`, `plan`, `agent`, `skill`, `command`, `mcp`, `context` | New MCP servers, tool improvements |
| Changes permissions/security | +15    | Keywords: `permission`, `auth`, `security`, `allow-list`, `sandbox`, `scope`, `org`                      | Permission relay feature           |
| Affects MCP/tool system      | +20    | Keywords: `mcp`, `server`, `plugin`, `custom-command`, `connection`, `token`                             | New OAuth support, server changes  |
| Affects model availability   | +20    | Keywords: `model`, `claude`, `router`, `capacity`, `deprecated`, `available`                             | Model deprecations, new models     |
| Is a breaking change         | ≥70    | Change type == `breaking`                                                                                | Non-backward-compatible API change |
| Is a deprecation             | ≥60    | Change type == `deprecation`                                                                             | Scheduled feature removal          |

**Score Cap:** 100 (clamped to prevent overflow)

### Scoring Examples

**Example 1: New VSCode extension feature (low relevance)**

- Text: "Fixed unnecessary permission prompts for Bash commands"
- Matches: "permission" (+15), "Bash" (indirect tool +10)
- **Score: 25** (Review, but not urgent)

**Example 2: New scripting capability (high relevance)**

- Text: "Added `--bare` flag for scripted `-p` calls"
- Matches: "Added" (feature), "--bare" (+20), "scripted" (+20), "calls" (invocation)
- **Score: 60+** (Action recommended)

**Example 3: Breaking change (highest relevance)**

- Text: "Breaking: Removed legacy hooks API"
- Type: `breaking`
- **Score: 70+** (Immediate action required)

---

## Pipeline Impact Areas

When a change scores high, map it to affected pipeline components:

| Release Area            | Affected Pipeline Component                        | Action                                |
| ----------------------- | -------------------------------------------------- | ------------------------------------- |
| Sub-agents, headless    | `HeadlessOrchestrator`, stage isolation            | Review execution mode compatibility   |
| Skills, SKILL.md format | Skill registration, invocation patterns            | Update skill frontmatter if needed    |
| Hooks (pre/post)        | `feature-validate` gate system, custom hooks       | Check if hook signatures changed      |
| MCP, servers            | Tool availability in stages, external integrations | Update MCP config, test tool calls    |
| Permissions             | Allowed-tools, sandbox restrictions                | Update SKILL.md allowed-tools lists   |
| Settings, config        | `.claude/settings.json`, config.yaml               | Update configuration docs             |
| SDK, APIs               | `@nightgauge/sdk`, programmatic control            | Update SDK integration tests          |
| Models                  | Model routing, capacity planning                   | Update cost models, retry logic       |
| Context                 | JSON context files, handoff architecture           | Check if context schema changed       |
| CLI                     | New flags, command structure                       | Test new flags in pipeline automation |
| VSCode                  | Extension behavior, tree views, diagnostics        | Test extension integration            |

---

## Relevance Categories

### High Relevance (Score ≥ 70)

**Action Required.** These changes directly affect how the pipeline operates,
what capabilities it has, or how it invokes Claude Code.

- Breaking changes to APIs or CLI
- Deprecations of features the pipeline uses
- New scripting/automation capabilities
- Permission model changes
- New tools or MCP server capabilities
- Model availability changes or deprecations

**When you see a high-relevance release:**

1. Read the full release notes on GitHub
2. Identify which pipeline components are affected
3. Create or update a GitHub issue for planning
4. Plan implementation timeline (especially for breaking changes)
5. Update relevant SKILL.md, config, or code

### Medium Relevance (Score 40–69)

**Review Recommended.** These changes are useful for pipeline improvement but
don't require immediate action. Consider on next planning cycle.

- New features that could enhance stages
- Improvements to existing capabilities
- Bug fixes in features the pipeline uses
- Configuration option changes
- Tool or MCP improvements (non-critical)

**When you see a medium-relevance release:**

1. Skim the release notes for keywords
2. Assess if improvement applies to current work
3. Note for future optimization planning
4. No urgent action required

### Low Relevance (Score < 40)

**Informational Only.** These changes are not immediately relevant to pipeline
operation.

- Desktop app features (pipeline is headless)
- UI improvements that don't affect CLI
- VSCode extension features unrelated to pipeline
- Configuration tweaks
- General bug fixes
- Documentation updates

**When you see a low-relevance release:**

- Archive the report
- No action required

---

## High-Relevance Keywords (Trigger Immediate Review)

Watch for these keywords in release notes — they almost always indicate
high-relevance changes:

| Keyword      | Reason                   | Example                     |
| ------------ | ------------------------ | --------------------------- |
| `--bare`     | Scripted execution mode  | Enables CI/CD automation    |
| `breaking`   | Non-backward-compatible  | Requires code update        |
| `deprecated` | Planned removal          | Requires migration plan     |
| `permission` | Security/access model    | May affect sandbox behavior |
| `sub-agent`  | Pipeline stage isolation | Core pipeline feature       |
| `hook`       | Pre/post execution       | Affects validation gates    |
| `headless`   | Automated execution      | Pipeline-specific mode      |
| `mcp`        | External tool access     | New capabilities for stages |
| `model`      | AI capability            | Cost/performance impact     |
| `tool`       | CLI tool access          | May enable new use cases    |
| `auth`       | Authentication           | Session/token management    |
| `api`        | API changes              | SDK compatibility           |
| `token`      | Token usage              | Cost/budget impact          |

---

## Issue Creation Criteria

Issues are auto-created (with `--create-issues`) when ALL of these are true:

1. **Release max relevance score ≥ 70**
2. **Release is not already tracked** (no open issue with label `claude-code-release`)
3. **User did not pass `--dry-run`**

### Auto-Generated Issue Template

```markdown
## Summary

New Claude Code release v{DATE} contains {COUNT} high-relevance changes
affecting the Nightgauge pipeline.

## Changes

{List of changes with relevance scores >= 50}

## Action Items

- [ ] Review the full release notes
- [ ] Assess impact on affected pipeline stages
- [ ] Plan implementation timeline (especially for breaking changes)
- [ ] Update relevant SKILL.md or configuration files
- [ ] Test updated pipeline in pre-prod

## Related Links

- Release: https://github.com/anthropics/claude-code/releases/tag/v{VERSION}
- Skill: skills/nightgauge-release-watch/SKILL.md
```

---

## Versioning & Comparison

Release versions follow semantic versioning: `MAJOR.MINOR.PATCH`

- `v2.1.81` > `v2.1.75` (patch update)
- `v2.2.0` > `v2.1.99` (minor update)
- `v3.0.0` > `v2.99.99` (major update)

The skill uses Python comparison logic to correctly handle all version formats
(including leading `v`).

---

## Historical Examples

### Example 1: `v2.1.81` (March 2026)

**High-relevance changes detected:**

- `--bare` flag for scripted calls (+20) — invocation change
- `--channels` permission relay (+20) — permission model
- MCP OAuth updates (+20) — tool system
- VSCode regression fix (low)

**Result:** Issue #2385 auto-created, assessed immediately

### Example 2: Version with mostly bug fixes

**Medium/Low-relevance changes:**

- Fixed race condition in background tasks (low)
- Fixed terminal tab title update (low)
- Fixed plugin hooks blocking submission (low)

**Result:** Minimal report, no issue created

### Example 3: Breaking change release

**Change:** "Breaking: Removed legacy sub-agent API"

**Score:** ≥ 70 (auto-classified as breaking)

**Result:** Issue created, flagged for immediate review and planning

---

## Update Cadence Recommendations

| Frequency                | When                | Why                          |
| ------------------------ | ------------------- | ---------------------------- |
| After every CC release   | Weekly or bi-weekly | Catch new features early     |
| First thing each Monday  | Weekly routine      | Batch weekly updates         |
| Before pipeline releases | Ad-hoc              | Check for breaking changes   |
| End of quarter           | Quarterly review    | Assess deprecation timelines |

---

## Related Skills & Tools

- **[nightgauge-docs-watch](../nightgauge-docs-watch/SKILL.md)** — Monitor Claude Code **documentation** (different from releases)
- **[nightgauge-pipeline-audit](../nightgauge-pipeline-audit/SKILL.md)** — Analyze pipeline efficiency metrics
- **[nightgauge-continuous-improvement](../nightgauge-continuous-improvement/SKILL.md)** — Unified improvement review

---

**Author:** nightgauge **License:** Apache-2.0
