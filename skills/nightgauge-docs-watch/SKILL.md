---
name: nightgauge-docs-watch
description: Monitor Claude Code documentation for new features and changes relevant to the
  pipeline. Use when checking for Claude Code updates or when a new CC version is
  released.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Bash Read Write Edit Glob Grep
---

# Nightgauge Docs Watch

## Description

Monitors Claude Code documentation at `https://code.claude.com/docs/` for new
pages, removed pages, and content changes. Compares the current state against a
locally stored snapshot and categorizes findings by relevance to the
Nightgauge pipeline.

**Use Cases:**

- Detecting new Claude Code features relevant to the pipeline
- Tracking documentation changes after a Claude Code release
- Auto-creating GitHub issues for high-relevance findings

**When to Use:**

- After a new Claude Code version is released
- On a periodic cadence (weekly or bi-weekly)
- When you suspect new features may be available

## Invocation

| Tool        | Command                                                |
| ----------- | ------------------------------------------------------ |
| Claude Code | `/nightgauge:docs-watch [options]`                     |
| Copilot     | Invoke via Agent Skills extension                      |
| Cursor      | Run via Agent Skills or direct SKILL.md                |
| Standalone  | `claude --skill skills/nightgauge-docs-watch/SKILL.md` |

## Arguments

| Argument                     | Description                                          | Default  |
| ---------------------------- | ---------------------------------------------------- | -------- |
| `--create-issues`            | Auto-create GitHub issues for high-relevance changes | `false`  |
| `--dry-run`                  | Show what issues would be created without creating   | `false`  |
| `--force-refresh`            | Re-fetch all pages, not just new/changed ones        | `false`  |
| `--correlate-releases`       | Cross-reference changes with Claude Code releases    | `true`\* |
| `--enrich-assessments`       | Update existing assessment files with doc links      | `false`  |
| `--skip-release-correlation` | Disable release correlation (e.g., for offline CI)   | `false`  |

\*Enabled by default when `.nightgauge/release-watch/last-seen.json` exists; disabled if missing.

### Examples

```bash
# Check for documentation changes (default: compare against last snapshot)
/nightgauge:docs-watch

# Check and auto-create issues for high-relevance changes
/nightgauge:docs-watch --create-issues

# Preview what issues would be created
/nightgauge:docs-watch --create-issues --dry-run

# Force re-fetch all pages and rebuild snapshot
/nightgauge:docs-watch --force-refresh

# Check changes and correlate with release versions
/nightgauge:docs-watch --correlate-releases

# Enrich existing assessment files with documentation context
/nightgauge:docs-watch --enrich-assessments
```

---

## Prerequisites

- `curl` installed (for fetching documentation pages)
- `sha256sum` or `shasum` installed (for content hashing)
- `jq` installed (for JSON processing)
- `gh` CLI installed and authenticated (only for `--create-issues`)
- Git repository with `.nightgauge/` directory

**CRITICAL**: This skill runs headless. Do NOT use AskUserQuestion. Make
autonomous decisions or fail with clear error.

---

## Workflow

### Phase 0: Environment Preflight

<!-- include: ../_shared/PREFLIGHT.md -->

---

### Phase 1: Fetch Current Documentation Index

#### Step 1.1: Fetch the docs index

Fetch the current page list from Claude Code documentation:

```bash
curl -s https://code.claude.com/docs/llms.txt > /tmp/docs-watch-index.txt
```

If the fetch fails, error with a clear message:

```bash
if [ ! -s /tmp/docs-watch-index.txt ]; then
  echo "ERROR: Failed to fetch docs index from https://code.claude.com/docs/llms.txt"
  echo "  Check network connectivity and try again."
  exit 1
fi
```

#### Step 1.2: Extract page URLs

Parse the index to extract all documentation page URLs. The `llms.txt` file
contains URLs (one per line or embedded in text). Extract all URLs matching
`https://code.claude.com/docs/`:

