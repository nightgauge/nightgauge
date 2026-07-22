# Guardrails & Budgets

> Why Nightgauge constrains autonomous agents.

Nightgauge runs LLM-driven pipelines **unattended**. An autonomous agent
with no constraints is one hallucination away from burning budget, corrupting a
branch, or reporting success on work it never did. This doc explains the threat
model, inventories the guardrails and budgets that contain it, and documents a
real incident as a worked example.

> **Single source of truth.** This is the canonical, tool-agnostic record of
> the runaway threat model. It lives in `docs/` (not any one tool's private
> memory) so every agent — Claude, Codex, Copilot, Gemini, Cursor — reads the
> same lessons. Do not record product/process knowledge in a tool-specific
> memory store; put it here.

---

## 1. Threat Model: How an Autonomous Run Goes Wrong

An unconstrained agent loop can fail in ways a human-in-the-loop session never
would, because nobody is watching each step:

| Failure mode              | What it looks like                                                                                                     |
| ------------------------- | ---------------------------------------------------------------------------------------------------------------------- |
| **Hallucinated task**     | The agent acts on a premise it invented — wrong file, wrong issue, wrong goal.                                         |
| **Lost grounding**        | After garbled/delayed tool output or a long context, the agent builds on a false picture instead of re-reading source. |
| **Wasted-motion runaway** | The agent burns turns/tokens on no-op or filler work (retries, polling, "flush" commands) that make no progress.       |
| **Cost/turn runaway**     | A stuck stage spends without bound — the model keeps trying, nobody stops it.                                          |
| **Silent stall**          | The agent idles waiting on something that will never arrive.                                                           |
| **Masked failure**        | The stage exits "success" while the real work (PR, merge, build) never landed.                                         |
| **Cascading failure**     | A systemic break (bad credential, API outage, poisoned base) fails every queued issue the same way.                    |

Newer, more capable models do **not** eliminate these — they can make them
_more_ convincing. A fluent hallucination is harder to catch than a clumsy one.
Capability raises the ceiling on useful work and on plausible-but-wrong work
alike, which is precisely why deterministic guardrails (not "a smarter model")
are the durable defense.

---

## 2. The Guardrails (deterministic, model-independent)

Each guardrail is intentionally **deterministic** — it does not ask an LLM
whether to fire. That is what makes it a backstop against the LLM itself.

