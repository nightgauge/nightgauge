# Docs Watch — Release Correlation Reference

Reference documentation for the release correlation feature of the
`nightgauge-docs-watch` skill. Explains how documentation changes are
cross-referenced with Claude Code releases, confidence scoring, and deduplication
with the release-watch skill.

## Overview

Release correlation enriches documentation changes with release version context,
enabling better assessment of which features were documented in which releases.
This helps answer questions like: "When was this feature first documented?" and
"Did the docs change in the same release that introduced the feature?"

**Design principle:** Documentation is often updated alongside feature releases.
Correlating changes helps distinguish between:

- Docs for newly-released features (correlation → same release)
- Docs updates for existing features (correlation → prior release)
- Docs that changed for reasons unrelated to code changes (correlation → none)

---

## Correlation Methods

### Method 1: Timestamp Matching (Highest Priority)

**How it works:** If a documentation change is detected within ±7 days of a
Claude Code release date, correlate the change with that release.

**Confidence scoring:**

- **High confidence (≥ 95%)** — Change detected within ±1 day of release
  - Indicates: Docs were published as part of release bundle
  - Example: New feature page added on 2026-03-24, v2.1.82 released 2026-03-24
- **Medium confidence (60–94%)** — Change detected 2–7 days after release
  - Indicates: Docs were updated shortly after release (post-release cleanup common)
  - Example: Feature released 2026-07-19, docs updated 2026-07-21
- **Low confidence (< 60%)** — No matching release within ±7 days
  - Indicates: Change unrelated to recent releases OR release too old

**Tolerance window:** ±7 days (configurable)

- Past: Check releases up to 7 days before detected change
- Future: Check releases up to 7 days after detected change
- Rationale: Docs are often updated before or after release dates

**Lookup source:** `.nightgauge/release-watch/reports/*.json`

- Queries the last 5 release reports (most recent first)
- Extracts `published_at` timestamp from each release entry
- Compares against detected change date

### Method 2: URL Pattern Matching (Medium Priority)

**How it works:** If the documentation page URL contains a version reference
(e.g., `/v2.1/`, `/2.1.81/`, `/versions/2.1/`), extract and correlate with
that version.

**Confidence scoring:**

- **Medium confidence (60–79%)** — URL version extracted successfully
  - Indicates: Docs are versioned and page URL directly references release
  - Example: `https://code.claude.com/docs/v2.1.81/sub-agents`
- **Pattern:** Matches `/(v?\d+\.\d+(?:\.\d+)?)[/]?` in URL path
  - Extracts: `v2.1.81` → `2.1.81`
  - Handles: `v2.1`, `2.1.81`, `v2.1.81`, `versions/2.1`

**Use case:** When Claude Code docs use versioned URLs (future enhancement)

### Method 3: Content Matching (Lowest Priority)

**How it works:** Parse the page content and search for feature keywords from
recent releases. If keywords match, correlate with that release.

**Current status:** Not yet implemented (Phase 5.5 future enhancement)

**Confidence scoring (when implemented):**

- **Low–Medium confidence (40–70%)** — Keyword match found
  - Indicates: Page mentions feature from release, but doesn't guarantee
    correlation (docs may reference old features)
  - Example: "sub-agents" mentioned in page → matches "sub-agents" feature in
    v2.1.81 release notes

**Algorithm (proposed):**

1. Parse release notes to extract feature keywords (50+ chars of description)
2. For each changed page, tokenize and search for keyword matches
3. Score based on keyword frequency and position (title > intro > body)
4. Threshold: Match ≥2 significant keywords or 1 exact phrase match

---

## Confidence Levels & Interpretation

| Level      | Range   | Interpretation                          | Recommended Action                             |
| ---------- | ------- | --------------------------------------- | ---------------------------------------------- |
| **High**   | 80–100% | Docs definitely updated in this release | Trust the correlation; act on it               |
| **Medium** | 50–79%  | Docs likely related to this release     | Use cautiously; cross-check with release notes |
| **Low**    | 0–49%   | Docs possibly related, but unclear      | Don't rely on; treat as informational only     |

