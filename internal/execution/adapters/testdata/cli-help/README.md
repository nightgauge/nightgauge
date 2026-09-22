# CLI help captures (#1617)

Each `.txt` file here is a **captured, real `--help`** of a coding CLI, at the
version its compat manifest (`internal/adaptercompat/manifests/<adapter>.json`)
pins as `max_tested`. `scripts/capture-cli-help.sh` writes them, and its header
lists every isolation and redaction step. No capture is written by hand. A
`.hidden` sidecar records the result of the probes below, which are run by
hand.

## Format and lookup

- The file name is `<adapter>[-<subcommand>]-<version>.txt`. The subcommand is
  the one the adapter's argv starts with: `codex exec` and `opencode run`.
- The first line is `# adapter=<id> version=<x.y.z> command=<command>`. The rest
  is the help text, redacted by the script.
- Exactly one file matches each adapter. `NIGHTGAUGE_FLAG_CONTRACT_HELP_DIR`
  points `TestFlagContract` at another directory in the same layout, such as
  captures from the newest CLIs. Only this directory is held to `max_tested`.
  The contract's self-tests always read this directory, so a wider `-run`
  still passes with the override set.

  ```bash
  NIGHTGAUGE_FLAG_CONTRACT_HELP_DIR=<dir> go test ./internal/execution/adapters -run '^TestFlagContract$'
  ```

- `<capture>.hidden`, such as `claude-headless-2.1.258.txt.hidden`, lists the
  flags that version accepts without listing them in its help, one per line.
  Lines starting with `#` are comments. A sidecar holds only for the version
  in its name: the contract reports one with no capture of that name, and it
  reports a hidden flag in a capture of another version until the probe is
  recorded for that version. Writing the sidecar needs no code edit.
- **The #1639 canary carries a committed `.hidden` sidecar forward onto a
  newer capture.** `scripts/adapter-canary.sh capture-help` copies the sidecar
  named here onto the fresh capture it writes for whatever version it just
  installed (`latest` on the daily/scheduled run, or a PR's proposed
  `max_tested`), without re-running any probe. A `flag-contract` pass on that
  newer version is therefore evidence the flag is still not _listed_ in
  `--help`, never evidence it is still _accepted_ there — that only holds for
  the version actually probed, in the table below. Treat a carried-forward
  pass as inconclusive for the hidden flag specifically, and re-probe it by
  hand (see "Re-capturing" below) once a version bump is otherwise ready.

## What reads them

- `TestFlagContract` in `../../flag_contract_test.go`: every flag an adapter's
  `BuildCommand` emits, over the whole option product, must be an option of its
  capture. The exceptions are the `knownBroken` entries and the `.hidden`
  sidecars, each backed by a probe below.
- `TestManifestRequiredFlagsMatch`: each manifest's `required_flags` is exactly
  the set of flags `BuildCommand` emits.
- `TestHelpOptionParser`: what the parser reads from these files, and that a
  flag named only in description prose is not counted.
- `TestOpenCodeHelpCaptureMatchesTheOpenCodeEvidence`: the opencode capture is
  byte-identical to `../opencode-cli/run-help.txt`.

## What was captured

| Field       | Value                              |
| ----------- | ---------------------------------- |
| Host OS     | macOS 27.0 (Darwin 27.0.0, arm64)  |
| Captured at | 2026-09-13                         |
| Command     | `bash scripts/capture-cli-help.sh` |
| Redaction   | the script's; nothing needed it    |

| File                          | Command               | Install source                                                                                                                                    | Exit code |
| ----------------------------- | --------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- | --------- |
| `claude-headless-2.1.258.txt` | `claude --help`       | npm `@anthropic-ai/claude-code@2.1.258`                                                                                                           | 0         |
| `codex-exec-0.145.0.txt`      | `codex exec --help`   | npm `@openai/codex@0.145.0`                                                                                                                       | 0         |
| `grok-1.0.4.txt`              | `grok --help`         | `https://x.ai/cli/install.sh` with version argument `1.0.4` (installer sha256 `7fd6fdc75d9418b2e58356726fcbf1ae849416f773925da07d0ccc7a60d3e791`) | 0         |
| `opencode-run-1.18.30.txt`    | `opencode run --help` | npm `opencode-ai@1.18.30`                                                                                                                         | 0         |

opencode 1.18.30 prints `run --help` on stderr, so the script captures stdout
and stderr together, as `../opencode-cli/capture.sh` does.

## Not captured

| Adapter   | Reason                                                                                                                                                   |
| --------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `gemini`  | CLI not installed on the maintainer's machine. The manifest pins no `max_tested`, so the script has no version to install and there is no help to check. |
| `copilot` | CLI not installed on the maintainer's machine; the same holds. Its npm package is `@github/copilot`.                                                     |

`TestFlagContract` logs both as not checked and fails once either manifest pins
a `max_tested` without a capture. Their `required_flags` are checked all the
same.

