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
made on this version the same day. Each run used a throwaway directory for all
four XDG base directories, a scratch git repository as `--dir`, and a stub
OpenAI-compatible model server on `127.0.0.1` that recorded every request and
returned canned replies, so no hosted provider and no operator configuration
was involved. Every invocation was bounded with `perl -e 'alarm 90; exec @ARGV'`.

| Observation                                                                                                                                                              | How it was observed                                                                                                                      |
| ------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------- |
| Piped stdin with no positional message becomes the user message verbatim; exit 0                                                                                         | A marker string piped to `opencode run --format json ...` arrived as the `user` message in the stub's recorded request                   |
| `opencode run` opens no TCP listener                                                                                                                                     | `lsof -a -iTCP -sTCP:LISTEN -p <pid>` was empty while a request was held open for 10 seconds; the only sockets were outbound to the stub |
| A permission that resolves to `ask` is rejected automatically; the run ends and exits 0                                                                                  | `bash: "ask"` printed `! permission requested: bash (...); auto-rejecting`, the tool call failed, and no command ran                     |
| `allow` runs the tool without any auto-approve flag; `deny` removes the tool from the model's tool list                                                                  | `bash: "allow"` ran the command; `edit: "deny"` removed `edit` and `write` from the request's tools                                      |
| `OPENCODE_DISABLE_PROJECT_CONFIG=1` ignores the repository's `opencode.json` **and** its `AGENTS.md` and `CLAUDE.md`                                                     | Marker strings in each file were absent from the recorded system prompt                                                                  |
| `OPENCODE_DISABLE_CLAUDE_CODE_PROMPT=1` drops the repository's `CLAUDE.md` fallback as well as `~/.claude/CLAUDE.md`                                                     | Same method, with a throwaway `HOME`                                                                                                     |
| `AGENTS.md` wins over `CLAUDE.md`; `@path` imports in `CLAUDE.md` are never followed                                                                                     | The imported file's marker never reached the model                                                                                       |
| An `instructions` entry with an absolute path loads a repository file even with project config disabled                                                                  | The file's marker reached the model                                                                                                      |
| The session database is `$XDG_DATA_HOME/opencode/opencode.db` (SQLite, WAL) and holds the full prompt                                                                    | The piped marker was found in its `part` table                                                                                           |
| `opencode export <session> --sanitize` redacts prompt, replies and tool input, and keeps per-message `tokens` and `cost`                                                 | Inspected the exported JSON                                                                                                              |
| `OPENCODE_SERVER_PASSWORD` is not written to stdout, stderr (even at `--log-level DEBUG`), or any file under the XDG directories                                         | Searched all of them for the value                                                                                                       |
| `step_finish` carries per-step `tokens` (`input`, `output`, `reasoning`, `cache.read`, `cache.write`) and a `cost` that was `0` for a provider OpenCode has no price for | Read from the `--format json` stream                                                                                                     |