---

## Edge Cases & Handling

### Multiple Matching Releases

If a documentation change falls within the window of multiple releases (rare),
priority order:

1. Exact match (timestamp within ±1 day) → High confidence
2. Closest release within ±7 days → Medium confidence
3. Oldest release in window (fallback) → Medium confidence

### Changed Pages Without Correlation

If a page is detected as changed but no correlation found:

```json
{
  "url": "https://code.claude.com/docs/troubleshooting",
  "type": "changed",
  "relevance": "low",
  "correlated_release": null,
  "correlation_confidence": "low",
  "correlation_method": "none"
}
```

**Interpretation:** Page changed, but not aligned with recent releases. Likely:

- Internal documentation updates (process changes, clarifications)
- Bug fixes to docs themselves (typos, formatting)
- Policy/legal updates (unrelated to feature releases)

### Missing Release State

If `.nightgauge/release-watch/last-seen.json` doesn't exist:

```bash
if [ ! -f "$RELEASE_STATE_FILE" ]; then
  echo "Release state not found; skipping correlation"
  # Continue with regular analysis, skip correlation
fi
```

**Behavior:** All changes annotated with:

```json
{
  "correlated_release": null,
  "correlation_confidence": "low",
  "correlation_method": "none"
}
```

---

## Deduplication with Release-Watch

Both skills can detect the same feature:

- **docs-watch** → Finds documentation page for feature
- **release-watch** → Finds feature announcement in release notes

### Deduplication Strategy

**Scenario:** Both skills detect "Agent Teams" feature

```
1. release-watch runs first (or already has reports)
   → Creates issue: "feat: Agent Teams (assessment score: 75)"
   → Stored in: .nightgauge/release-watch/

2. docs-watch runs and finds related page changes
   → Correlates changes to same release
   → INSTEAD of creating duplicate issue:
      a. Check if assessment already exists for this feature
      b. If yes → Add documentation_context section to existing assessment
      c. If no → Create new issue with both release + doc context
```

### Assessment Enrichment Format

When docs-watch enriches an existing assessment (via `--enrich-assessments`):

```json
{
  "feature": "Agent Teams",
  "version": "2.1.81",
  "assessment": {/* existing assessment from release-watch */},
  "documentation_context": {
    "pages": [
      {
        "url": "https://code.claude.com/docs/sub-agents",
        "type": "new",
        "relevance": "high",
        "correlated_confidence": "high",
        "correlation_method": "timestamp_match",
        "summary": "New sub-agent capabilities..."
      }
    ],
    "total_doc_pages": 1,
    "high_relevance_pages": 1,
    "enriched_by": "docs-watch",
    "enriched_at": "ISO-8601"
  }
}
```

### Cross-Skill Communication

**Release-watch → docs-watch:**

- Publishes reports to: `.nightgauge/release-watch/reports/`
- docs-watch reads these reports to find release dates

**docs-watch → release-watch:**

- Writes assessment enrichments to: `.nightgauge/assessments/` (future)
- Format compatible with release-watch's assessment schema

---

## Configuration

### Enabling/Disabling Correlation

#### Enable (default if release state exists)

```bash
/nightgauge:docs-watch --correlate-releases
```

#### Disable

```bash
/nightgauge:docs-watch --skip-release-correlation
```

#### Use case for disabling:

- Running in offline CI environment
- Release-watch state is stale (e.g., > 30 days old)
- Debugging docs-watch changes independent of releases

### Controlling Confidence Thresholds

Current hardcoded thresholds:

- High confidence: ±1 day timestamp match
- Medium confidence: 2–7 days timestamp match
- Low confidence: No match or URL pattern match

**Future enhancement:** Make thresholds configurable via `.nightgauge/config.yaml`

---

## Snapshot Format Changes

### Before (v1.0 format)