```bash
grep -oE 'https://code\.claude\.com/docs/[a-zA-Z0-9/_-]+' /tmp/docs-watch-index.txt \
  | sort -u > /tmp/docs-watch-urls.txt
PAGE_COUNT=$(wc -l < /tmp/docs-watch-urls.txt | tr -d ' ')
echo "Found ${PAGE_COUNT} documentation pages"
```

---

### Phase 2: Load Stored Snapshot

#### Step 2.1: Load or initialize snapshot

```bash
SNAPSHOT_DIR=".nightgauge/doc-snapshots"
SNAPSHOT_FILE="${SNAPSHOT_DIR}/index.json"

if [ ! -f "$SNAPSHOT_FILE" ]; then
  echo '{"last_check":"1970-01-01T00:00:00Z","page_count":0,"pages":{}}' \
    | jq '.' > "$SNAPSHOT_FILE"
  echo "Initialized new snapshot file at ${SNAPSHOT_FILE}"
fi
```

Read the snapshot into a temp file for comparison:

```bash
cp "$SNAPSHOT_FILE" /tmp/docs-watch-snapshot.json
LAST_CHECK=$(jq -r '.last_check' /tmp/docs-watch-snapshot.json)
echo "Last check: ${LAST_CHECK}"
```

---

### Phase 3: Compare Index Against Snapshot

#### Step 3.1: Detect new, existing, and removed pages

```bash
# Extract known URLs from snapshot
jq -r '.pages | keys[]' /tmp/docs-watch-snapshot.json | sort > /tmp/docs-watch-known.txt

# New pages: in current but not in snapshot
comm -23 /tmp/docs-watch-urls.txt /tmp/docs-watch-known.txt > /tmp/docs-watch-new.txt

# Removed pages: in snapshot but not in current
comm -13 /tmp/docs-watch-urls.txt /tmp/docs-watch-known.txt > /tmp/docs-watch-removed.txt

# Existing pages: in both
comm -12 /tmp/docs-watch-urls.txt /tmp/docs-watch-known.txt > /tmp/docs-watch-existing.txt

NEW_COUNT=$(wc -l < /tmp/docs-watch-new.txt | tr -d ' ')
REMOVED_COUNT=$(wc -l < /tmp/docs-watch-removed.txt | tr -d ' ')
EXISTING_COUNT=$(wc -l < /tmp/docs-watch-existing.txt | tr -d ' ')

echo "New pages: ${NEW_COUNT}"
echo "Removed pages: ${REMOVED_COUNT}"
echo "Existing pages to check: ${EXISTING_COUNT}"
```

---

### Phase 4: Fetch and Hash Content

Use the snapshot-diff script to detect content changes:

```bash
nightgauge docs snapshot-diff \
  --snapshot /tmp/docs-watch-snapshot.json \
  --urls /tmp/docs-watch-urls.txt \
  --json \
  > /tmp/docs-watch-diff.json
```

The script outputs JSON with `new`, `changed`, and `removed` arrays. Each entry
includes the URL and content hash.

If `--force-refresh` is set, treat ALL pages as new (ignore existing hashes).

---

### Phase 5: Categorize Findings by Relevance

Read the reference file for categorization criteria:

```bash
REFERENCE_FILE="${SKILL_DIR}/reference.md"
```

Apply relevance scoring based on page URL path segments. Use the categories
defined in `reference.md`:

**High relevance** (directly affects pipeline operation):

- sub-agents, skills, hooks, headless, mcp, plugins, permissions, settings,
  sdk, agent, automation, custom-commands

**Medium relevance** (useful for pipeline improvement):

- cli-reference, common-workflows, best-practices, tools-reference,
  github-actions, configuration, memory, context

**Low relevance** (informational only):

- authentication, data-usage, legal, troubleshooting, desktop, chrome, voice,
  pricing, changelog

For each new or changed page:

1. Fetch the page content: `curl -s <url>`
2. Generate sha256 hash of the content
3. Match URL path segments against relevance categories
4. Assign relevance: `high`, `medium`, or `low`
5. Extract a brief summary of the page content (first 200 chars or title)

---

### Phase 5.5: Release Correlation (Optional)