| Guardrail                            | Contains                              | Reference                                                                                                                                                                        |
| ------------------------------------ | ------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Per-stage cost caps**              | Cost runaway on a single stage        | [CONFIGURATION → `pipeline.stage_cost_caps`](CONFIGURATION.md#pipelinestage_cost_caps) — hard, deterministic USD ceiling per stage                                               |
| **BudgetEnforcer**                   | Run-level cost runaway                | [CONFIGURATION → `pipeline.stage_cost_caps`](CONFIGURATION.md#pipelinestage_cost_caps) — `pipeline.budget_mode` / `budget_grace_percent`, estimate-vs-actual with a grace buffer |
| **Adaptive forward-progress budget** | Spend-without-progress                | [ADAPTIVE_PIPELINE.md](ADAPTIVE_PIPELINE.md) — semantic forward-progress awareness replaces the dollar-ceiling hard-kill                                                         |
| **Stage timeouts + stall detection** | Silent stall / stuck stage            | idle-vs-elapsed split (#3160); [STAGE_EXIT_DIAGNOSTIC.md](STAGE_EXIT_DIAGNOSTIC.md) for the forensics                                                                            |
| **Stage gates**                      | Masked failure ("skill said success") | [STAGE_GATES.md](STAGE_GATES.md) — deterministic post-condition checks, independent of LLM claims                                                                                |
| **Cascade circuit breaker**          | Cross-pipeline cascading failure      | [CASCADE_CIRCUIT_BREAKER.md](CASCADE_CIRCUIT_BREAKER.md) — sliding-window auto-pause, operator-cleared                                                                           |
| **Failure taxonomy + auto-triage**   | Misclassified / unrecovered failures  | [FAILURE_TAXONOMY.md](FAILURE_TAXONOMY.md), [AUTO_TRIAGE.md](AUTO_TRIAGE.md)                                                                                                     |
| **Health monitoring**                | Drift / reliability erosion over time | [HEALTH_MONITORING.md](HEALTH_MONITORING.md)                                                                                                                                     |
| **Autonomous safety**                | Unsafe cross-repo / wave execution    | [AUTONOMOUS_ORCHESTRATOR.md](AUTONOMOUS_ORCHESTRATOR.md)                                                                                                                         |

The budgets are the **ceiling**; the gates are the **truth check**; the cascade
breaker is the **blast-radius limiter**. Defense in depth — no single mechanism
is trusted to catch everything.

---

## 3. Worked Example: the #3863 session (2026-05-31)

A human-attended session on issue #3863 (the deterministic-Node-resolution fix)
exhibited several of the §1 failure modes in sequence. It is recorded here
because it is a clean, real demonstration of _why the guardrails exist_ — and of
what an **unattended** run would have needed to survive it.

**What happened**

1. **Hallucinated task.** A window of delayed/garbled tool output led the agent
   to invent an unrelated task (forge-abstraction docs) and begin editing `main`
   before catching the error. It reset cleanly — no repo trace — but the
   wrong-premise action was real.
2. **Wasted-motion runaway.** To "flush" the delayed output, the agent fired
   dozens of `echo ping` no-op commands. Pure token/turn burn, zero progress.
3. **Sequencing miss.** The actual `gh pr merge` was caught in an
   interrupted/rejected batch, so a PR with **all CI checks green** sat unmerged
   until the human asked why.
4. **Recurrence during the writeup.** While authoring _this very doc_, garbled
   output struck again — piped `grep` returned `0` for config keys that
   demonstrably existed, and editor views duplicated lines. The fix was to stop
   trusting the piped/streamed output and re-read the source file directly. The
   lesson is self-demonstrating.

**What contained it here:** a human. The human spotted the hallucination, the
filler-command burn, and the un-merged green PR.

**What an unattended run would have relied on instead** — and the gaps this
exposes:

| §1 failure mode (observed)               | Deterministic guardrail that should catch it                                                               | Gap to close                                                                     |
| ---------------------------------------- | ---------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------- |
| Hallucinated task → edits on `main`      | Branch-discipline / never-push-main hooks; scope-drift gate ([STAGE_GATES.md](STAGE_GATES.md))             | **Closed (#4099)**: pre-feature-dev grounding gate (`nightgauge ground`)         |
| Wasted-motion runaway (filler commands)  | Run-level BudgetEnforcer + adaptive forward-progress budget ([ADAPTIVE_PIPELINE.md](ADAPTIVE_PIPELINE.md)) | A budget caps total spend; a _no-progress_ detector is the sharper tool          |
| Sequencing miss (green PR left unmerged) | pr-merge stage gate + reconcile ([PR_MERGE_STAGE.md](PR_MERGE_STAGE.md))                                   | The terminal step needs an explicit "work landed?" confirmation, not best-effort |

**Lessons (apply to every agent, not just this one):**

- **Re-ground before acting on a shaky premise.** If tool output looks garbled,
  delayed, or surprising, STOP and re-read the source of truth (the issue, `git
status`, the file itself) before building on it. A hallucinated premise
  compounds.
- **Never advance with no-op/filler work.** "Flushing" output, speculative
  polling, and retry-for-its-own-sake are runaway in miniature. Issue the next
  _real_ command and read its result.
- **A green PR is not a merged PR.** Terminal actions (merge, close, deploy)
  must be confirmed, not assumed from an earlier success signal.

---

## 4. Design Principle

> Capability is not a safety mechanism. Budgets, gates, timeouts, and the
> cascade breaker are — because they are deterministic and fire regardless of
> what the model believes. As models get more capable, the guardrails get
> _more_ important, not less: more capable models produce more convincing wrong
> answers, and only a model-independent backstop reliably stops them.

When adding a new autonomous capability, ask: _what is its budget, what is its
post-condition gate, and what stops it if it runs away?_ If any answer is "the
model will know better," that is the gap.

---

## Related Documentation

- [CONFIGURATION → `pipeline.stage_cost_caps`](CONFIGURATION.md#pipelinestage_cost_caps) — cost caps + BudgetEnforcer
- [ADAPTIVE_PIPELINE.md](ADAPTIVE_PIPELINE.md) — forward-progress budgets
- [STAGE_GATES.md](STAGE_GATES.md) — post-condition verification
- [CASCADE_CIRCUIT_BREAKER.md](CASCADE_CIRCUIT_BREAKER.md) — cascading-failure safety
- [FAILURE_TAXONOMY.md](FAILURE_TAXONOMY.md) — weighted failure outcomes
- [AUTO_TRIAGE.md](AUTO_TRIAGE.md) — self-heal / recovery registry
- [STAGE_EXIT_DIAGNOSTIC.md](STAGE_EXIT_DIAGNOSTIC.md) — post-mortem forensics
- [HEALTH_MONITORING.md](HEALTH_MONITORING.md) — reliability over time
- [AUTONOMOUS_ORCHESTRATOR.md](AUTONOMOUS_ORCHESTRATOR.md) — cross-repo safety
- [PR_MERGE_STAGE.md](PR_MERGE_STAGE.md) — terminal-stage two-path execution

---

## Author

nightgauge