```json
{
  "pages": {
    "https://code.claude.com/docs/hooks": {
      "hash": "abc123...",
      "last_seen": "2026-03-20T00:00:00Z",
      "relevance": "high"
    }
  }
}
```

### After (v1.1+ format, backward compatible)

```json
{
  "pages": {
    "https://code.claude.com/docs/hooks": {
      "hash": "abc123...",
      "last_seen": "2026-03-20T00:00:00Z",
      "relevance": "high",
      "correlated_release": "2.1.81",
      "correlation_confidence": "high",
      "correlation_method": "timestamp_match"
    }
  }
}
```

**Backward compatibility:** New fields are optional. Old snapshots without
correlation data are still valid and will work unchanged. When those pages are
checked again, correlation data will be added.

---

## Report Output Changes

### JSON Report Addition

Old reports include `findings` array. New reports add `release_correlation`
summary section:

```json
{
  "check_date": "2026-03-24T12:00:00Z",
  "findings": [/* existing */],
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

### Markdown Report Enhancement

High-relevance findings now include release correlation badge:

```markdown
## HIGH RELEVANCE (action recommended)

[NEW] https://code.claude.com/docs/sub-agents
→ New sub-agent capabilities may affect pipeline stage isolation
**Correlated with v2.1.81** (high confidence, timestamp match)
```

---

## Testing Correlation

### Manual Test Case

1. **Setup:** Have a known release (e.g., v2.1.81) with docs
2. **Add a documentation page** on the same day as release
3. **Run docs-watch:** `./nightgauge:docs-watch --correlate-releases`
4. **Expected:** Page correlated with high confidence

### Unit Test Examples

```bash
# Test timestamp matching
assert_equals(
  correlate_change(
    url="https://...",
    last_seen="2026-03-24T12:00:00Z",
    release_date="2026-03-24T08:00:00Z"
  ),
  {"confidence": "high", "method": "timestamp_match"}
)

# Test URL pattern matching
assert_equals(
  correlate_change(
    url="https://code.claude.com/docs/v2.1.81/hooks",
    ...
  ),
  {"correlated_release": "2.1.81", "method": "url_pattern"}
)
```

---

## Troubleshooting

### Correlation Not Working

**Symptom:** All changes show `correlated_release: null`

**Causes:**

1. `.nightgauge/release-watch/last-seen.json` doesn't exist
2. No release reports in `.nightgauge/release-watch/reports/`
3. `--skip-release-correlation` flag passed

**Solution:**

- Run release-watch first: `./nightgauge:release-watch`
- Check state file exists: `cat .nightgauge/release-watch/last-seen.json`
- Verify reports directory: `ls .nightgauge/release-watch/reports/`

### Wrong Release Correlated

**Symptom:** Change correlated with v2.1.80 instead of v2.1.81

**Causes:**

1. Timestamp falls within window of both releases
2. Release dates in reports are close together (< 7 days)

**Solution:**

- Check release dates: `jq '.releases[].published_at' < reports/report-*.json`
- Tighten tolerance window (future config option)
- Manually check which release date is closer

### High Confidence When Should Be Medium

**Symptom:** `correlation_confidence: high` but timestamp is 3 days apart

**Debug:**

1. Check actual timestamps: `jq '.pages[].last_seen' < index.json`
2. Check release date: `jq '.releases[].published_at' < reports/report-*.json`
3. File a bug if mismatch

---

## References

- **docs-watch Skill:** [skills/nightgauge-docs-watch/SKILL.md](./SKILL.md)
- **release-watch Skill:** [skills/nightgauge-release-watch/SKILL.md](../nightgauge-release-watch/SKILL.md)
- **Assessment Engine:** [skills/nightgauge-release-watch/assessment-engine.md](../nightgauge-release-watch/assessment-engine.md)
- **Relevance Categories:** [skills/nightgauge-docs-watch/reference.md](./reference.md)

---

**Author:** nightgauge
**Version:** 1.0.0
**License:** Apache-2.0
**Last Updated:** 2026-03-24
