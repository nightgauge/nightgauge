# Nightgauge - Claude Code Configuration

AI-powered Issue-to-PR pipeline. See
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for product layers and design.

## Key Files

- **packages/nightgauge-vscode/** - VSCode extension (PRIMARY product)
- **cmd/nightgauge/** - Go CLI binary entry point
- **internal/** - Go packages (deterministic layer — hooks, GitHub,
  intelligence)
- **packages/nightgauge-sdk/** - SDK for programmatic access
- **skills/** - Pipeline stage definitions (portable)
- **claude-plugins/** - Claude Code CLI wrappers (thin shell → Go binary)
- **standards/** - Universal coding standards and security requirements
- **[docs/GIT_WORKFLOW.md](docs/GIT_WORKFLOW.md)** - Git workflow and
  pre-submission validation
- **[docs/GO_BINARY.md](docs/GO_BINARY.md)** - Go binary architecture and CLI
  reference
- **[CONTRIBUTING.md](CONTRIBUTING.md)** - Contributing guide (skills, plugins,
  extension, SDK)

## Critical Rules

### Versioning

**Unified version**: all packages share `0.1.0` as the base version in
`package.json`. The release version is derived from the git tag (`vX.Y.Z`)
at release time and applied uniformly to the Go binary and the extension —
not from a commit count. **NEVER set different versions** across
`nightgauge-vscode` and `nightgauge-sdk`. See
[docs/GIT_WORKFLOW.md](docs/GIT_WORKFLOW.md#versioning) for full rules.

### Git Workflow

**NEVER push directly to main.** Use feature branches (`feat/`, `fix/`,
`docs/`). See [docs/GIT_WORKFLOW.md](docs/GIT_WORKFLOW.md) for full workflow.

### Knowledge & Memory — Single Source of Truth

**Do NOT use the Claude Code auto-memory system** (no `MEMORY.md` / per-fact
memory files). Everything durable lives in the repository so there is exactly
one source of truth:

- **How the agent should work** (rules, preferences, conventions) → this
  `CLAUDE.md` and `AGENTS.md`.
- **Technical / triage knowledge** (symptom → root cause → fix, known
  false-alarms, runtime gotchas) → `docs/` — primarily
  [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md),
  [docs/CI_CD_RUNBOOK.md](docs/CI_CD_RUNBOOK.md),
  [docs/AUTO_TRIAGE.md](docs/AUTO_TRIAGE.md), and
  [docs/FAILURE_TAXONOMY.md](docs/FAILURE_TAXONOMY.md). Use the Documentation
  Map below to find the right file; add to the existing doc rather than creating
  a parallel one.
- **Per-issue working context** → the `.nightgauge/knowledge/` base, which
  graduates to `docs/` (see [docs/KNOWLEDGE_BASE.md](docs/KNOWLEDGE_BASE.md)).

When you learn something worth keeping, edit the appropriate repo file in a
branch/PR — never a side-channel memory store.

### Agent Operating Rules

- **No backwards compatibility (pre-customer).** Delete old paths; never add
  deprecation shims, migration fallbacks, or compat knobs. Consolidate N
  overlapping options to one, delete the rest from schema/config/docs, and
  surface the single resolved value. Consistency over compatibility.
- **Ship the best solution; don't offer menus.** If you spot a real gap or a
  better approach, file the issue and execute it. Propose the recommended fix
  and do it — avoid "quick fix vs proper fix" choices and "want me to…?"
  friction. (This does not override genuine product-direction decisions, which
  are still the user's call.)
- **Manual PR merges only.** Auto-merge is disabled on all workspace repos.
  Watch CI (`gh pr checks`), fix/rerun real failures (never dismiss a failing
  test as "flaky" without root-causing it), then `gh pr merge --squash` — never
  `--auto`, never `--admin`. See [docs/GIT_WORKFLOW.md](docs/GIT_WORKFLOW.md).
- **Context economy — auto-compact is not a context-management strategy.**
  Frontier-model tokens are expensive and long contexts cost more per step, so
  a large context is justified only when it actually carries the answer to the
  task at hand (e.g. an in-flight incident's accumulated state). Otherwise
  prefer the lean path: finish the current scope, then start a fresh session
  for new work; delegate self-contained searches/subtasks to subagents that
  return conclusions instead of dumping file contents into the main context;
  and read only the parts of files a task needs. Never let a session drift
  into "one more small task" accretion just because compaction will eventually
  reclaim space — deliberate scoping beats automatic summarization. This is
  also product philosophy: Nightgauge's pipeline hands each stage a scoped
  context on purpose.

### GitHub CLI in Multi-User Workspaces

Multiple workspaces from **different GitHub accounts** are often open at once
(e.g. `octocat` for nightgauge repos, a separate bot account elsewhere).
`gh auth switch` changes the **global** active account and silently breaks every
other open workspace. **Never `gh auth switch` for repo work.** Instead pass a
per-command scoped token:

```bash
GH_TOKEN=$(gh auth token --user octocat) gh <command> ...
```

This resolves `nightgauge/*` without disturbing the active account. (`git push`
uses SSH and is unaffected.)

### Developer Setup

`npm install` requires no registry authentication: this repo depends on no
private packages, and the generated platform types are vendored under
`api/generated/`. Building the Go binary needs only the Go toolchain (`go.mod`).

### Pre-Submission Validation (MANDATORY before every push)

**NEVER push to GitHub without passing ALL local checks first.** CI is for
confirming environment differences — not for discovering failures you could
have caught locally. Every failed CI push wastes time and pollutes PRs with
fix-up commits.

The complete, ordered command list is maintained in **exactly one place** —
[docs/GIT_WORKFLOW.md § Pre-Submission Validation](docs/GIT_WORKFLOW.md#pre-submission-validation-critical).
Run every step there (Go build/tests → IPC client regen → TypeScript build →
VSCode tests → SDK tests → Prettier → ESLint), or run them all at once with
`bash scripts/ci-local.sh`, before every `git push`. Do NOT mark work as
complete until all checks pass.

### Security

See **[standards/security.md](standards/security.md)** for complete
requirements.

- NEVER hardcode secrets
- ALWAYS validate input
- NEVER commit secrets to git

**Code review checklist**: Input validation on external data, no hardcoded
secrets, parameterized DB queries, auth checks on every request, authorization
for resource access, sensitive data encrypted, no internals in error messages,
no sensitive data in logs, dependencies up to date.

### Public-Repo Content Hygiene (issues, spikes, docs)

This repo is maintained as a public-safe tree. The publication-boundary guard scans
the tracked **tree** — but **GitHub issue and epic bodies are not in the tree**,
so nothing mechanical catches them. When authoring an issue, epic, spike, or doc
whose home is `nightgauge/nightgauge`, keep company economics, private
implementation details, customer data, and unreleased roadmap material out.
Those belong in `nightgauge-internal`; see
[docs/DOCUMENTATION_IA.md](docs/DOCUMENTATION_IA.md). Describe only the stable
public integration contract.

- **Coordination epics/spikes that are mostly private work** belong in
  `nightgauge-internal`; leave only a
  slim capability-level stub in the public repo if a community tracker is wanted.
- References to a public integration contract are fine. Private repository
  plans, issue numbers, deployment topology, and implementation status are not.
- Generated `docs/spikes/`, `docs/epics/`, and ADR artifacts require explicit
  publication review; their directory location is not evidence of safety.

### Pipeline Execution

See `.claude/rules/vscode-extension.md` for pipeline execution rules (scoped to
`packages/nightgauge-vscode/**`).

### Issue Creation

See `.claude/rules/scripts.md` for issue creation and project board sync rules
(scoped to `claude-plugins/**` and `scripts/**`).

## Creating Content

See **[CONTRIBUTING.md](CONTRIBUTING.md)** for how to add VSCode commands, SDK
modules, skills, and plugin commands.

## Architecture

See **[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)** for complete documentation.

## Multi-Repository Workspace

See **[docs/MULTI_REPO_WORKSPACE.md](docs/MULTI_REPO_WORKSPACE.md)** for
multi-repo workspace support.

## Knowledge Base Usage

When working within `nightgauge/`, the pipeline auto-scaffolds a knowledge base directory per issue when `knowledge.enabled: true`. Always read `knowledge_path/PRD.md` and `knowledge_path/decisions.md` if `knowledge_path` is set in context. See `docs/KNOWLEDGE_BASE.md` for the full schema and lifecycle.

**IA rule**: see [docs/KNOWLEDGE_BASE.md#information-architecture](docs/KNOWLEDGE_BASE.md#information-architecture) before deciding where to record decisions (`docs/` vs `.nightgauge/knowledge/`). Cross-cutting, stable, reader-facing decisions live in `docs/`; per-issue context lives in `knowledge/` and graduates manually via `nightgauge knowledge graduate`.

## Documentation Map

> This map helps AI agents find relevant documentation based on the task at
> hand. Keywords are matched against issue content to prioritize which docs to
> read.

| Topic                    | Primary Docs                                                                       | Keywords                                                                                                                                                                                                                      |
| ------------------------ | ---------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Architecture             | docs/ARCHITECTURE.md                                                               | architecture, design, components, layers, structure                                                                                                                                                                           |
| Ecosystem                | docs/ECOSYSTEM.md                                                                  | ecosystem, platform, flutter, angular, mobile, web, cross-repo, integration, api                                                                                                                                              |
| Product Overview         | docs/PRODUCT_OVERVIEW.md                                                           | product, features, overview, capabilities, self-learning, intelligence, what-it-does                                                                                                                                          |
| Pipeline                 | docs/CONTEXT_ARCHITECTURE.md                                                       | pipeline, context, handoff, stage, workflow, state                                                                                                                                                                            |
| Pipeline Exec            | docs/PIPELINE_EXECUTION.md                                                         | execution, mode, interactive, headless, run, invoke                                                                                                                                                                           |
| Git                      | docs/GIT_WORKFLOW.md                                                               | git, branch, commit, merge, pull, request, pr                                                                                                                                                                                 |
| Security                 | standards/security.md                                                              | security, validation, secrets, auth, sanitize, input                                                                                                                                                                          |
| Testing                  | docs/TESTING.md                                                                    | test, coverage, unit, integration, e2e, vitest                                                                                                                                                                                |
| Standards                | docs/CODE_STANDARDS.md                                                             | naming, style, format, convention, standard, markdown                                                                                                                                                                         |
| Config                   | docs/CONFIGURATION.md                                                              | config, yaml, settings, options, nightgauge                                                                                                                                                                                   |
| Skills                   | skills/README.md                                                                   | skill, command, plugin, slash, invoke                                                                                                                                                                                         |
| VSCode                   | packages/nightgauge-vscode/README.md                                               | vscode, extension, dashboard, sidebar, tree                                                                                                                                                                                   |
| SDK                      | packages/nightgauge-sdk/README.md                                                  | sdk, programmatic, orchestration, api                                                                                                                                                                                         |
| Go Binary                | docs/GO_BINARY.md                                                                  | go, binary, cli, compiled, hook, gate, ipc, deterministic                                                                                                                                                                     |
| Multi-Repo               | docs/MULTI_REPO_WORKSPACE.md                                                       | multi, repo, workspace, monorepo, routing, switch, board-sync, provision, sweep, shared-project, lifecycle-sweep                                                                                                              |
| Adaptive Pipeline        | docs/ADAPTIVE_PIPELINE.md                                                          | adaptive, policy, auto-tune (removed — see doc for what was kept)                                                                                                                                                             |
| Fast-Track               | docs/GATE_RELAXATION.md                                                            | fast-track, change_rules, change-class, classifier, skip-stages, ci-gate, changes-gate, run_heavy, gate-relaxation, relax_on_change_class, drift-revoke, docs-only, config-only, trivial-change, cost                         |
| Adaptive                 | docs/ADAPTIVE_DOCUMENTATION_READING.md                                             | adaptive, complexity, scope, minimal, targeted, extended                                                                                                                                                                      |
| Discovery                | docs/CONTEXT_AWARE_DOC_DISCOVERY.md                                                | discovery, keywords, documentation, map, routing                                                                                                                                                                              |
| Execution                | docs/INTERACTIVE_MODE.md                                                           | interactive, headless, mode, execution, token                                                                                                                                                                                 |
| Health                   | docs/HEALTH_MONITORING.md                                                          | health, monitor, analysis, dimension, score, finding, recommendation, stall, detect, reliability                                                                                                                              |
| Pipeline Audit           | skills/nightgauge-pipeline-audit/                                                  | audit, snapshot, efficiency, token-usage, cost, stage-performance, quick-check                                                                                                                                                |
| Pipeline Health          | skills/nightgauge-pipeline-health/                                                 | health-analysis, comprehensive, cross-reference, recommendation-tracking, baseline, dimensions                                                                                                                                |
| Epic Assessment          | skills/nightgauge-assess-epic/                                                     | epic, assess, batch, sequential, strategy, sub-issue, parallel, wave                                                                                                                                                          |
| Epic Validation          | skills/nightgauge-epic-validate/                                                   | validate, epic, verify, board, blockedBy, linking, structure, post-creation (DEPRECATED — use Issue Audit)                                                                                                                    |
| Issue Audit              | docs/ISSUE_AUDIT.md                                                                | audit, post-creation, manifest, repair, finding, severity, ready, needs-fixes, board, blockedBy, knowledge                                                                                                                    |
| Integration Audit        | skills/nightgauge-integration-audit/                                               | integration, cross-repo, api, endpoint, auth, mismatch, gap, drift, alignment                                                                                                                                                 |
| Queue Management         | skills/nightgauge-queue/                                                           | queue, add, remove, clear, label, enqueue, dequeue, reorder, pipeline-queue                                                                                                                                                   |
| Outcome Recording        | docs/OUTCOME_RECORDING.md                                                          | outcome, recording, complexity, model, feedback, learning, calibration                                                                                                                                                        |
| Post-Merge Survival      | docs/GO_BINARY.md#survival-operations-4151                                         | survival, post-merge, ground-truth, revert, broke, reverted, unobserved, window, reconcile-sweep, merge-commit-sha, capture, detection, survival-records                                                                      |
| Learning System          | docs/SELF_IMPROVEMENT_LOOP.md                                                      | learning, tuning, calibration, optimizer, outcome, pipeline-learning                                                                                                                                                          |
| Model Evaluation         | docs/MODEL_EVALUATION.md, docs/decisions/011-model-eval-system.md                  | model-eval, benchmark, cost-vs-quality, model-registry, pricing, matrix-runner, judge, reliability-guard, attempts-to-green, routing-advisor, new-model-decision, sonnet-5, evaluate-models                                   |
| Boundaries               | docs/SELF_IMPROVEMENT_BOUNDARIES.md                                                | boundary, internal, external, dogfood, product-improvement, pipeline-optimization                                                                                                                                             |
| Phase Tracking           | docs/ARCHITECTURE.md                                                               | phase, tracker, marker, registry, progress, phaseRegistry                                                                                                                                                                     |
| Stage Gates              | docs/STAGE_GATES.md                                                                | gate, post-condition, verify, stage-gate, StageGate, framework, skill-said-success                                                                                                                                            |
| Failure Outcomes         | docs/FAILURE_TAXONOMY.md                                                           | failure, outcome, taxonomy, weighted, retry, classify, error                                                                                                                                                                  |
| Auto-Triage              | docs/AUTO_TRIAGE.md                                                                | recovery, registry, self-heal, auto-triage, failure-recovery, RecoveryAction, RecoveryAttempts                                                                                                                                |
| Stage-Exit Diagnostic    | docs/STAGE_EXIT_DIAGNOSTIC.md                                                      | exit-record, diagnostic, forensic, retro, post-mortem, stall, signal, last-bash, stderr-tail, rate-limit                                                                                                                      |
| Lifecycle Trace          | docs/GO_BINARY.md#trace-operations-issue-179--adr-013                              | trace, decision-trace, lifecycle, replay, rationale, alternatives, ghost-path, trace-show, trace-export, run-trace, decision-graph, seq, producer, idempotency                                                                |
| Cascade Breaker          | docs/CASCADE_CIRCUIT_BREAKER.md                                                    | cascade, cascading-failures, circuit-breaker, sliding-window, safety, pause, manual-triage, discord, notifier                                                                                                                 |
| CI Integration           | docs/CI_INTEGRATION.md                                                             | ci, continuous, integration, automation, github, actions                                                                                                                                                                      |
| Automations              | docs/AUTOMATIONS.md                                                                | automation, workflow, trigger, scheduled, webhook, routine                                                                                                                                                                    |
| RALPH Loop               | docs/RALPH_LOOP.md                                                                 | ralph, loop, self-healing, build, test, correction, retry                                                                                                                                                                     |
| Feedback Loops           | docs/FEEDBACK_LOOPS.md                                                             | feedback, signal, backtrack, escalation, feedback-driven, revision, oscillation                                                                                                                                               |
| MCP Integration          | docs/MCP_INTEGRATION.md                                                            | mcp, model context protocol, server, allowed-tools, recipe, filesystem, playwright, browser                                                                                                                                   |
| Knowledge Base           | docs/KNOWLEDGE_BASE.md                                                             | knowledge, prd, decision, adr, wiki, reference, scaffold                                                                                                                                                                      |
| Deprecations             | docs/DEPRECATIONS.md                                                               | deprecation, deprecated, legacy, migration, removal, timeline                                                                                                                                                                 |
| Publication Boundary     | docs/DOCUMENTATION_IA.md, .github/publication-boundary.yaml                        | publication, boundary, public, private, docs-ia, allowlist, fail-closed, strategy, nightgauge-internal, classification, manifest, go-public, visibility                                                                       |
| Skill Assessment         | docs/SKILL_SELF_ASSESSMENT.md                                                      | self-assessment, epilogue, friction, drift, improvement, amendment, skill-drift                                                                                                                                               |
| Progressive Disclosure   | docs/SKILL_PROGRESSIVE_DISCLOSURE.md, docs/decisions/010-progressive-disclosure.md | progressive-disclosure, \_includes, supporting-files, read-directive, skill-size, skeleton, on-demand, option-a                                                                                                               |
| GitHub GraphQL           | docs/GITHUB_GRAPHQL_SCHEMA.md                                                      | graphql, mutation, query, input, schema, project, board, issue, pr, api, github                                                                                                                                               |
| GitHub API Deps          | docs/GITHUB_API_DEPENDENCIES.md                                                    | api, dependency, deprecation, risk, breaking, availability                                                                                                                                                                    |
| Continuous Impr.         | skills/nightgauge-continuous-improvement/                                          | continuous-improvement, review, dogfood, customer, loop, effectiveness, proposal, self-improvement                                                                                                                            |
| Autonomous               | docs/AUTONOMOUS_ORCHESTRATOR.md                                                    | autonomous, cross-repo, scheduler, orchestrator, safety, graph, dependency, wave, cascade                                                                                                                                     |
| Focus Mode               | docs/FOCUS_MODE.md                                                                 | focus, lens, quality, security, performance, reliability, documentation, ux, features, steering, bias, prioritize                                                                                                             |
| PR Merge Stage           | docs/PR_MERGE_STAGE.md                                                             | pr-merge, deterministic, llm, two-path, execution_path, fallback, gate, gh-pr-merge                                                                                                                                           |
| PR Create Stage          | docs/PR_CREATE_STAGE.md                                                            | pr-create, deterministic, llm, two-path, execution_path, fallback, gate, gh-pr-create                                                                                                                                         |
| Forge ADR                | docs/decisions/008-skill-forge-cli.md                                              | adr, forge, gh, glab, gitlab, skill-migration, deprecation, allowlist, parity                                                                                                                                                 |
| Forge Abstraction        | docs/FORGE_ABSTRACTION.md                                                          | forge, abstraction, adapter, interface, lifecycle, router, dispatch, sentinel, gitlab, github, forge-client                                                                                                                   |
| Self-Hosted GitLab Setup | docs/SELF_HOSTED_GITLAB_SETUP.md                                                   | gitlab, self-hosted, ce, ee, operator, setup, runbook, oauth, pat, deploy-token, webhook, ca-cert, tls, omnibus                                                                                                               |
| Self-Hosted GitLab       | docs/FORGE_ABSTRACTION.md#7-ce-vs-ee-feature-matrix-gitlab                         | gitlab, self-hosted, ce, ee, premium, ultimate, edition, tier, license, scoped-label, iteration, push-rules                                                                                                                   |
| Workspace Schema         | docs/decisions/009-workspace-schema-migration.md                                   | adr, schema, schema_version, migration, v1, v2, in-memory, idempotent, workspace, config-migrate                                                                                                                              |
| Mattermost Integration   | docs/MATTERMOST_INTEGRATION.md                                                     | mattermost, integration, bot, webhook, incoming, outgoing, slash, operator, setup, runbook, tunnel                                                                                                                            |
| Skill Evaluation         | docs/SKILL_EVALUATION.md                                                           | eval, evaluation, harness, cross-model, scenario, assertion, regression, haiku, sonnet, opus, matrix, mock, live                                                                                                              |
| Skill Portability        | docs/SKILL_PORTABILITY.md                                                          | portability, portable, cross-adapter, codex, gemini, binary-discovery, NIGHTGAUGE_BIN, vscode-extension-path, preflight, skill-portability, model-tier-advisory, phase-marker, guard.sh, provider-neutral                     |
| Workflow Orchestration   | docs/WORKFLOW_ORCHESTRATION.md                                                     | workflow, orchestration, judge, fan-out, sdk-fanout, dynamic-workflows, native-workflow, subagent-tree, WorkflowEvent, selectExecutor, WorkflowExecutor, orchestration-capability, workflow-journal, quota-gate, gate-metrics |
| Fan-Out Security         | docs/security/WORKFLOW_FANOUT_SECURITY.md                                          | security-review, fan-out, ceiling, absolute-ceiling, outputRef, replay, prompt-injection, secret-exfil, budget-dos, spawn                                                                                                     |
| Adapter Doctor           | docs/ADAPTER_DOCTOR.md                                                             | doctor, preflight, adapter-health, codex login, version, mcp, model-validity, remediation, binary, auth, per-stage, doctor --adapters                                                                                         |

## Companion Repositories

Nightgauge integrates with a separate, closed-source hosted platform
(licensing, billing, team analytics). It is optional: the pipeline runs fully
locally against your own model keys with no account and no server.

## Knowledge Base Usage

When `knowledge_path` is set in pipeline context (auto-scaffolded at issue pickup when `knowledge.enabled: true`), always read `knowledge_path/PRD.md` and `knowledge_path/decisions.md` before implementing — these capture accumulated requirements and architecture decisions for the issue's feature area. Record new decisions using the ADR block format defined in `docs/KNOWLEDGE_BASE.md`. Outcomes and lessons are appended post-retro via `/nightgauge:retro`. See `docs/KNOWLEDGE_BASE.md` for the full schema and lifecycle.

## Compaction Preservation

When compacting conversation history, ALWAYS preserve:

- The full list of modified files and their paths
- Current pipeline stage and issue number
- Any test commands that need to be run
- Error messages from failed operations
- The current git branch name
- Acceptance criteria from the issue being worked on

## Author

nightgauge
