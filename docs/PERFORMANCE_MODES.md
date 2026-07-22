# Performance Modes

Performance modes are named policy envelopes that control how Nightgauge routes
work to configured models. They let users choose a consistent quality, latency,
and resource posture without hard-coding one provider or model into a workflow.

## Modes

| Mode       | Intended use                 | Routing posture                                   |
| ---------- | ---------------------------- | ------------------------------------------------- |
| `economy`  | Routine, well-bounded work   | Prefer the least resource-intensive capable model |
| `balanced` | General development          | Balance capability, latency, and resource use     |
| `quality`  | Complex or high-risk changes | Prefer stronger reasoning and validation capacity |
| `custom`   | Repository-specific policy   | Use explicit per-stage configuration              |

Model availability and capabilities change over time. The active model registry
and local configuration determine the concrete provider/model selection; this
document deliberately does not promise future models or fixed provider prices.

## Configuration

Set the default mode in `.nightgauge/config.yaml` and override individual
stages only when the repository has evidence that a different policy is useful.
Unknown or unavailable models fall back according to the configured adapter and
portable capability floor.

Performance modes do not weaken build, test, security, or merge gates. A less
resource-intensive route must still satisfy the same deterministic completion
criteria.

See [CONFIGURATION.md](CONFIGURATION.md) for the current schema and
[MODEL_EVALUATION.md](MODEL_EVALUATION.md) for measuring routing choices.
