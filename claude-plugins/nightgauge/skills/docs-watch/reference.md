# Docs Watch — Relevance Categorization & Scoring

Reference data for the `nightgauge-docs-watch` skill. Used to classify
documentation changes by their relevance to the Nightgauge pipeline.

## Relevance Categories

### High Relevance

Changes in these areas directly affect pipeline operation, agent behavior, or
skill execution. **Action recommended** — review and potentially update pipeline
code.

| URL Path Segment  | Why It Matters                                   |
| ----------------- | ------------------------------------------------ |
| `sub-agents`      | Pipeline stages run as isolated sub-agents       |
| `sub-agent`       | Alternate naming for sub-agent docs              |
| `skills`          | Skills are the core pipeline instruction format  |
| `hooks`           | Pre/post hooks affect pipeline execution flow    |
| `headless`        | Pipeline runs in headless mode                   |
| `mcp`             | MCP servers extend agent capabilities            |
| `plugins`         | Plugin system affects skill invocation           |
| `permissions`     | Permission changes affect what agents can do     |
| `settings`        | Settings changes affect agent configuration      |
| `sdk`             | SDK changes affect programmatic agent control    |
| `agent`           | Core agent behavior changes                      |
| `automation`      | Automation features affect CI/CD integration     |
| `custom-commands` | Custom command system affects skill registration |
| `tool`            | Tool availability affects agent capabilities     |
| `context`         | Context management affects pipeline handoff      |
| `max-turns`       | Turn limits affect pipeline stage execution      |
| `model`           | Model selection affects pipeline routing         |

### Medium Relevance

Changes in these areas are useful for pipeline improvement but do not require
immediate action. **Review recommended** on next planning cycle.

| URL Path Segment   | Why It Matters                                       |
| ------------------ | ---------------------------------------------------- |
| `cli-reference`    | CLI flag changes may enable new pipeline features    |
| `cli`              | General CLI documentation                            |
| `common-workflows` | Workflow patterns may suggest pipeline improvements  |
| `best-practices`   | Best practices may reveal optimization opportunities |
| `tools-reference`  | Tool reference updates for available capabilities    |
| `github-actions`   | CI integration may have new capabilities             |
| `configuration`    | Configuration options may affect pipeline setup      |
| `memory`           | Memory management affects long-running pipelines     |
| `context-window`   | Context window changes affect stage planning         |
| `cost`             | Cost changes affect pipeline economics               |
| `api`              | API changes for programmatic integration             |
| `git`              | Git integration changes                              |
| `terminal`         | Terminal tool changes                                |
| `testing`          | Testing patterns for validation stage                |

### Low Relevance

Changes in these areas are informational only. No pipeline action expected.

| URL Path Segment  | Why It Matters                                      |
| ----------------- | --------------------------------------------------- |
| `authentication`  | Auth setup — one-time configuration                 |
| `data-usage`      | Data policy — legal/compliance only                 |
| `legal`           | Legal terms — compliance only                       |
| `troubleshooting` | Troubleshooting guides — reference only             |
| `desktop`         | Desktop app — not used in pipeline (headless)       |
| `chrome`          | Browser extension — not used in pipeline            |
| `voice`           | Voice features — not used in pipeline               |
| `pricing`         | Pricing info — financial planning only              |
| `changelog`       | General changelog — covered by specific page checks |
| `getting-started` | Onboarding — one-time reference                     |
| `faq`             | FAQ — general reference                             |
| `privacy`         | Privacy policy — compliance only                    |

## Scoring Criteria

A documentation change warrants a GitHub issue when ALL of the following are
true:

1. **Relevance is High** — the page URL matches a high-relevance path segment
2. **Change is Substantive** — the content hash changed (not just formatting) or
   the page is entirely new
3. **No Duplicate Exists** — no open issue with label `claude-code-feature`
   already covers this page

### Issue Priority Mapping