Only executes when `--skip-release-correlation` is not set AND
`.nightgauge/release-watch/last-seen.json` exists.

#### Step 5.5.1: Load release state and recent reports

```bash
RELEASE_STATE_FILE=".nightgauge/release-watch/last-seen.json"
REPORTS_DIR=".nightgauge/release-watch/reports"

if [ ! -f "$RELEASE_STATE_FILE" ]; then
  echo "Release state not found; skipping correlation"
  CORRELATE_RELEASES=false
else
  CURRENT_VERSION=$(jq -r '.version // .full_tag' "$RELEASE_STATE_FILE" 2>/dev/null | sed 's/^v//')
  CORRELATE_RELEASES=true
  echo "Will correlate changes with releases (current: ${CURRENT_VERSION})"
fi
```

#### Step 5.5.2: Correlate each detected change

For each new or changed page in the diff results, attempt correlation by:

1. **Timestamp matching** — If the change was detected within ±7 days of a release date,
   correlate with that release (confidence: high if exact match, medium if 3-7 days)
2. **URL pattern matching** — If the page URL contains version references (e.g.,
   `/docs/v2.1/...` or `/docs/2.1.81/...`), extract the version
3. **Content matching** — If the page content includes release notes or changelog,
   search for feature keywords from recent releases

```bash
python3 << 'PYTHON_EOF'
import json
import os
import re
from datetime import datetime, timedelta

def correlate_change(page_url, change_type, last_seen_iso, release_version, reports_dir):
    """Attempt to correlate a documentation change with a release.

    Returns:
    {
        'correlated_release': 'version string or null',
        'correlation_confidence': 'high|medium|low',
        'correlation_method': 'timestamp_match|url_pattern|content_match|none'
    }
    """
    result = {
        'correlated_release': None,
        'correlation_confidence': 'low',
        'correlation_method': 'none'
    }

    # Parse timestamps
    try:
        if last_seen_iso:
            change_date = datetime.fromisoformat(last_seen_iso.replace('Z', '+00:00'))
        else:
            return result
    except:
        return result

    # Method 1: URL pattern matching
    url_pattern = r'/(v?\d+\.\d+(?:\.\d+)?)[/]?'
    match = re.search(url_pattern, page_url)
    if match:
        version_from_url = match.group(1).lstrip('v')
        result['correlated_release'] = version_from_url
        result['correlation_confidence'] = 'medium'
        result['correlation_method'] = 'url_pattern'
        return result

    # Method 2: Timestamp matching (within ±7 days of release)
    # Load release reports to find nearest release date
    if os.path.isdir(reports_dir):
        try:
            reports = [f for f in os.listdir(reports_dir) if f.endswith('.json')]
            reports.sort(reverse=True)  # newest first

            for report_file in reports[:5]:  # check last 5 reports
                try:
                    with open(os.path.join(reports_dir, report_file)) as f:
                        report = json.load(f)

                    for release in report.get('releases', []):
                        release_date_str = release.get('published_at', '')
                        if not release_date_str:
                            continue

                        try:
                            release_date = datetime.fromisoformat(release_date_str.replace('Z', '+00:00'))
                            time_diff = abs((change_date - release_date).days)

                            if time_diff <= 1:  # exact day match
                                result['correlated_release'] = release['version']
                                result['correlation_confidence'] = 'high'
                                result['correlation_method'] = 'timestamp_match'
                                return result
                            elif time_diff <= 7:  # within 7 days
                                result['correlated_release'] = release['version']
                                result['correlation_confidence'] = 'medium'
                                result['correlation_method'] = 'timestamp_match'
                                # Don't return; keep looking for better match
                        except:
                            continue
                except:
                    continue
        except:
            pass

    return result

# Load current diff results
with open('/tmp/docs-watch-diff.json') as f:
    diff = json.load(f)

# Load release state
with open('.nightgauge/release-watch/last-seen.json') as f:
    release_state = json.load(f)

current_version = release_state.get('version', release_state.get('full_tag', '')).lstrip('v')
reports_dir = '.nightgauge/release-watch/reports'

# Enrich each change with correlation data
for entry_list in [diff.get('new', []), diff.get('changed', [])]:
    for entry in entry_list:
        correlation = correlate_change(
            entry['url'],
            'new' if entry in diff.get('new', []) else 'changed',
            entry.get('last_seen'),
            current_version,
            reports_dir
        )
        entry.update(correlation)

# Save enriched diff
with open('/tmp/docs-watch-diff-correlated.json', 'w') as f:
    json.dump(diff, f, indent=2)

print(f"Correlated {len(diff.get('new', [])) + len(diff.get('changed', []))} changes with releases")
PYTHON_EOF

# Use correlated diff for subsequent phases
cp /tmp/docs-watch-diff-correlated.json /tmp/docs-watch-diff.json
```

