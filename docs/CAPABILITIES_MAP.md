<!-- GENERATED FROM capabilities.yaml -- DO NOT EDIT. Run `nightgauge capabilities matrix --write`. -->

# Capabilities Map

Every Nightgauge capability, the surfaces it is exposed on, its open-core
home, and its documentation. Generated from `capabilities.yaml`, which is the
one hand-authored layer of the workspace knowledge graph (ADR-005).

Cell values are explicit by design: `✓` is generally available on that
surface, a status word is that surface at that maturity, and `—` means the
capability is not exposed there. **A blank cell would be unexpressed data
posing as a negative, so the generator never emits one.**

## Capability × surface

| Capability                              | Status | Home | cli   | sdk   | vscode | skills | dashboard | flutter | platform | site | ci  |
| --------------------------------------- | ------ | ---- | ----- | ----- | ------ | ------ | --------- | ------- | -------- | ---- | --- |
| Issue-to-PR pipeline                    | ga     | core | ✓     | ✓     | —      | ✓      | —         | —       | —        | —    | —   |
| Deterministic stage gates               | ga     | core | ✓     | —     | —      | —      | —         | —       | —        | —    | —   |
| Autonomous scheduling                   | beta   | core | beta  | —     | —      | —      | —         | —       | —        | —    | —   |
| Context handoff between stages          | ga     | core | ✓     | ✓     | —      | —      | —         | —       | —        | —    | —   |
| Repository knowledge base               | beta   | core | beta  | beta  | —      | —      | —         | —       | —        | —    | —   |
| GitHub and GitLab support               | ga     | core | ✓     | —     | —      | —      | —         | —       | —        | —    | —   |
| Model adapters                          | ga     | core | —     | ✓     | —      | —      | —         | —       | —        | —    | —   |
| Model evaluation harness                | beta   | core | beta  | beta  | —      | —      | —         | —       | —        | —    | —   |
| Safety guardrails and budgets           | ga     | core | ✓     | —     | —      | —      | —         | —       | —        | —    | —   |
| Audit trail and outcome recording       | ga     | core | ✓     | —     | —      | —      | —         | —       | —        | —    | —   |
| VS Code interface                       | beta   | core | —     | —     | beta   | —      | —         | —       | —        | —    | —   |
| Programmatic embedding                  | beta   | core | —     | beta  | —      | —      | —         | —       | —        | —    | —   |
| Telemetry and privacy controls          | ga     | core | ✓     | ✓     | ✓      | —      | —         | —       | —        | —    | —   |
| Multi-repository workspace              | ga     | core | ✓     | —     | ✓      | —      | —         | —       | —        | —    | —   |
| Action Center and attention producers   | beta   | core | beta  | —     | beta   | —      | —         | —       | —        | —    | —   |
| Workflow orchestration and fan-out      | alpha  | core | alpha | alpha | —      | —      | —         | —       | —        | —    | —   |
| Adapter usage metering and quota        | beta   | core | beta  | —     | beta   | —      | —         | —       | —        | —    | —   |
| Pipeline and codebase health monitoring | beta   | core | beta  | —     | —      | beta   | —         | —       | —        | —    | —   |
| Self-improvement and learning loop      | beta   | core | beta  | —     | —      | beta   | —         | —       | —        | —    | —   |
| Failure taxonomy and auto-triage        | beta   | core | beta  | —     | —      | —      | —         | —       | —        | —    | —   |
| Skill composition and portability       | ga     | core | ✓     | —     | —      | ✓      | —         | —       | —        | —    | —   |
| Publication boundary enforcement        | ga     | core | —     | —     | —      | —      | —         | —       | —        | —    | ✓   |
| Hosted platform client contract         | beta   | both | beta  | beta  | beta   | —      | —         | —       | —        | —    | —   |

## Documentation

