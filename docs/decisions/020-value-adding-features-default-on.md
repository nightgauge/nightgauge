# Value-Adding Features Default ON — an opt-out is for footprint and cost, never for hiding value

**Date:** 2026-09-06
**Author:** nightgauge
**Status:** Decided
**Issue:** #1513

---

## Executive Summary

**A feature that adds value to a workspace ships enabled. The flag that turns it
off exists so a user can decline a cost they can name — repo footprint, or
tokens per run — and for no other reason.**

A feature shipped behind an opt-in defaults to _off for everybody_, and "off for
everybody" is indistinguishable from "not built". The knowledge base was the
case that forced the rule: it had been fully implemented — scaffolding, recall,
graduation, telemetry, a VS Code tree, a dashboard — and `knowledge.enabled`
defaulted to `false`, with no rationale recorded anywhere and no ADR behind it.
Every repo that never wrote the key logged

```
Skipped: knowledge.enabled=false and knowledge_path is null
```

on every pickup, and accumulated nothing. The corpus the recall ranking exists
to search was empty by default.

This ADR records the general rule and the specific flip: `knowledge.enabled`
now defaults to **`true`**, including — especially — in a repo with no
`knowledge:` section at all.

## Context

### The default was undocumented and self-contradictory

Nothing recorded _why_ the knowledge base was opt-in. `docs/KNOWLEDGE_BASE.md`
stated the default flatly ("The knowledge base is **opt-in** — product default
`knowledge.enabled: false`") with no rationale and no link to a decision.

Worse, the documentation did not agree with itself. `docs/CONFIGURATION.md`
§ _Knowledge Base Configuration_ documented `enabled` with **Default `true`**,
and `NIGHTGAUGE_KNOWLEDGE_ENABLED` with default `true`, while
`config.KnowledgeConfig.IsEnabled()` returned `false` for an unset field. Two
reference tables, opposite answers, and the code sided with the one that had no
rationale. That is the signature of a default nobody chose.

The same drift had already been found one level down: `auto_scaffold`'s Go
doc-comment claimed "defaults to false" while the docs, the table and the SDK
reader all treated unset as on — and nothing in Go read the field at all, so
the contradiction never surfaced (#1205). An unchosen default rots quietly.

### An opt-in default is not neutral

The argument for opt-in is that it is the conservative choice. It is not: it is
a choice, made once, on behalf of every workspace that will ever exist, by
whoever typed the zero value. Conservatism would be asking; a default is an
answer.

Both directions have a cost. Default-on costs a workspace some files and some
tokens, and the workspace can decline in one line. Default-off costs the
workspace the entire feature, and the workspace cannot decline _that_, because
it never learns the feature was there. Only one of those two costs is
recoverable by the person paying it.

## Decision

### Decision 1 — Value-adding features default ON

A feature that adds value to a workspace defaults to enabled. Opt-outs exist for
**footprint** and **cost**, never to hide value.

The corollary is procedural: **a default-off flag must record its reason next to
the flag.** A default with no rationale next to it is a bug, not a policy — the
reader cannot tell an intentional default from an uninitialized one, and neither
can the next agent to touch it. Ship no more `enabled: false` defaults with an
empty "why".

This does not override the existing rule that anything destructive, anything
that spends money outside the run's own budget, or anything that transmits data
off the machine stays opt-in. Those are not "value-adding features with a
footprint"; they are decisions the user has to make.

### Decision 2 — `knowledge.enabled` defaults to `true`, and absent means the default

`knowledge.enabled` now resolves to `true` when unset, at every layer that
resolves it. **"Key absent" means the default, not `false`** — including a nil
`Config.Knowledge`, which is precisely the "no `knowledge:` section at all" case
this change exists for.

This is a behaviour change for every repo with no `knowledge:` section: those
repos begin scaffolding `.nightgauge/knowledge/` at issue pickup. That is the
intent.

The readers had each independently encoded "absent ⇒ off", in idioms that read
as harmless:

| Layer            | Old idiom                              | New idiom                                      |
| ---------------- | -------------------------------------- | ---------------------------------------------- |
| Go resolver      | `if k.Enabled == nil { return false }` | `… { return true }`                            |
| SDK scaffold     | `if (!config.enabled)`                 | `if (config.enabled === false)`                |
| VS Code commands | `get<boolean>("enabled", false)`       | `get<boolean>("enabled", true)`                |
| Skill shell      | `jq -r '.knowledge.enabled // false'`  | `jq -r '.knowledge.enabled // true'`           |
| Skill Python     | `'true' if cfg.get(...) else 'false'`  | `'false' if cfg.get(...) is False else 'true'` |

Every one of them collapses "unset" and "false" into one branch, and flipping
the stored default alone would have changed nothing for the repos that matter —
the ones that store no value. Falsy-testing an optional boolean is the bug; the
default is only the symptom.

### Decision 3 — the opt-out stays, with its two reasons written down

`knowledge.enabled: false` remains supported and is documented beside the flag
with the only two reasons to reach for it:

- **Repo footprint.** The knowledge base writes files under
  `.nightgauge/knowledge/` and commits them, so the working tree and its history
  grow.
- **Per-run token cost.** Recall and enrichment add tokens to every pipeline
  run.

Naming the reasons is the point. An opt-out with a stated purpose is a product
decision a user can evaluate; an opt-out with no stated purpose is where the
next unchosen default hides.

### Decision 4 — `auto_scaffold` keeps its default

`knowledge.auto_scaffold` stays `true` and stays gated by `enabled` (#1205,
`IsAutoScaffold`). It needed no change: it already defaulted on, and its gate on
the parent flag is the ADR-005 safety rule that a project which opts out of the
KB must not have directories scaffolded into its tree by a nested flag it never
looked at. That rule is unaffected — `enabled: false` still forces
`auto_scaffold`, `workspace_scoped` and telemetry off.

## Consequences

- Repos with no `knowledge:` section start scaffolding at pickup and start
  paying the recall/enrichment token cost. One line of config declines it.
- `nightgauge init` now writes `enabled: true` with the opt-out reasons in a
  comment, so a new project sees the choice rather than inheriting it.
- `--knowledge-enabled` on `nightgauge knowledge scaffold` defaults to `true`,
  matching the config default; callers that resolved config and passed the value
  explicitly are unaffected.
- The `docs/CONFIGURATION.md` ↔ `docs/KNOWLEDGE_BASE.md` contradiction is
  resolved in favour of the table that was already right.

## Implementation

Issue #1513.
