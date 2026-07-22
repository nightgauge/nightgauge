---
name: nightgauge-release-watch
description: >
  Monitor Claude Code GitHub releases for new features and changes relevant to
  the pipeline. Classifies changes by type and scores pipeline relevance. Use
  after a Claude Code release or on a periodic cadence to surface pipeline-relevant changes.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.1.1"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Bash Read Write Glob Grep
---

# Nightgauge Release Watch

## Description

Monitors Claude Code GitHub releases for new versions and changes relevant to the
Nightgauge pipeline. Classifies each change by type (feature, fix, breaking,
deprecation, improvement) and scores pipeline relevance based on affected areas
(tools, permissions, models, SDK, MCP, hooks, desktop, performance, UI).

**Use Cases:**

- Detecting new Claude Code capabilities that could enhance the pipeline
- Tracking breaking changes that require pipeline updates
- Auto-creating GitHub issues for high-impact changes
- Maintaining awareness of deprecations or permission model changes

**When to Use:**

- When a new Claude Code version is released
- On a regular cadence (weekly or bi-weekly)
- When checking what changed since the last known version

## Invocation

| Tool        | Command                                                   |
| ----------- | --------------------------------------------------------- |
| Claude Code | `/nightgauge:release-watch [options]`                     |
| Copilot     | Invoke via Agent Skills extension                         |
| Cursor      | Run via Agent Skills or direct SKILL.md                   |
| Standalone  | `claude --skill skills/nightgauge-release-watch/SKILL.md` |

## Arguments