| Change Type     | Relevance | Issue Priority         |
| --------------- | --------- | ---------------------- |
| New page        | High      | `priority:high`        |
| Content changed | High      | `priority:medium`      |
| New page        | Medium    | `priority:low`         |
| Content changed | Medium    | No issue (report only) |
| Any             | Low       | No issue (report only) |

### Issue Size Mapping

| Estimated Impact     | Issue Size |
| -------------------- | ---------- |
| New capability       | `size:M`   |
| Behavior change      | `size:S`   |
| Configuration change | `size:XS`  |

## Issue Template

```markdown
## Summary

A {change_type} was detected in Claude Code documentation that may affect the
Nightgauge pipeline.

**Page:** {url}
**Change Type:** {new|changed}
**Relevance:** High
**Content Hash:** {hash}

## Details

{brief_summary_of_page_content}

## Action Items

- [ ] Review the documentation change at {url}
- [ ] Assess impact on pipeline stages: {likely_affected_stages}
- [ ] Implement any necessary updates
- [ ] Update relevant skill files if behavior changed

## Affected Pipeline Areas

{list_of_potentially_affected_components}

## Source

Auto-detected by `/nightgauge:docs-watch` on {timestamp}
```

## URL-to-Pipeline-Component Mapping

When a high-relevance page changes, map it to likely affected pipeline
components:

| URL Contains  | Likely Affected Components                           |
| ------------- | ---------------------------------------------------- |
| `sub-agent`   | HeadlessOrchestrator, stage isolation, context files |
| `skills`      | SKILL.md format, skill registration, invocation      |
| `hooks`       | Pre/post pipeline hooks, validation gates            |
| `headless`    | HeadlessOrchestrator, CI execution mode              |
| `mcp`         | MCP server config, tool availability                 |
| `permissions` | Security settings, tool allow-lists                  |
| `settings`    | config.yaml, .claude/settings.json                   |
| `sdk`         | nightgauge-sdk, programmatic API                     |
| `model`       | Model routing, cost optimization                     |
| `context`     | Context files, handoff architecture                  |
| `max-turns`   | Stage execution limits, timeout handling             |
| `tool`        | allowed-tools in SKILL.md, tool availability         |

---

## Release Correlation Fields (Optional)

When `--correlate-releases` is enabled, detected changes include release
correlation metadata in the snapshot and reports. These fields are **optional**
for backward compatibility with existing snapshots.

### Snapshot Format Enhancement

```json
{
  "pages": {
    "https://code.claude.com/docs/hooks": {
      "hash": "sha256-hash...",
      "last_seen": "2026-03-24T00:00:00Z",
      "relevance": "high",
      "correlated_release": "2.1.81",
      "correlation_confidence": "high",
      "correlation_method": "timestamp_match"
    }
  }
}
```

### Correlation Fields Reference

| Field                    | Type                        | Description                                                                                  |
| ------------------------ | --------------------------- | -------------------------------------------------------------------------------------------- |
| `correlated_release`     | string or null              | Claude Code version (e.g., "2.1.81") that correlates with this change                        |
| `correlation_confidence` | "high" \| "medium" \| "low" | Confidence level of the correlation (high: ≥80%, medium: 50-79%, low: <50%)                  |
| `correlation_method`     | string                      | How correlation was determined: `timestamp_match`, `url_pattern`, `content_match`, or `none` |

### Report Enhancement

JSON reports include a `release_correlation` summary section (when
`--correlate-releases` is enabled):

```json
{
  "release_correlation": {
    "enabled": true,
    "current_release": "2.1.81",
    "correlated_count": 3,
    "high_confidence_count": 1,
    "medium_confidence_count": 1,
    "low_confidence_count": 1,
    "correlation_summary": {
      "timestamp_match": 2,
      "url_pattern": 0,
      "content_match": 0,
      "none": 0
    }
  }
}
```

For detailed documentation on how release correlation works, confidence scoring,
and deduplication with release-watch, see:
[correlation-reference.md](./correlation-reference.md)

---

**Author:** nightgauge **License:** Apache-2.0
