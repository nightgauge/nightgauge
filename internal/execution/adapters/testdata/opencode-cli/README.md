# OpenCode CLI evidence (ADR-022)

`run-help.txt` is a **captured, real `opencode run --help`**, not a
hand-authored example. `TestOpenCodeArgvMatchesCapturedHelp` in
`../../opencode_test.go` reads it and fails when a flag the adapter emits is not
an option of the captured CLI, when a value the adapter passes is not one of
that option's declared choices, or when a flag the adapter must never emit stops
being a real option (the forbidden list would then be guarding nothing).

`capture.sh` regenerates both files and applies the redaction described in its
header. Re-capture when the version policy raises the tested OpenCode version,
and update the table below in the same change.

## What was captured

| Field       | Value                                           |
| ----------- | ----------------------------------------------- |
| Host OS     | macOS 27.0 (Darwin 27.0.0, arm64)               |
| Captured at | 2026-09-12                                      |
| Command     | `opencode run --help`, stdin from `/dev/null`   |
| CLI version | `1.18.30` (`opencode --version`, `version.txt`) |
| Install     | npm package `opencode-ai`                       |
| Exit code   | 0                                               |
| Redaction   | `capture.sh`; nothing needed redacting          |

## Behaviour observed on the same version

ADR-022 rests on behaviour, not only on flags, so the observations it cites were
made on this version, the same day unless a row gives a later date. Each run
used a throwaway directory for all four XDG base directories, a scratch git
repository as `--dir`, and a stub OpenAI-compatible model server on
`127.0.0.1` that recorded every request and returned canned replies, so no
hosted provider and no operator configuration was involved. Every invocation
was bounded with `perl -e 'alarm 90; exec @ARGV'`.
The provider-block rows also used a throwaway `HOME` and an environment cleared
with `env -i`, in which every API-key variable held a fake sentinel value, never
a real key.

| Observation                                                                                                                                                              | How it was observed                                                                                                                                                                                                             |
| ------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Piped stdin with no positional message becomes the user message verbatim; exit 0                                                                                         | A marker string piped to `opencode run --format json ...` arrived as the `user` message in the stub's recorded request                                                                                                          |
| `opencode run` opens no TCP listener                                                                                                                                     | `lsof -a -iTCP -sTCP:LISTEN -p <pid>` was empty while a request was held open for 10 seconds; the only sockets were outbound to the stub                                                                                        |
| A permission that resolves to `ask` is rejected automatically; the run ends and exits 0                                                                                  | `bash: "ask"` printed `! permission requested: bash (...); auto-rejecting`, the tool call failed, and no command ran                                                                                                            |
| `allow` runs the tool without any auto-approve flag; `deny` removes the tool from the model's tool list                                                                  | `bash: "allow"` ran the command; `edit: "deny"` removed `edit` and `write` from the request's tools                                                                                                                             |
| `OPENCODE_DISABLE_PROJECT_CONFIG=1` ignores the repository's `opencode.json` **and** its `AGENTS.md` and `CLAUDE.md`                                                     | Marker strings in each file were absent from the recorded system prompt                                                                                                                                                         |
| `OPENCODE_DISABLE_CLAUDE_CODE_PROMPT=1` drops the repository's `CLAUDE.md` fallback as well as `~/.claude/CLAUDE.md`                                                     | Same method, with a throwaway `HOME`                                                                                                                                                                                            |
| `AGENTS.md` wins over `CLAUDE.md`; `@path` imports in `CLAUDE.md` are never followed                                                                                     | The imported file's marker never reached the model                                                                                                                                                                              |
| An `instructions` entry with an absolute path loads a repository file even with project config disabled                                                                  | The file's marker reached the model                                                                                                                                                                                             |
| The session database is `$XDG_DATA_HOME/opencode/opencode.db` (SQLite, WAL) and holds the full prompt                                                                    | The piped marker was found in its `part` table                                                                                                                                                                                  |
| `opencode export <session> --sanitize` redacts prompt, replies and tool input, and keeps per-message `tokens` and `cost`                                                 | Inspected the exported JSON                                                                                                                                                                                                     |
| `OPENCODE_SERVER_PASSWORD` is not written to stdout, stderr (even at `--log-level DEBUG`), or any file under the XDG directories                                         | Searched all of them for the value                                                                                                                                                                                              |
| `step_finish` carries per-step `tokens` (`input`, `output`, `reasoning`, `cache.read`, `cache.write`) and a `cost` that was `0` for a provider OpenCode has no price for | Read from the `--format json` stream                                                                                                                                                                                            |
| A config provider block whose key is in the bundled catalog inherits that provider's API-key variables                                                                   | A `deepseek` block setting only `baseURL` (the stub) sent `Authorization: Bearer <sentinel>` from `DEEPSEEK_API_KEY`, with `enabled_providers` narrowed to `deepseek`; an `lmstudio` block did the same with `LMSTUDIO_API_KEY` |
| `env: []` or an explicit empty `apiKey` stops that binding; a key outside the catalog binds nothing                                                                      | The same blocks with `env: []`, with `apiKey: ""`, and with both sent no `Authorization` header; an `lmstudio-remote` block sent none with both sentinels set                                                                   |
| 1.18.30 bundles 213 catalog provider keys; `lmstudio` is one, `ollama` and `lm-studio` are not, and every key with a custom loader is one                                | Read from the catalog snapshot and the provider loaders bundled in the binary                                                                                                                                                   |
| The catalog a run loads is the one bundled in the binary                                                                                                                 | No run wrote a catalog to its empty cache, yet the `deepseek` block inherited its catalog entry's key variable                                                                                                                  |
| An `error` event in the `--format json` stream carries the failed request's full URL                                                                                     | A block pointed at a closed loopback port produced an `error` event with `metadata.url`; stderr and `log/opencode.log` did not name the URL                                                                                     |
| `--print-logs --log-level ERROR` still writes `log/opencode.log` in the data directory, and an error goes to it and to stderr                                            | Every run created the file; it stayed empty without an error, and held the same `ERROR` line as stderr with one                                                                                                                 |
| `OPENCODE_AUTH_CONTENT`, when set, is read as the stored logins instead of `auth.json`, so a run whose data directory is empty still has logins                          | Four empty XDG directories and an empty `HOME` under `env -i`: `opencode auth list` read `0 credentials`, and listed `Anthropic oauth` once a fake `OPENCODE_AUTH_CONTENT` was set (2026-09-13)                                 |
| OpenCode exports `OPENCODE_AUTH_CONTENT`, holding every login it has stored, to the processes it starts for a workspace                                                  | Read from the bundled source: creating a workspace sets it to every stored login in the new workspace's environment                                                                                                             |
| `-m` is split on the first slash, and provider keys are case-sensitive                                                                                                   | An `lmstudio` block pointed at the stub: `-m lmstudio/qwen/qwen3.8-27b` sent model `qwen/qwen3.8-27b`; `-m LMStudio/qwen/qwen3.8-27b` sent no request and exited 1 with `ProviderModelNotFoundError` (2026-09-13)               |
