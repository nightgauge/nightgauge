# Summary Report & Sub-Issue Migration Check (Phases 7 and 7.5)

Procedural detail for Phase 7 (Summary Report) and Phase 7.5 (Native Sub-Issue
Migration Check).

## Contents

- [Phase 7: Summary Report](#phase-7-summary-report)
- [Phase 7.5: Native Sub-Issue Migration Check](#phase-75-native-sub-issue-migration-check)

---

## Phase 7: Summary Report

Output a clear summary:

```
┌─────────────────────────────────────────────────────────────────┐
│  NIGHTGAUGE REPO INIT COMPLETE                             │
└─────────────────────────────────────────────────────────────────┘

Repository:  nightgauge/my-repo
Project:     #2 — Nightgauge Platform

── Labels ──────────────────────────────────────────────────────────
  + created: type:bug, type:docs, type:chore, type:feature, type:refactor,
             type:epic, type:spike
  ✓ exists:  documentation, bug (defaults kept)
  + created: priority:critical, priority:high, priority:medium, priority:low
  + created: size:XS, size:S, size:M, size:L, size:XL
  + created: component:api, component:auth, component:billing,
             component:analytics, component:infra, component:sdk

── Project Board ───────────────────────────────────────────────────
  ✓ exists:  Status (Backlog/Ready/In progress/In review/Done)
  ✓ exists:  Priority (P0/P1/P2/P3)
  ✓ exists:  Size (XS/S/M/L/XL)
  + created: Start date
  + created: Target date
  ✓ exists:  Estimate
  + linked:  nightgauge/my-repo → project #2

── Views ───────────────────────────────────────────────────────────
  + created: Backlog (board)
  + created: Priority board (board)
  + created: Team items (table)
  + created: Roadmap (roadmap)
  + created: My items (table)

── Config ──────────────────────────────────────────────────────────
  + created: .nightgauge/config.yaml
  + verified: project.fields in config.yaml

── Workspace ──────────────────────────────────────────────────────
  + registered: my-repo in nightgauge/.vscode/nightgauge-workspace.yaml
  [or if standalone]:
  — standalone: no multi-repo workspace detected

── Knowledge ───────────────────────────────────────────────────────
  + created: .nightgauge/knowledge/ (epics/, features/, README.md)
  (disabled by default — set knowledge.enabled: true in config.yaml to activate)
  [or if --skip-knowledge was passed]:
  -- skipped: --skip-knowledge flag was set
  [or if already existed]:
  ✓ exists: .nightgauge/knowledge/ (no changes)

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

✅ Repository is ready for the Nightgauge pipeline.

Next steps:
  1. /nightgauge:smart-setup — Generate AI documentation (AGENTS.md, CLAUDE.md, docs/)
  2. /nightgauge:issue-pickup — Claim your first issue and begin the pipeline
  3. /nightgauge:project-sync — If you have existing issues to sync to the board
```

If `--dry-run` was set, prefix the header with `[DRY RUN]` and note no changes
were applied.

---

## Phase 7.5: Native Sub-Issue Migration Check

Check whether any existing issues use the legacy "Part of #X" body pattern
without native GitHub sub-issue links. The Nightgauge pipeline relies on
native sub-issues (GitHub's parent/child feature) for epic grouping and ordering
in the project board. Body-based references alone are not sufficient.

```bash
# Count issues with "Part of #" in body
BODY_REFS=$(nightgauge forge issue list --repo "$REPO" --state all --limit 500 \
  --json number,body | jq '[.[] | select(.body != null) | select(.body | test("(?i)part of #[0-9]+"))] | length')

if [[ "$BODY_REFS" -gt 0 ]]; then
  echo ""
  echo "⚠  Found $BODY_REFS issues with 'Part of #X' body references."
  echo "   The pipeline requires native GitHub sub-issues for epic grouping."
  echo ""
  echo "   Run the migration script to create native sub-issue links:"
  echo "   ./scripts/migrate-body-to-native-sub-issues.sh --repo $REPO"
  echo "   (Use --dry-run first to preview changes)"
fi
```

**Why native sub-issues matter:**

- The project board tree view groups issues under epics using the GraphQL
  `subIssues(first: 50)` field — epics are detected by having sub-issues
- Body-based "Part of #X" text is not queryable via the GitHub API
- Native sub-issues are visible in the GitHub UI with proper hierarchy
- The Go binary (`nightgauge issue create-sub` and `issue link-sub`)
  already creates native links — this check catches repos that predate the Go
  binary