| Argument                    | Description                                                        | Default                  |
| --------------------------- | ------------------------------------------------------------------ | ------------------------ |
| `--provider <name>`         | Provider slug — selects per-provider state, label, tracker (#4054) | `claude-code`            |
| `--source <owner/repo>`     | GitHub repo whose releases are fetched                             | `anthropics/claude-code` |
| `--since <version>`         | Check from a specific version (e.g., `2.1.75`)                     | last-seen                |
| `--dry-run`                 | Show report without updating last-seen state                       | `false`                  |
| `--create-issues`           | Auto-create GitHub issues for high-relevance changes               | `false`                  |
| `--format <json\|markdown>` | Output format                                                      | `markdown`               |

> **Multi-provider (#4054):** `--provider` selects the provider slug; everything
> provider-specific derives from it — the per-provider state file
> `last-seen-<provider>.json`, the per-provider issue label `<provider>-release`,
> and the tracker. `--source` is the repo polled (defaults to the Claude Code
> repo). The Release Watchdog workflow passes both for every provider in its
> matrix; a bare manual run defaults to Claude Code (backward-compatible).

### Examples

```bash
# Check for new releases since last check
/nightgauge:release-watch

# Check and auto-create issues for high-relevance changes
/nightgauge:release-watch --create-issues

# Preview what issues would be created (dry-run)
/nightgauge:release-watch --create-issues --dry-run

# Check releases since a specific version
/nightgauge:release-watch --since 2.1.75

# Get results as JSON instead of markdown
/nightgauge:release-watch --format json
```

### Scheduled / Autonomous Invocation

When invoked by GitHub Actions (`release-watchdog.yml`), the skill receives
`--since <last-seen-version> --create-issues` automatically. The workflow
respects the kill-switch from `.nightgauge/config.yaml`:

```yaml
# .nightgauge/config.yaml
autonomous_discovery:
  kill_switch: true # Disables --create-issues even in scheduled runs
  score_threshold: 70 # Minimum score for auto-issue creation
```

See [docs/SCHEDULED_DISCOVERY.md](../../docs/SCHEDULED_DISCOVERY.md) for full
documentation on the scheduled discovery workflow.

---

## Prerequisites

- nightgauge binary installed and configured
- `jq` installed (for JSON processing)
- Git repository with `.nightgauge/` directory
- Network access to `api.github.com`

**CRITICAL**: This skill runs headless. Do NOT use AskUserQuestion. Make
autonomous decisions or fail with clear error.

---

## Workflow

### Phase 0: Environment Preflight

<!-- include: ../_shared/PREFLIGHT.md -->

---

### Phase 1: Validate Environment

#### Step 1.1: Check `nightgauge` binary is installed and forge auth is configured

```bash
if ! command -v nightgauge &> /dev/null; then
  echo "ERROR: nightgauge binary not found. Install via the VSCode extension or build from source."
  exit 1
fi

if ! nightgauge forge auth status &>/dev/null; then
  echo "ERROR: nightgauge forge auth not configured. Run: nightgauge forge auth login"
  exit 1
fi
```

#### Step 1.2: Check `jq` is installed

```bash
if ! command -v jq &> /dev/null; then
  echo "ERROR: jq not found. Install with: brew install jq"
  exit 1
fi
```

#### Step 1.3: Ensure `.nightgauge/` directory exists

```bash
WATCH_DIR=".nightgauge/release-watch"
mkdir -p "${WATCH_DIR}/reports"
```

---

### Phase 2: Load Last-Seen Version

#### Step 2.1: Check for last-seen state file

```bash
# Resolve the provider (#4054). Everything provider-specific derives from it.
# Defaults keep a bare manual run pointed at Claude Code (backward-compatible).
PROVIDER="claude-code"
SOURCE="anthropics/claude-code"
if [[ "$*" == *"--provider"* ]]; then
  PROVIDER=$(echo "$*" | grep -oP '(?<=--provider\s)\S+')
fi
if [[ "$*" == *"--source"* ]]; then
  SOURCE=$(echo "$*" | grep -oP '(?<=--source\s)\S+')
fi
RELEASE_LABEL="${PROVIDER}-release"
LAST_SEEN_FILE="${WATCH_DIR}/last-seen-${PROVIDER}.json"
echo "Provider: ${PROVIDER}  Source: ${SOURCE}  Label: ${RELEASE_LABEL}"

if [ ! -f "$LAST_SEEN_FILE" ]; then
  # Initialize with empty state (all releases are new)
  echo "{\"provider\":\"${PROVIDER}\",\"source\":\"${SOURCE}\",\"version\":\"0.0.0\",\"checked_at\":\"1970-01-01T00:00:00Z\",\"releases_seen\":[]}" \
    | jq '.' > "$LAST_SEEN_FILE"
  LAST_VERSION="0.0.0"
  echo "Initialized new last-seen state for ${PROVIDER}"
else
  LAST_VERSION=$(jq -r '.version' "$LAST_SEEN_FILE")
  LAST_CHECK=$(jq -r '.checked_at // .detected_at // "unknown"' "$LAST_SEEN_FILE")
  echo "Last check: ${LAST_VERSION} on ${LAST_CHECK}"
fi

# Allow --since to override
if [[ "$*" == *"--since"* ]]; then
  SINCE_VERSION=$(echo "$*" | grep -oP '(?<=--since\s)\S+')
  LAST_VERSION="$SINCE_VERSION"
  echo "Checking from specified version: ${LAST_VERSION}"
fi
```

---

### Phase 3: Fetch Provider Releases

#### Step 3.1: Fetch + filter via the Go binary

```bash
NEW_RELEASES_FILE="/tmp/release-watch-new.json"

# `release fetch` issues GET https://api.github.com/repos/{source}/releases,
# applies a strict-semver --since filter, and emits a stable v1 JSON document
# (FetchResult). Replaces ~50 lines of inline forge api + Python — audit row B33.
# --source is the resolved provider repo (#4054); defaults to anthropics/claude-code.
nightgauge release fetch \
  --source "$SOURCE" \
  --since "$LAST_VERSION" \
  --limit 10 \
  --json > "$NEW_RELEASES_FILE"

NEW_COUNT=$(jq '.releases | length' "$NEW_RELEASES_FILE")
FILTERED=$(jq '.filtered' "$NEW_RELEASES_FILE")
echo "Fetched releases — new: ${NEW_COUNT}  filtered (older than ${LAST_VERSION}): ${FILTERED}"
```

---

### Phase 4: Parse and Classify Changes

#### Step 4.1: Classify release-body bullets via the Go binary

The classifier walks each release body line-by-line and emits one
`ClassifiedChange` per `-`-prefixed bullet using the canonical five-bucket
prefix mapping. Replaces ~80 lines of inline Python — audit row B33.

| Body-line prefix                     | `type`        |
| ------------------------------------ | ------------- |
| `Added …`                            | `feature`     |
| `Fixed …`                            | `fix`         |
| `Breaking …`                         | `breaking`    |
| `Deprecated …`                       | `deprecation` |
| `Improved …` / `Changed …` / default | `improvement` |

`[BRACKETED]` annotations are extracted into `changes[].tags`. Backticks are
stripped from the description.

```bash
CLASSIFIED_FILE="/tmp/release-watch-classified.json"

# Output is a top-level JSON array of ClassifiedRelease values — the field
# names ('version', 'published_at', 'changes[].type', '.description', '.tags')
# match the legacy /tmp/release-watch-classified.json shape so Phases 5+
# below need no changes.
nightgauge release classify-changes \
  --input "$NEW_RELEASES_FILE" \
  --json > "$CLASSIFIED_FILE"

CLASSIFIED_COUNT=$(jq 'length' "$CLASSIFIED_FILE")
echo "Classified ${CLASSIFIED_COUNT} release(s) with changes"
```

---

### Phase 5: Score Pipeline Relevance

#### Step 5.1: Quick-Pass Filter

Apply quick-pass keyword filtering to each change. This fast heuristic identifies changes that _might_ be relevant without full assessment.

Scoring logic (quick-pass):

- **Does this affect how we invoke Claude Code?** (+20 points)
  - Keywords: `--bare`, `--channels`, scripted, api-key, auth, token, session, headless, interactive
- **Does this add capabilities we could use in pipeline stages?** (+25 points)
  - Keywords: tools, bash, read, write, edit, plan, agent, sub-agent, skill, command, mcp, context
- **Does this change permissions/safety model?** (+15 points)
  - Keywords: permission, security, auth, allow-list, sandbox, privacy, scope
- **Does this affect MCP or tool system?** (+20 points)
  - Keywords: mcp, tool, server, plugin, custom-command, connection
- **Does this affect model availability/routing?** (+20 points)
  - Keywords: model, claude, router, capacity, availability, deprecat

**Decision:** If quick-pass score ≥ 50, flag for full assessment using the Feature Assessment Engine.

#### Step 5.2: Full Assessment (for high-scoring changes)

For changes scoring ≥ 50 on quick-pass, apply the **Feature Assessment Engine** framework:

**Reference:** [assessment-engine.md](./assessment-engine.md)

The engine scores changes across six dimensions (Pipeline Stage Impact, Automation Potential, Safety & Reliability, Developer Experience, Implementation Complexity, Cross-Repo Applicability) producing a composite 0–100 score.

**Classification thresholds:**

- **≥ 70** — High priority; auto-create GitHub issue
- **40–69** — Medium priority; add to backlog
- **< 40** — Low priority; log for reference

#### Step 5.3: Implement Quick-Pass Filter in Code

Apply quick-pass filter to all changes. Also loads `.nightgauge/focus.yaml`
to apply focus-based score boosts after the base score is calculated.

**Focus integration:** The active lens's `ScoringBoosts` map is applied after
the base quick-pass score. Each change's keywords are mapped to assessment
dimensions; matching dimensions contribute their boost. Final score is capped at 100. If no `focus.yaml` exists or the active lens is "general", no boosts are
applied and `focus_adjusted` is `false`.

**Dimension mapping (keyword → dimension):**

| Keyword Group                                                                                      | Dimension            |
| -------------------------------------------------------------------------------------------------- | -------------------- |
| auth, permission, security, sandbox, privacy, scope, vulnerability, secret, encrypt, sanitize, CVE | safety_reliability   |
| tool, mcp, agent, command, skill, context, ability, plugin, server                                 | pipeline_stage       |
| performance, speed, token, cost, cache, optimize, efficient, reduce                                | automation_potential |
| ux, experience, ergonomic, friction, ui, usability, onboard, interface                             | developer_experience |
| cross, multi-repo, workspace, integration, ecosystem                                               | cross_repo           |

```bash
SCORED_FILE="/tmp/release-watch-scored.json"
FOCUS_YAML=".nightgauge/focus.yaml"

python3 << 'PYTHON_EOF'
import json
import re
import os

# --- Focus lens loading ---

def load_focus_state(focus_yaml_path):
    """Load active lens name and scoring boosts from focus.yaml.

    Returns (lens_name, scoring_boosts) where scoring_boosts is a dict mapping
    dimension names to bonus points. Returns ("general", {}) if file missing or
    lens is "general".
    """
    if not os.path.exists(focus_yaml_path):
        return "general", {}

    try:
        # Parse YAML manually (avoid requiring pyyaml — use simple key:value parsing
        # since focus.yaml uses a flat structure the release-watch skill can parse)
        import yaml
        with open(focus_yaml_path) as f:
            state = yaml.safe_load(f)
    except ImportError:
        # Fallback: parse active_lens line only if pyyaml unavailable
        state = {}
        with open(focus_yaml_path) as f:
            for line in f:
                if line.startswith('active_lens:'):
                    state['active_lens'] = line.split(':', 1)[1].strip().strip('"\'')
                    break

    active_lens = state.get('active_lens', 'general') or 'general'

    if active_lens == 'general':
        return 'general', {}

    # Look up scoring boosts for the active lens
    builtin_boosts = {
        'quality':       {'safety_reliability': 10, 'pipeline_stage': 5, 'developer_experience': 5},
        'features':      {'pipeline_stage': 10, 'automation_potential': 10},
        'security':      {'safety_reliability': 15, 'cross_repo': 5},
        'performance':   {'automation_potential': 10, 'implementation_complexity': 5, 'pipeline_stage': 5},
        'documentation': {'developer_experience': 15, 'cross_repo': 5},
        'reliability':   {'safety_reliability': 15, 'pipeline_stage': 5},
        'ux':            {'developer_experience': 15, 'cross_repo': 5},
    }

    if active_lens in builtin_boosts:
        return active_lens, builtin_boosts[active_lens]

    # Check custom lenses in state
    for custom in state.get('custom_lenses', []):
        if isinstance(custom, dict) and custom.get('name', '').lower() == active_lens:
            return active_lens, custom.get('scoring_boosts', {})

    # Lens name set but not found — treat as general
    return active_lens, {}

def map_keywords_to_dimensions(text):
    """Map text keywords to assessment-engine dimension names.

    Returns a set of dimension names matched by the text.
    """
    text_lower = text.lower()
    matched = set()

    safety_keywords = ['auth', 'permission', 'security', 'sandbox', 'privacy',
                       'scope', 'vulnerability', 'secret', 'encrypt', 'sanitize', 'cve']
    pipeline_keywords = ['tool', 'mcp', 'agent', 'command', 'skill', 'context',
                         'ability', 'plugin', 'server']
    automation_keywords = ['performance', 'speed', 'token', 'cost', 'cache',
                           'optimize', 'efficient', 'reduce']
    dx_keywords = ['ux', 'experience', 'ergonomic', 'friction', 'ui',
                   'usability', 'onboard', 'interface']
    cross_repo_keywords = ['cross', 'multi-repo', 'workspace', 'integration', 'ecosystem']

    if any(kw in text_lower for kw in safety_keywords):
        matched.add('safety_reliability')
    if any(kw in text_lower for kw in pipeline_keywords):
        matched.add('pipeline_stage')
    if any(kw in text_lower for kw in automation_keywords):
        matched.add('automation_potential')
    if any(kw in text_lower for kw in dx_keywords):
        matched.add('developer_experience')
    if any(kw in text_lower for kw in cross_repo_keywords):
        matched.add('cross_repo')

    return matched

def apply_focus_boost(base_score, text, lens_name, scoring_boosts):
    """Apply focus-based score boost to base_score.

    Returns (adjusted_score, boost_amount, matched_dimensions).
    """
    if not scoring_boosts or lens_name == 'general':
        return base_score, 0, []

    matched_dims = map_keywords_to_dimensions(text)
    boost = sum(scoring_boosts.get(dim, 0) for dim in matched_dims)
    adjusted = min(base_score + boost, 100)
    return adjusted, boost, sorted(matched_dims)

def score_relevance(description, change_type, tags):
    """Quick-pass score for change relevance (0-100).

    This is a fast heuristic filter. Scores >= 50 are flagged for full assessment
    using the Feature Assessment Engine (see assessment-engine.md).
    """
    score = 0
    text = (description + ' ' + ' '.join(tags)).lower()

    # Invocation/scripting changes
    if any(word in text for word in ['--bare', '--channels', 'scripted', 'api-key', 'auth', 'token', 'session', 'headless', 'interactive', 'hook']):
        score += 20

    # New capabilities for stages
    if any(word in text for word in ['tool', 'bash', 'read', 'write', 'edit', 'plan', 'agent', 'sub-agent', 'skill', 'command', 'mcp', 'context', 'permission']):
        score += 25

    # Permissions/security
    if any(word in text for word in ['permission', 'security', 'auth', 'allow-list', 'sandbox', 'privacy', 'scope', 'org']):
        score += 15

    # MCP/tool system
    if any(word in text for word in ['mcp', 'server', 'plugin', 'custom-command', 'connection']):
        score += 20

    # Model/routing
    if any(word in text for word in ['model', 'claude', 'router', 'capacity', 'deprecat', 'available']):
        score += 20

    # Breaking changes always rate high
    if change_type == 'breaking':
        score = max(score, 70)

    # Deprecations rate high
    if change_type == 'deprecation':
        score = max(score, 60)

    return min(score, 100)

# Load focus state
focus_yaml_path = os.environ.get('FOCUS_YAML', '.nightgauge/focus.yaml')
lens_name, scoring_boosts = load_focus_state(focus_yaml_path)
if lens_name != 'general':
    print(f"Focus lens active: {lens_name} (boosts: {scoring_boosts})")
else:
    print("No focus lens active — using base scores")

with open('/tmp/release-watch-classified.json') as f:
    releases = json.load(f)

scored_releases = []

for release in releases:
    scored_changes = []
    for change in release['changes']:
        base_score = score_relevance(
            change['description'],
            change['type'],
            change['tags']
        )

        text = (change['description'] + ' ' + ' '.join(change['tags'])).lower()
        adjusted_score, boost_amount, matched_dims = apply_focus_boost(
            base_score, text, lens_name, scoring_boosts
        )

        focus_adjusted = boost_amount > 0
        scored_changes.append({
            **change,
            'relevance_score': base_score,
            'focus_adjusted': focus_adjusted,
            'focus_adjustment_amount': boost_amount,
            'focus_adjusted_score': adjusted_score if focus_adjusted else None,
            'focus_lens_applied': lens_name if focus_adjusted else None,
            'dimension_matches': matched_dims if focus_adjusted else [],
        })

    # Use focus-adjusted scores for release-level metrics when available
    effective_scores = [
        c['focus_adjusted_score'] if c['focus_adjusted'] else c['relevance_score']
        for c in scored_changes
    ]

    avg_relevance = sum(effective_scores) // len(effective_scores) if effective_scores else 0
    max_relevance = max(effective_scores, default=0)

    scored_releases.append({
        'version': release['version'],
        'published_at': release['published_at'],
        'average_relevance': avg_relevance,
        'max_relevance': max_relevance,
        'focus_lens': lens_name,
        'changes': scored_changes
    })

with open('/tmp/release-watch-scored.json', 'w') as f:
    json.dump(scored_releases, f, indent=2)

high_count = sum(1 for r in scored_releases if r['max_relevance'] > 50)
print(f"Scored {len(scored_releases)} releases ({high_count} with relevance > 50)")
PYTHON_EOF
```

---

### Phase 6: Generate Report

#### Step 6.1: Create markdown report

```bash
REPORT_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
REPORT_FILE="${WATCH_DIR}/reports/report-$(date -u +%Y%m%d-%H%M%S).md"

python3 << 'PYTHON_EOF'
import json
from datetime import datetime

with open('/tmp/release-watch-scored.json') as f:
    releases = json.load(f)

with open('.nightgauge/release-watch/last-seen-claude-code.json') as f:
    last_state = json.load(f)

# Detect active focus lens (same across all releases in this run)
active_lens = releases[0]['focus_lens'] if releases else 'general'

report = []
report.append("# Claude Code Release Monitor Report")
report.append("")
report.append(f"**Check Date:** {datetime.utcnow().isoformat()}Z")
report.append(f"**Last Check:** {last_state.get('checked_at', 'Never')}")
if active_lens and active_lens != 'general':
    report.append(f"**Focus Lens:** {active_lens}")
report.append("")

# Summary stats
high_relevance = [r for r in releases if r['max_relevance'] >= 70]
medium_relevance = [r for r in releases if 40 <= r['max_relevance'] < 70]
low_relevance = [r for r in releases if r['max_relevance'] < 40]

report.append("## Summary")
report.append("")
report.append(f"- **New Releases:** {len(releases)}")
report.append(f"- **High Relevance (>= 70):** {len(high_relevance)}")
report.append(f"- **Medium Relevance (40-69):** {len(medium_relevance)}")
report.append(f"- **Low Relevance (< 40):** {len(low_relevance)}")
if active_lens and active_lens != 'general':
    report.append(f"- **Focus Adjustment:** Scores boosted by {active_lens} lens")
report.append("")

def format_change_line(change, min_score=0):
    """Format a single change line with optional focus adjustment notation."""
    # Determine effective display score
    display_score = change.get('focus_adjusted_score') or change['relevance_score']
    if display_score < min_score:
        return None

    icon = ('🔴' if change['type'] == 'breaking' else
            '✨' if change['type'] == 'feature' else
            '🐛' if change['type'] == 'fix' else
            '⚠️' if change['type'] == 'deprecation' else '📈')
    tags = f" [{', '.join(change['tags'])}]" if change['tags'] else ""

    # Build score display with focus adjustment notation
    if change.get('focus_adjusted') and change.get('focus_adjustment_amount', 0) > 0:
        base = change['relevance_score']
        adj = change['focus_adjusted_score']
        boost = change['focus_adjustment_amount']
        lens = change.get('focus_lens_applied', active_lens)
        score_str = f"Score: {base} → {adj} [+{boost} {lens} focus]"
    else:
        score_str = f"Score: {display_score}"

    return f"- {icon} **{change['type'].title()}** ({score_str}): {change['description']}{tags}"

# High relevance section
if high_relevance:
    report.append("## High Relevance Changes (Action Recommended)")
    report.append("")
    for r in high_relevance:
        report.append(f"### v{r['version']} ({r['published_at'][:10]})")
        report.append(f"**Average Relevance Score:** {r['average_relevance']}")
        report.append("")
        for change in r['changes']:
            line = format_change_line(change, min_score=50)
            if line:
                report.append(line)
        report.append("")

# Medium relevance section
if medium_relevance:
    report.append("## Medium Relevance Changes (Review Recommended)")
    report.append("")
    for r in medium_relevance:
        report.append(f"### v{r['version']} ({r['published_at'][:10]})")
        report.append(f"**Average Relevance Score:** {r['average_relevance']}")
        report.append("")
        for change in r['changes']:
            line = format_change_line(change, min_score=40)
            if line:
                report.append(line)
        report.append("")

# Low relevance section (summary only)
if low_relevance:
    report.append("## Low Relevance Changes")
    report.append(f"- {len(low_relevance)} releases with average relevance < 40 (no action required)")
    report.append("")

report.append("---")
report.append("")
report.append("**How to use this report:**")
report.append("- 🔴 **Breaking Changes:** Review immediately, plan updates")
report.append("- ✨ **Features:** Assess if we can use in pipeline stages")
report.append("- 🐛 **Fixes:** Check if we're affected by the original bug")
report.append("- ⚠️ **Deprecations:** Plan migration timeline")
if active_lens and active_lens != 'general':
    report.append(f"- **Focus Notation:** `Score: X → Y [+Z {active_lens} focus]` shows base → adjusted score")
report.append("")
report.append("Run `--create-issues` flag to auto-create GitHub issues for high-relevance changes.")
report.append("")

# Write report
output = '\n'.join(report)
print(output)

# Also save to file for archival
with open('${REPORT_FILE}', 'w') as f:
    f.write(output)

print(f"\nReport saved to: ${REPORT_FILE}")
PYTHON_EOF
```

#### Step 6.2: Create JSON report for programmatic consumption

```bash
REPORT_JSON="${WATCH_DIR}/reports/report-$(date -u +%Y%m%d-%H%M%S).json"

python3 << 'PYTHON_EOF'
import json
from datetime import datetime

with open('/tmp/release-watch-scored.json') as f:
    releases = json.load(f)

with open('.nightgauge/release-watch/last-seen-claude-code.json') as f:
    last_state = json.load(f)

active_lens = releases[0]['focus_lens'] if releases else 'general'

# Count focus-adjusted changes across all releases
focus_adjusted_count = sum(
    1 for r in releases for c in r['changes']
    if c.get('focus_adjusted')
)

report = {
    'check_date': datetime.utcnow().isoformat() + 'Z',
    'last_check': last_state.get('checked_at', '1970-01-01T00:00:00Z'),
    'release_count': len(releases),
    'high_relevance_count': len([r for r in releases if r['max_relevance'] >= 70]),
    'medium_relevance_count': len([r for r in releases if 40 <= r['max_relevance'] < 70]),
    'low_relevance_count': len([r for r in releases if r['max_relevance'] < 40]),
    'focus_lens_applied': active_lens,
    'focus_adjusted_changes': focus_adjusted_count,
    'releases': releases
}

with open('${REPORT_JSON}', 'w') as f:
    json.dump(report, f, indent=2)

print(f"JSON report saved to: ${REPORT_JSON}")
PYTHON_EOF
```

---

### Phase 7: Update Last-Seen State (unless --dry-run)

#### Step 7.1: Check if this is a dry-run

```bash
if [[ "$*" == *"--dry-run"* ]]; then
  echo "Dry-run mode: not updating last-seen state"
  exit 0
fi
```

#### Step 7.2: Update last-seen file with newest version

```bash
python3 << 'PYTHON_EOF'
import json
from datetime import datetime

with open('/tmp/release-watch-scored.json') as f:
    releases = json.load(f)

if not releases:
    print("No new releases to record")
else:
    # Get newest version
    newest_version = releases[0]['version']

    # Load current state
    with open('.nightgauge/release-watch/last-seen-claude-code.json') as f:
        state = json.load(f)

    # Update with new version
    state['version'] = newest_version
    state['checked_at'] = datetime.utcnow().isoformat() + 'Z'

    # Keep a history of last 10 versions seen
    if newest_version not in state.get('releases_seen', []):
        state['releases_seen'].insert(0, newest_version)
        state['releases_seen'] = state['releases_seen'][:10]

    # Save updated state
    with open('.nightgauge/release-watch/last-seen-claude-code.json', 'w') as f:
        json.dump(state, f, indent=2)

    print(f"Updated last-seen version to: {newest_version}")
PYTHON_EOF
```

---

### Phase 8: Create Issues (Optional)

Only executes when `--create-issues` flag is passed.

**Reference:** All auto-issue-creation logic is documented in **[auto-issue-creation.md](./auto-issue-creation.md)**.

This phase:

- Creates GitHub issues for changes scoring **≥ 70 (High priority)** after full Feature Assessment Engine evaluation
- Handles override rules (breaking changes min 60, deprecations min 70, security min 50, model changes min 60)
- Applies deduplication to prevent duplicate issues
- Syncs created issues to the project board with proper labels and fields
- Tracks all decisions in `.nightgauge/release-watch/creation-log.json` for auditability
- Supports `--dry-run` flag for preview before creation
- Enforces safety rails (max 3 issues per release, confirmation before creation)

#### Implementation Overview

The auto-issue-creation workflow:

1. **Load scored releases** from Phase 6 output
2. **Filter by score** (≥70 or override rules apply)
3. **Deduplication check** — Skip if similar issue already exists (>80% title match)
4. **Template selection** — Choose issue template based on feature type:
   - Single-stage feature → Standard issue
   - Multi-stage feature → Epic with sub-issues
   - Breaking change → High-priority bug issue
   - Deprecation → Chore with migration timeline
5. **Preview (if dry-run)** — Show what would be created
6. **Create issue** — Use `nightgauge forge issue create` with proper labels and body
7. **Board sync** — Add to project, set Priority/Size fields, set Status
8. **Logging** — Record decision in creation-log.json

#### Step 8.1: Label Setup

Ensure required labels exist before creating issues:

```bash
# Create label if it doesn't exist
nightgauge forge label create "source:auto-discovery" \
  --description "Auto-created by release-watch assessment" \
  --color "0e8a16" 2>/dev/null || true

# Per-provider release label (#4054): claude-code-release | codex-release | gemini-release.
nightgauge forge label create "$RELEASE_LABEL" \
  --description "Tracks ${PROVIDER} release impact and integration" \
  --color "1f6feb" 2>/dev/null || true
```

> Use `$RELEASE_LABEL` (= `<provider>-release`) wherever this skill labels a
> created issue or searches for prior release issues to dedupe — never a
> hardcoded `claude-code-release` — so each provider's issues stay distinct.

#### Step 8.2: Trigger Workflow

The complete workflow is implemented by:

1. Load `/tmp/release-watch-scored.json` (from Phase 6)
2. For each release and each change:
   - If score ≥ 70 or override rule applies → Proceed to issue creation
   - If score 40-69 → Log to backlog file
   - If score < 40 → Skip
3. Run deduplication check against existing issues
4. Select appropriate template from `auto-issue-creation.md`
5. Build issue content with variable substitution
6. If `--dry-run`: Show preview and ask for confirmation
7. If confirmed: Create issue, sync to board, update log
8. Show summary (X created, Y deduped, Z backlogged)

#### Step 8.3: Detailed Workflow Reference

For complete implementation details, see:

- **Trigger Criteria:** [auto-issue-creation.md § Trigger Criteria](./auto-issue-creation.md#trigger-criteria)
- **Issue Templates:** [auto-issue-creation.md § Issue Template by Feature Type](./auto-issue-creation.md#issue-template-by-feature-type)
  - Single-stage feature (1–2 stages)
  - Multi-stage feature (3+ stages, create epic)
  - Breaking change
  - Deprecation
- **Deduplication Logic:** [auto-issue-creation.md § Deduplication Logic](./auto-issue-creation.md#deduplication-logic)
- **Label Selection:** [auto-issue-creation.md § Label Selection Strategy](./auto-issue-creation.md#label-selection-strategy)
- **Board Sync:** [auto-issue-creation.md § Board Sync Process](./auto-issue-creation.md#board-sync-process)
- **Safety Rails:** [auto-issue-creation.md § Safety Rails](./auto-issue-creation.md#safety-rails)
- **Dry-Run Mode:** [auto-issue-creation.md § Dry-Run Mode](./auto-issue-creation.md#dry-run-mode)
- **Creation Log:** [auto-issue-creation.md § Creation Log for Auditability](./auto-issue-creation.md#safety-rails) (in Rail 4 section)

#### Step 8.4: Error Handling

If any step fails:

1. Log error to creation-log.json with `"action": "error"`
2. Print clear error message with remediation steps
3. STOP (do not continue creating remaining issues)

**Common errors and fixes:** See [auto-issue-creation.md § Error Handling & Troubleshooting](./auto-issue-creation.md#error-handling--troubleshooting)

---

### Self-Assessment Epilogue

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Error Handling

| Condition                          | Action                                                |
| ---------------------------------- | ----------------------------------------------------- |
| Network failure fetching releases  | Error with GitHub API troubleshooting                 |
| `nightgauge` binary not installed  | Error with install instructions                       |
| Forge auth not configured          | Error with `nightgauge forge auth login` instructions |
| Malformed last-seen JSON           | Re-initialize state, warn user                        |
| `jq` not installed                 | Error with install instructions                       |
| `--create-issues` with no findings | Output "No high-relevance changes found"              |
| `--dry-run` without issues mode    | Warn, continue with analysis only                     |
| Release notes parsing failure      | Skip that release, warn in report                     |
| No new releases found              | Output "All releases have been reviewed"              |

---

## Pipeline Position

```
MONITORING UTILITY (not part of main pipeline)

/nightgauge:release-watch
       ↑
  Use on regular cadence or after a provider release
  Reads:  GitHub API (<source> releases — e.g. anthropics/claude-code, openai/codex, google-gemini/gemini-cli)
  Reads:  .nightgauge/release-watch/last-seen-<provider>.json
  Writes: .nightgauge/release-watch/reports/*.md
  Writes: .nightgauge/release-watch/reports/*.json
  Writes: .nightgauge/release-watch/last-seen-<provider>.json (unless --dry-run)
```

This is a standalone utility skill. It does not affect pipeline state and can be
run at any time without interfering with active pipeline runs.

---

**Author:** nightgauge **License:** Apache-2.0
