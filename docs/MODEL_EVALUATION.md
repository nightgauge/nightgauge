# Model Evaluation

Nightgauge can compare configured model routes using repeatable tasks and
structured outcomes. The goal is to support evidence-based routing without
turning one benchmark into a universal model ranking.

## What is measured

- Completion and deterministic gate results
- Attempts required to reach a valid result
- Latency and token usage
- Provider-reported or locally calculated resource cost
- Task-specific quality signals

Evaluations should be interpreted per job class and pipeline stage. A model that
performs well on documentation work may not be the best route for debugging or
UI implementation.

## Execution modes

Mock execution validates evaluation wiring without invoking a paid model. Live
execution invokes the locally configured provider and may incur provider
charges. Nightgauge never starts a live evaluation merely because mock
evaluation succeeded; the operator must choose the live path.

## Evidence and privacy

Tracked evaluation evidence may contain task identifiers, route identifiers,
usage totals, timing, and gate outcomes over synthetic tasks. It must not contain
prompts, generated source, diffs, credentials, customer content, or private
repository paths.

Telemetry upload is optional and governed by
[TELEMETRY_PRIVACY.md](TELEMETRY_PRIVACY.md). Local evaluation and local routing
remain available when telemetry is disabled.

## Safe interpretation

1. Compare like-for-like tasks and configuration.
2. Require enough samples to distinguish a trend from noise.
3. Treat provider prices and capabilities as versioned inputs, not permanent
   facts in documentation.
4. Keep deterministic quality gates constant across routes.
5. Require human review before evaluation results change repository policy.

Use the CLI help and [CONFIGURATION.md](CONFIGURATION.md) for current commands
and schema fields.