| Capability                | Docs                                                                                                                        |
| ------------------------- | --------------------------------------------------------------------------------------------------------------------------- |
| `issue-to-pr-pipeline`    | [ISSUE_TO_PR_WORKFLOW](ISSUE_TO_PR_WORKFLOW.md), [PIPELINE_EXECUTION](PIPELINE_EXECUTION.md)                                |
| `stage-gates`             | [STAGE_GATES](STAGE_GATES.md), [HOOK_CONTRACT](HOOK_CONTRACT.md)                                                            |
| `autonomous-scheduling`   | [AUTONOMOUS_ORCHESTRATOR](AUTONOMOUS_ORCHESTRATOR.md)                                                                       |
| `context-handoff`         | [CONTEXT_ARCHITECTURE](CONTEXT_ARCHITECTURE.md)                                                                             |
| `knowledge-base`          | [KNOWLEDGE_BASE](KNOWLEDGE_BASE.md)                                                                                         |
| `forge-abstraction`       | [FORGE_ABSTRACTION](FORGE_ABSTRACTION.md), [SELF_HOSTED_GITLAB_SETUP](SELF_HOSTED_GITLAB_SETUP.md)                          |
| `model-adapters`          | [ADAPTER_MATRIX](ADAPTER_MATRIX.md), [ADAPTER_GUIDE](ADAPTER_GUIDE.md), [ADAPTER_DOCTOR](ADAPTER_DOCTOR.md)                 |
| `model-evaluation`        | [MODEL_EVALUATION](MODEL_EVALUATION.md), [SKILL_EVALUATION](SKILL_EVALUATION.md)                                            |
| `guardrails-and-budgets`  | [GUARDRAILS_AND_BUDGETS](GUARDRAILS_AND_BUDGETS.md), [CASCADE_CIRCUIT_BREAKER](CASCADE_CIRCUIT_BREAKER.md)                  |
| `audit-and-outcomes`      | [AUDIT_TRAIL](AUDIT_TRAIL.md), [OUTCOME_RECORDING](OUTCOME_RECORDING.md), [STAGE_EXIT_DIAGNOSTIC](STAGE_EXIT_DIAGNOSTIC.md) |
| `vscode-extension`        | [VSCODE_EXTENSION_GUIDE](VSCODE_EXTENSION_GUIDE.md)                                                                         |
| `sdk-embedding`           | [SDK_COOKBOOK](SDK_COOKBOOK.md)                                                                                             |
| `telemetry-controls`      | [TELEMETRY_PRIVACY](TELEMETRY_PRIVACY.md)                                                                                   |
| `multi-repo-workspace`    | [MULTI_REPO_WORKSPACE](MULTI_REPO_WORKSPACE.md)                                                                             |
| `attention-action-center` | [ATTENTION_PRODUCERS](ATTENTION_PRODUCERS.md), [015-decision-requests](decisions/015-decision-requests.md)                  |
| `workflow-orchestration`  | [WORKFLOW_ORCHESTRATION](WORKFLOW_ORCHESTRATION.md), [WORKFLOW_FANOUT_SECURITY](security/WORKFLOW_FANOUT_SECURITY.md)       |
| `usage-and-quota`         | [018-adapter-usage-quota-model](decisions/018-adapter-usage-quota-model.md)                                                 |
| `health-monitoring`       | [HEALTH_MONITORING](HEALTH_MONITORING.md)                                                                                   |
| `self-improvement-loop`   | [SELF_IMPROVEMENT_LOOP](SELF_IMPROVEMENT_LOOP.md), [SELF_IMPROVEMENT_BOUNDARIES](SELF_IMPROVEMENT_BOUNDARIES.md)            |
| `failure-recovery`        | [AUTO_TRIAGE](AUTO_TRIAGE.md), [FAILURE_TAXONOMY](FAILURE_TAXONOMY.md)                                                      |
| `skill-portability`       | [SKILL_PORTABILITY](SKILL_PORTABILITY.md), [SKILL_PROGRESSIVE_DISCLOSURE](SKILL_PROGRESSIVE_DISCLOSURE.md)                  |
| `publication-boundary`    | [DOCUMENTATION_IA](DOCUMENTATION_IA.md), [PUBLIC_CORE_BOUNDARY](PUBLIC_CORE_BOUNDARY.md)                                    |
| `platform-integration`    | [ECOSYSTEM](ECOSYSTEM.md)                                                                                                   |

## Dependencies

| Capability               | Depends on                                                            |
| ------------------------ | --------------------------------------------------------------------- |
| `autonomous-scheduling`  | `issue-to-pr-pipeline`, `guardrails-and-budgets`, `forge-abstraction` |
| `failure-recovery`       | `audit-and-outcomes`                                                  |
| `health-monitoring`      | `audit-and-outcomes`                                                  |
| `issue-to-pr-pipeline`   | `stage-gates`, `context-handoff`, `model-adapters`                    |
| `model-evaluation`       | `model-adapters`                                                      |
| `multi-repo-workspace`   | `forge-abstraction`                                                   |
| `sdk-embedding`          | `context-handoff`                                                     |
| `self-improvement-loop`  | `audit-and-outcomes`                                                  |
| `usage-and-quota`        | `model-adapters`                                                      |
| `vscode-extension`       | `issue-to-pr-pipeline`, `attention-action-center`                     |
| `workflow-orchestration` | `model-adapters`                                                      |

## Totals

23 capabilities.

| Status  | Count |
| ------- | ----- |
| `alpha` | 1     |
| `beta`  | 11    |
| `ga`    | 11    |

| Home   | Count |
| ------ | ----- |
| `both` | 1     |
| `core` | 22    |

## Holes

**Surfaces no capability claims:** `dashboard`, `flutter`, `platform`, `site`.

These are product surfaces no capability in this registry is accountable
for. In the core repository that is expected for sibling-repo surfaces until
the registry is workspace-scoped; it is a defect for any surface this
repository owns.
