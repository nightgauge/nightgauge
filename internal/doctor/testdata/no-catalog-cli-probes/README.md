# No-catalog CLI adapter evidence (#604)

This directory is the negative counterpart to
[`../grok-catalog/`](../grok-catalog/README.md): where that directory records
a **captured, real catalog command** to build a parser against, this one
records the **captured, real absence of one** for `codex`, `claude`, `gemini`,
and `copilot` — the evidence backing each adapter's
`adapterSpec.catalogSkipReason` in `internal/doctor/adapters.go`. Nothing here
is invented; every claim below is either a verbatim captured invocation or a
verified negative (binary not on `PATH`, package not installed).

| Field       | Value                             |
| ----------- | --------------------------------- |
| Host OS     | macOS 26.0 (Darwin 27.0.0, arm64) |
| Captured at | 2026-08-16                        |

No system `timeout(1)` was available on the capture machine (no GNU
coreutils), so every bounded invocation below used
`perl -e 'alarm 30; exec @ARGV' -- <cmd> <args>` with stdin redirected from
`/dev/null` — non-interactive and time-bounded, matching the discipline this
issue requires (never a bare interactive launch, never a command that could
block on a prompt).

## codex — no models/catalog command

| Field       | Value                                   |
| ----------- | --------------------------------------- |
| Command     | `codex --help`                          |
| CLI version | `codex-cli 0.145.0` (`codex --version`) |
| Exit code   | 0                                       |
| Captured to | `codex-help.txt` (verbatim)             |

`codex --help` lists 27 subcommands (`exec`, `review`, `login`, `mcp`,
`plugin`, `doctor`, `sandbox`, `debug`, …) and top-level flags including
`-m, --model <MODEL>` — which **selects** a model for the session, not one
that **lists** the catalog of available models. No subcommand or flag named
`models`, `list-models`, or similar appears anywhere in the captured output
(`grep -i models codex-help.txt` matches nothing — pinned by
`TestNoCatalogEvidence_CodexHelpHasNoModelsCommand`).

## claude — no models/catalog command

| Field       | Value                                        |
| ----------- | -------------------------------------------- |
| Command     | `claude --help`                              |
| CLI version | `2.1.233 (Claude Code)` (`claude --version`) |
| Exit code   | 0                                            |
| Captured to | `claude-help.txt` (verbatim)                 |

`claude --help` lists subcommands (`agents`, `auth`, `doctor`, `mcp`,
`plugin`, `project`, `update`, …) and top-level flags including
`--model <model>` and `--fallback-model <model>` — both **select** a model,
neither **lists** one. No subcommand or flag named `models`, `list-models`,
or similar appears anywhere in the captured output (`grep -i models
claude-help.txt` matches nothing — pinned by
`TestNoCatalogEvidence_ClaudeHelpHasNoModelsCommand`).

## gemini — no CLI installed to probe

`gemini` was not on `PATH` (`which gemini` → `gemini not found`) and no
global npm package provides it (`npm list -g --depth=0 | grep -i gemini` →
no match) on the capture machine. There is no genuine command/output shape
to build a parser against — capturing one would mean fabricating it, which
the fixture-realism rule forbids. No `--help` output is committed here
because none exists; the negative result itself is the evidence.

## copilot — no functioning standalone CLI to probe

The only `copilot` resolvable on the capture machine's `PATH` is a VS Code
Copilot Chat extension bootstrap shim
(`~/Library/Application Support/Code/User/globalStorage/github.copilot-chat/copilotCli/copilot`),
not a genuine standalone GitHub Copilot CLI install (no `@github/copilot` npm
package is installed globally either). Both `copilot --version` and
`copilot --help` produce the same output regardless of the flag given —
captured verbatim in `copilot-help.txt`:

```
Cannot find GitHub Copilot CLI (https://docs.github.com/en/copilot/how-tos/set-up/install-copilot-cli)
Install GitHub Copilot CLI? ['y/N']
```

Exit code 0 in both cases — with stdin redirected from `/dev/null` the shim's
own install prompt reads EOF and defaults to "N" rather than hanging, so the
invocation is non-interactive-safe, but it never reaches a real CLI to probe
a catalog command against.

## Revisiting this decision

Each `catalogSkipReason` string names the vendor whose release could close
this gap. When a future `codex`/`claude` release adds a models-listing
command, or a genuine `gemini`/`copilot` CLI becomes available to capture
against, wire it the same way `grok` was wired in #602: a real invocation,
committed verbatim, parsed by a dedicated `parse<Adapter>Catalog` function,
with drift tests built by mutating the captured fixture — never a shape
authored from memory.