---

### Phase 6: Generate Report

#### Step 6.1: Build findings summary

```
CLAUDE CODE DOCS WATCH REPORT
═══════════════════════════════════════════════════════════
Check Date:    2026-03-19
Last Check:    2026-03-12
Pages Tracked: 72

CHANGES DETECTED
───────────────────────────────────────────────────────────
  New Pages:     3
  Changed Pages: 5
  Removed Pages: 0

HIGH RELEVANCE (action recommended)
───────────────────────────────────────────────────────────
  [NEW] https://code.claude.com/docs/sub-agents
    → New sub-agent capabilities may affect pipeline stage isolation

  [CHANGED] https://code.claude.com/docs/hooks
    → Content hash changed — review for new hook types

MEDIUM RELEVANCE (review recommended)
───────────────────────────────────────────────────────────
  [CHANGED] https://code.claude.com/docs/cli-reference
    → CLI flags may have new options for headless execution

LOW RELEVANCE (informational)
───────────────────────────────────────────────────────────
  [NEW] https://code.claude.com/docs/troubleshooting-faq
    → New troubleshooting page added

───────────────────────────────────────────────────────────
Run with --create-issues to auto-create GitHub issues for high-relevance findings.
```

#### Step 6.2: Write JSON report

Write structured results to `/tmp/docs-watch-report.json`:

```json
{
  "check_date": "ISO-8601",
  "last_check": "ISO-8601",
  "page_count": 72,
  "new_pages": [],
  "changed_pages": [],
  "removed_pages": [],
  "findings": [
    {
      "url": "https://code.claude.com/docs/sub-agents",
      "type": "new",
      "relevance": "high",
      "summary": "Brief description of the page content",
      "correlated_release": "2.1.81",
      "correlation_confidence": "high",
      "correlation_method": "timestamp_match"
    }
  ],
  "release_correlation": {
    "enabled": true,
    "current_release": "2.1.81",
    "correlated_count": 3,
    "high_confidence_count": 1,
    "medium_confidence_count": 1,
    "low_confidence_count": 1
  }
}
```

---

### Phase 7: Update Snapshot

Write the updated snapshot with current hashes:

```bash
# Build updated snapshot from diff results
python3 -c "
import json, sys
from datetime import datetime

# Load existing snapshot
with open('/tmp/docs-watch-snapshot.json') as f:
    snapshot = json.load(f)

# Load diff results
with open('/tmp/docs-watch-diff.json') as f:
    diff = json.load(f)

# Update pages with new/changed hashes and correlation data
for entry in diff.get('new', []) + diff.get('changed', []):
    page_data = {
        'hash': entry['hash'],
        'last_seen': datetime.utcnow().isoformat() + 'Z',
        'relevance': entry.get('relevance', 'low')
    }

    # Add correlation data if present (optional, for backward compatibility)
    if entry.get('correlated_release'):
        page_data['correlated_release'] = entry.get('correlated_release')
        page_data['correlation_confidence'] = entry.get('correlation_confidence', 'low')
        page_data['correlation_method'] = entry.get('correlation_method', 'none')

    snapshot['pages'][entry['url']] = page_data

# Remove deleted pages
for entry in diff.get('removed', []):
    snapshot['pages'].pop(entry['url'], None)

# Update metadata
snapshot['last_check'] = datetime.utcnow().isoformat() + 'Z'
snapshot['page_count'] = len(snapshot['pages'])

with open('${SNAPSHOT_FILE}', 'w') as f:
    json.dump(snapshot, f, indent=2)

print(f'Snapshot updated: {len(snapshot[\"pages\"])} pages tracked')
"
```

