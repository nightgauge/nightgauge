# Nightgauge Vision

Nightgauge is an open-source governance and orchestration layer for autonomous
software development. It surrounds AI coding agents with deterministic checks,
portable workflows, and auditable state transitions so teams can automate more
of the software lifecycle without giving up control.

## Principles

1. **Models are workers, not authorities.** Compiled checks verify consequential
   state changes instead of trusting an agent's narrative.
2. **Local operation is complete and useful.** The open-source pipeline runs on
   infrastructure and model credentials controlled by the user.
3. **Provider choice matters.** Skills and orchestration should remain portable
   across model providers, agent runtimes, and source-control forges.
4. **Safety is part of the product.** Quality gates, budgets, recovery rules,
   audit records, and explicit approval points are first-class capabilities.
5. **Evidence beats claims.** Nightgauge records outcomes and exposes why a
   pipeline advanced, stopped, retried, or requested human input.

## What the open-source project includes

- Portable Agent Skills for the issue-to-PR lifecycle
- A deterministic Go CLI for validation, state management, and forge operations
- A TypeScript SDK for embedding pipeline orchestration
- A VS Code extension for local operation and visibility
- Configuration schemas, test harnesses, and documented extension points

The local pipeline is not a limited trial. It can run without a Nightgauge
account or hosted service and does not require telemetry.

## Project boundary

This repository contains the open-source product and the documentation needed
to use, understand, secure, and contribute to it. Optional services may
integrate through documented public contracts, but private service
implementations, commercial plans, company strategy, and unreleased roadmaps
are outside this repository.

See [PRODUCT_OVERVIEW.md](docs/PRODUCT_OVERVIEW.md) for current capabilities and
[GOVERNANCE.md](GOVERNANCE.md) for project stewardship.
