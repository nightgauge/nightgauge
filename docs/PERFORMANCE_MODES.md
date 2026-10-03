# Performance Modes

A performance mode is the envelope that every stage's model and effort are
chosen within. Each implementation holds one table of modes: `modeProfiles` in
`internal/intelligence/routing/performance_mode.go` for the Go scheduler, and
`MODE_PROFILES` in `packages/nightgauge-vscode/src/utils/modeProfiles.ts` for
the extension. The two tables mirror each other.

## Modes

| Mode                 | Model floor | Model ceiling | Effort floor | Effort ceiling | Per-stage pins                                  |
| -------------------- | ----------- | ------------- | ------------ | -------------- | ----------------------------------------------- |
| `efficiency`         | `haiku`     | `sonnet`      | none         | `medium`       | none                                            |
| `elevated` (default) | `haiku`     | `opus`        | none         | none           | none                                            |
| `maximum`            | `opus`      | `opus`        | `high`       | none           | `opus` at `high` on the six stages listed below |
| `frontier`           | `haiku`     | `fable`       | none         | none           | none                                            |

- **Envelope.** The router picks each stage's model inside the mode's
  `[floor, ceiling]` band. The ceiling also caps post-failure escalation, the
  `model_routing.minimum_model` floor and a forced retry tier, so a mode that
  caps cost does cap it. Each stage's resolved effort is clamped into the
  effort bounds; when nothing resolves an effort, the effort floor applies.
- **Pins.** A pin replaces routing for its stage. Only `maximum` pins: `opus`
  at effort `high` on `issue-pickup`, `feature-planning`, `feature-dev`,
  `feature-validate`, `pr-create` and `pr-merge`. It pins neither
  `issue-refine` nor `spike-materialize`; its envelope holds those to `opus`,
  with effort at least `high`.
- **Frontier.** `fable` is the ceiling on `feature-planning` and `feature-dev`
  only; every other stage is capped at `opus`. The router reaches `fable` on
  those two stages only for an issue in the top complexity band (`L`/`XL`).
  No other mode can route to `fable` automatically.
- **Thinking policy.** Envelopes carry a thinking-policy axis, but no mode sets
  one. The dispatched model's declared thinking default applies, and
  `CLAUDE_CODE_DISABLE_THINKING` overrides any policy.
- **Budget ceiling.** Under `maximum`, the scheduler's pipeline token ceiling
  is observe-only: it is logged and does not stop the run.
- **Routing advice.** When evaluation evidence exists for an issue's job class,
  the mode sets the posture of the pick: `efficiency` takes the cheapest
  candidate, `elevated` the best quality per dollar, and `maximum` and
  `frontier` the highest quality.

A stage's `NIGHTGAUGE_PIPELINE_STAGE_MODEL_<STAGE>` environment override
overrides the mode for that stage in every mode, a `maximum` pin included. An
explicit `pipeline.stage_models` entry overrides the envelope for its stage,
but not a pin: under `maximum` the pin wins on every stage it pins.
`model_routing.max_model` lowers a mode's ceiling without ever raising it. See
[CONFIGURATION.md](CONFIGURATION.md#capping-automatic-routing-with-max_model)
for how these interact with the envelope.

## How the Active Mode Is Resolved

1. `NIGHTGAUGE_PERFORMANCE_MODE`, when it names one of the four modes.
2. The checkout's `performance-mode.yaml` (`mode: <name>`), written by the
   status-bar picker. It lives in the checkout's private directory
   (`nightgauge layout path checkout performance-mode.yaml`), so each checkout
   and linked worktree has its own. The extension reads the primary workspace
   folder's file first, then the file of the checkout the stage runs in.
3. `elevated`.

No configuration key sets the mode. The execution profile a workspace
advertises reports which layer answered: `env`, `file` or `default`.

A mode never weakens a build, test, security or merge gate. A cheaper route
must still pass the same deterministic completion criteria.

See [MODEL_EVALUATION.md](MODEL_EVALUATION.md) for measuring routing choices.