---

### Phase 8: Issue Creation (Optional)

Only executes when `--create-issues` is passed and `--dry-run` is false.

#### Step 8.1: Filter high-relevance findings

Only create issues for findings with `relevance: high`.

#### Step 8.2: Check for existing issues

```bash
gh issue list --search "claude-code-feature" --label "claude-code-feature" \
  --state open --json number,title
```

Skip creation if a matching issue already exists.

#### Step 8.3: Create issues

For each high-relevance finding:

```bash
gh issue create \
  --title "feat: Claude Code doc change — ${PAGE_NAME}" \
  --body "$(cat <<EOF
## Summary

A ${CHANGE_TYPE} was detected in Claude Code documentation that may affect the
Nightgauge pipeline.

**Page:** ${URL}
**Change Type:** ${CHANGE_TYPE}
**Relevance:** High
$(if [ -n "${CORRELATED_RELEASE}" ]; then echo "**Correlated Release:** v${CORRELATED_RELEASE} (${CORRELATION_CONFIDENCE} confidence)"; fi)

## Details

${SUMMARY}

## Action Items

- [ ] Review the documentation change
- [ ] Assess impact on pipeline stages
- [ ] Implement any necessary updates

## Release Correlation

This change was correlated with release v${CORRELATED_RELEASE} using ${CORRELATION_METHOD}.
See \`docs-watch\` Release Correlation feature in
[skills/nightgauge-docs-watch/correlation-reference.md](../../skills/nightgauge-docs-watch/correlation-reference.md)
for details on how correlation works and confidence scoring.

## Source

Auto-detected by \`/nightgauge:docs-watch\` on $(date -u +%Y-%m-%dT%H:%M:%SZ)
EOF
)" \
  --label "claude-code-feature,auto-detected"
```

For `--dry-run`, output what would be created without actually creating.

#### Step 8.4: Report created issues

Output list of created issues with numbers, titles, and URLs.

---

### Phase 9: Run Reflection

The snapshot (`index.json`) already tracks per-page state; this log tracks the
**per-run delta** so each run can report "since last check" at a glance.

```bash
SKILL_NAME="nightgauge-docs-watch"
RUN_LOG=".nightgauge/doc-snapshots/runs.jsonl"
```

<!-- include: ../_shared/RUN_REFLECTION.md -->

Set `RUN_COUNTS` (e.g. `{"new":N,"changed":N,"removed":N,"issues_created":N}`)
and `RUN_SUMMARY` from the Phase 6 report before the append step.

---

### Self-Assessment Epilogue

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Error Handling

| Condition                             | Action                                   |
| ------------------------------------- | ---------------------------------------- |
| Network failure fetching docs index   | Error with connectivity instructions     |
| curl timeout on individual page       | Skip page, note in report                |
| Malformed snapshot JSON               | Re-initialize snapshot, warn user        |
| `gh` CLI not authenticated            | Error with `gh auth login` instructions  |
| `--create-issues` with no findings    | Output "No high-relevance changes found" |
| `--dry-run` without `--create-issues` | Warn, continue with analysis only        |
| sha256sum/shasum not found            | Try both, error if neither available     |
| jq not installed                      | Error with install instructions          |

---

## Pipeline Position

```
UTILITIES (not part of main pipeline)

/nightgauge:docs-watch
       ↑
  Use on regular cadence or after CC releases
  Reads:  https://code.claude.com/docs/llms.txt
  Reads:  .nightgauge/doc-snapshots/index.json
  Writes: .nightgauge/doc-snapshots/index.json
  Writes: /tmp/docs-watch-report.json (ephemeral)
```

This is a standalone utility skill. It does not affect pipeline state and can be
run at any time without interfering with active pipeline runs.

---

**Author:** nightgauge **License:** Apache-2.0