## Probes

The rows below were observed on 2026-09-13 on the pinned versions, installed
the way the script installs them and under the same mitigations: a throwaway
prefix removed afterwards, `env -i` with `HOME` and the XDG directories inside
it, an empty working directory, stdin from `/dev/null`, and every call bounded
to 60 seconds. No credential was present, and no probe reached a model.
`env -i` clears the environment a probe's process sees — no operator
`HOME`, API key or proxy setting reaches it through a variable — but it is
not filesystem isolation: the process still has whatever file access the
account running the script has, and a CLI or installer that reads a path
directly (not through `$HOME`), or one that calls into the OS user database
rather than reading the environment, is not confined by it. The prefix, the
empty working directory and the timeout are what keeps a capture's own files
inside a throwaway location (#1721).

### Accepted but not listed (the `.hidden` sidecars)

| Adapter and version     | Flag               | Probe                                                                                                                                                                                        | Control                                                                                 |
| ----------------------- | ------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------- |
| claude-headless 2.1.258 | `--max-turns`      | `claude -p --max-turns 5` exits 1 with `Error: Input must be provided either through stdin or as a prompt argument when using --print`, as the listed `claude -p --max-budget-usd 1.50` does | `claude -p --bogus-xyz` exits 1 with `error: unknown option '--bogus-xyz'`              |
| grok 1.0.4              | `--no-auto-update` | `grok --no-auto-update --help` prints the help and exits 0                                                                                                                                   | `grok --bogus-xyz --help` exits 2 with `error: unexpected argument '--bogus-xyz' found` |

Claude Code prints its help for `--help` even after an unknown option
(`claude --bogus-xyz --help` exits 0), so its probe uses `-p` with empty input
instead. codex and grok refuse an unknown option before they read `--help`.

grok's `--effort` is not hidden: 1.0.4 lists it as `[aliases: --effort]` of
`--reasoning-effort`, and `grok --effort high --help` exits 0.

### Emitted but refused (`knownBroken`)

| Adapter and version     | Flag                 | Probe                                                                                                                                                                                                                                                                      | Bug   |
| ----------------------- | -------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----- |
| claude-headless 2.1.258 | `--max-tokens`       | `claude -p --max-tokens 5` exits 1 with `error: unknown option '--max-tokens'`                                                                                                                                                                                             | #1716 |
| codex 0.145.0           | `--ask-for-approval` | `codex exec --sandbox read-only --ask-for-approval never --json -` and `codex exec --ask-for-approval never --help` exit 2 with `error: unexpected argument '--ask-for-approval' found`; `codex --ask-for-approval never exec --help` exits 0, so it is a top-level option | #1715 |

### opencode 1.18.30 `run`

ADR-022 § "The command" rests on these rows.

| Flags                                      | Observed                                                                                                                                                                                               |
| ------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `--auto`, `--share`                        | Listed. With no message, `run` exits 1 with `Error: You must provide a message or a command`, as `run` with no flag does                                                                               |
| `--yolo`, `--dangerously-skip-permissions` | Not listed, and accepted exactly as `--auto` is: the same error and exit 1. The bundled source defines both as hidden boolean `run` options and switches on auto-approval when any of the three is set |
| `--mdns`, `--cors x`, `--bogus-flag`       | Refused: exit 1, nothing on stdout, the `run` help on stderr, and no "Unknown argument" text                                                                                                           |

The last row is the strict-mode guardrail #1617 asks for: `run` refuses a flag
it does not define. If a later version accepts `--bogus-flag`, strict mode has
changed; record it here and re-read ADR-022 § 9.

yargs also takes an option under other spellings. These rows used npm
`opencode-ai@1.18.30` as already installed on the capture machine, not a fresh
install, in the same `env -i` isolation:

| Flags                                                                  | Observed                                                                  |
| ---------------------------------------------------------------------- | ------------------------------------------------------------------------- |
| `--dangerouslySkipPermissions`, `--yolo=true`, `--auto.x`, `--share.x` | Accepted as `--auto` is: `Error: You must provide a message or a command` |
| `--Yolo`, `--dangerously_skip_permissions`                             | Refused like `--bogus-flag`: exit 1, the `run` help on stderr             |

The forbidden-flag guard in the tests (`openCodeForbiddenFlagIn`) reduces every
argv token to its kebab-case option name first, so it catches all of these,
the refused ones included.

## Re-capturing

When a manifest's `max_tested` changes, run the script and update the tables
above. The script replaces the adapter's previous capture and leaves its
sidecar alone. Probe each flag the sidecar lists on the new version, then
rename the sidecar after the new capture. `TestFlagContract` fails until the
sidecar has been renamed. A flag the new version refuses is a defect, not a
hidden flag: take it out of the sidecar and give it a `knownBroken` entry and
a bug. Keep the opencode capture on the version of
`../opencode-cli/`, which ADR-022 § 20 re-captures in the same change.
