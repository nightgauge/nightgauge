# Capabilities Map

This map points to the public source of truth for each major Nightgauge
capability. It describes the open-source product, not private service
implementations or future product plans.

| Capability                | Primary documentation                                                          | Implementation surface    |
| ------------------------- | ------------------------------------------------------------------------------ | ------------------------- |
| Issue-to-PR pipeline      | [ISSUE_TO_PR_WORKFLOW.md](ISSUE_TO_PR_WORKFLOW.md)                             | Skills, Go CLI, SDK       |
| Deterministic stage gates | [STAGE_GATES.md](STAGE_GATES.md)                                               | Go CLI and hooks          |
| Autonomous scheduling     | [AUTONOMOUS_ORCHESTRATOR.md](AUTONOMOUS_ORCHESTRATOR.md)                       | Go scheduler              |
| Context handoff           | [CONTEXT_ARCHITECTURE.md](CONTEXT_ARCHITECTURE.md)                             | SDK and pipeline state    |
| Repository knowledge      | [KNOWLEDGE_BASE.md](KNOWLEDGE_BASE.md)                                         | Go CLI and SDK            |
| GitHub/GitLab support     | [FORGE_ABSTRACTION.md](FORGE_ABSTRACTION.md)                                   | Forge adapters            |
| Model adapters            | [ADAPTER_MATRIX.md](ADAPTER_MATRIX.md)                                         | SDK adapters              |
| Model evaluation          | [MODEL_EVALUATION.md](MODEL_EVALUATION.md)                                     | Eval harness and registry |
| Safety and budgets        | [GUARDRAILS_AND_BUDGETS.md](GUARDRAILS_AND_BUDGETS.md)                         | Scheduler and hooks       |
| Audit and outcomes        | [AUDIT_TRAIL.md](AUDIT_TRAIL.md), [OUTCOME_RECORDING.md](OUTCOME_RECORDING.md) | Local state and CLI       |
| VS Code interface         | [VSCODE_EXTENSION_GUIDE.md](VSCODE_EXTENSION_GUIDE.md)                         | VS Code extension         |
| Programmatic embedding    | [SDK_COOKBOOK.md](SDK_COOKBOOK.md)                                             | TypeScript SDK            |
| Telemetry controls        | [TELEMETRY_PRIVACY.md](TELEMETRY_PRIVACY.md)                                   | SDK and extension         |

## Runtime relationship

```text
Agent runtime → Agent Skill → SDK / VS Code extension → Nightgauge CLI
                                                       ├── repository
                                                       ├── forge API
                                                       └── local pipeline state
```

Optional integrations consume documented contracts and must not be required
for local execution. See [ECOSYSTEM.md](ECOSYSTEM.md) for that boundary and
[PRODUCT_OVERVIEW.md](PRODUCT_OVERVIEW.md) for a user-facing summary.
