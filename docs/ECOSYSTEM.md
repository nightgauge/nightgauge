# Ecosystem and Integration Boundaries

Nightgauge is designed as a local-first open-source system with replaceable
integration points. This document describes the public product boundary and
contracts without depending on any private service implementation.

## Open-source components

```text
Agent tool
   │
   ├── Agent Skills
   │
   ├── TypeScript SDK / VS Code extension
   │
   └── Nightgauge Go CLI
          ├── GitHub or GitLab
          ├── local repository and pipeline state
          └── configured model provider
```

The Go CLI owns deterministic repository operations and validation. Skills
describe the agent-facing workflow. The SDK and extension provide orchestration
and user-interface surfaces over the same local capabilities.

## Integration contracts

### Model providers

Adapters invoke provider tools using credentials configured by the user.
Nightgauge does not require model traffic to pass through a Nightgauge service.
Capabilities vary by provider, so routing degrades to a documented portable
baseline when a feature is unavailable.

### Source-control forges

Forge behavior is accessed through a common interface. GitHub and GitLab are
implemented and contract-tested; additional implementations can target the
same public abstraction.

### Optional remote integrations

Remote monitoring or orchestration services can integrate through versioned,
authenticated contracts. They are optional consumers of the open-source
product, not prerequisites for local execution. This repository documents only
the public client contract needed to build or operate the open-source side.

### Telemetry

Telemetry is disabled unless explicitly enabled. The allowed event schema,
redaction rules, and user controls are documented in
[TELEMETRY_PRIVACY.md](TELEMETRY_PRIVACY.md).

## Compatibility rules

- Public schemas are versioned before incompatible changes are introduced.
- Local execution must continue to work when optional network integrations are
  unavailable.
- Secrets remain in provider-native or local secret stores and must not enter
  pipeline handoff files or telemetry.
- An integration failure must not silently weaken deterministic quality gates.

See [ARCHITECTURE.md](ARCHITECTURE.md) for implementation details,
[ADAPTER_GUIDE.md](ADAPTER_GUIDE.md) for model adapters, and
[FORGE_ABSTRACTION.md](FORGE_ABSTRACTION.md) for forge integration.
