# OpenCode failure captures — provenance

How opencode 1.18.30 reports a failed model request, captured by
[`scripts/capture-opencode-failure-fixture.sh`](../../../../scripts/capture-opencode-failure-fixture.sh)
(#1631). They are the evidence the OpenCode clauses of
[`../../table.json`](../../table.json) are written against, and
`internal/orchestrator/opencode_failure_kinds_test.go` runs each `.stderr`
through the scheduler's own reason builder and classifier.

| Field           | Value                                                                   |
| --------------- | ----------------------------------------------------------------------- |
| Capture date    | 2026-09-14                                                              |
| CLI version     | `1.18.30` (`opencode --version`; the script refuses any other)          |
| Command         | `opencode run --format json --print-logs --log-level ERROR -m <model>`  |
| Model servers   | `127.0.0.1` only: the stub provider (#1618) or a one-status server      |
| Sandbox         | `env -i`, throwaway `HOME`, `TMPDIR` and four XDG directories           |
| Redaction       | `internal/execution/testdata/redact-opencode.jq`, then fixed ids/clocks |
| Credential scan | `scripts/capture-opencode-fixture.sh --check` on every file             |

Each leg is one `opencode run` and leaves `<name>.stderr` (what OpenCode
printed on stderr) and `<name>.jsonl` (its `--format json` stream). Every run
exited 1.

| Leg                    | Server                                                            | stderr carries                                               | Stream `error` event   |
| ---------------------- | ----------------------------------------------------------------- | ------------------------------------------------------------ | ---------------------- |
| `overflow-openai`      | stub `overflow`: 400, OpenAI-compatible `context_length_exceeded` | `AI_APICallError: This model's maximum context length is …`  | `ContextOverflowError` |
| `overflow-lmstudio`    | 400, a message holding LM Studio's fragment (below)               | `AI_APICallError: … greater than the context length …`       | `ContextOverflowError` |
| `overflow-ollama`      | 400, a message holding Ollama's fragment                          | `AI_APICallError: prompt too long; exceeded max context …`   | `ContextOverflowError` |
| `overflow-llamacpp`    | 400, a message holding the llama.cpp server's fragment            | `AI_APICallError: … exceeds the available context size …`    | `ContextOverflowError` |
| `provider-error`       | stub `error`: 500 (the negative control)                          | `AI_APICallError: stub-provider: internal error`             | `APIError`             |
| `auth`                 | 401, empty body                                                   | `AI_APICallError: Unauthorized`                              | `APIError`             |
| `server-down`          | a closed loopback port                                            | `AI_APICallError: Cannot connect to API: Unable to connect…` | `APIError`             |
| `model-not-configured` | stub, `-m lmstudio/qwen/not-a-configured-model`                   | `ProviderModelNotFoundError: Model not found: …`             | `UnknownError`         |

`lmstudio-unknown-model.json` is not an OpenCode run: a one-token request to
the LM Studio on `127.0.0.1:1234` naming a model id it does not have, and the
model id that answered. With one model loaded, LM Studio answered with that
model and no error, so a missing model on LM Studio is not a failure there is
text to classify.

## Where the overflow wording comes from

The four overflow legs are what the table's `context-window-exceeded` rule is
written against, and only the first is a whole message from a server this
repository runs, the stub provider (#1618). The other three messages are
sentences built around one fragment each. The fragments are not invented: each
is a pattern OpenCode's own overflow recogniser matches, read from the
`opencode` 1.18.30 binary, and OpenCode's source names the server each belongs
to. The list at
[`packages/opencode/src/provider/error.ts`](https://github.com/anomalyco/opencode/blob/334ab4707c/packages/opencode/src/provider/error.ts)
(commit `334ab4707c`) carries one comment per pattern; 1.18.30 moved the list
to `packages/llm/src/provider-error.ts` without the comments and kept every
pattern below.

| Fragment                                            | OpenCode's comment             | Table clause, after `ai_apicallerror` |
| --------------------------------------------------- | ------------------------------ | ------------------------------------- |
| `context[_ ]length[_ ]exceeded`                     | Generic fallback               | `context_length_exceeded`             |
| `maximum context length is \d+ tokens`              | OpenRouter, DeepSeek, vLLM     | `maximum context length is`           |
| `greater than the context length`                   | LM Studio                      | `greater than the context length`     |
| `prompt too long; exceeded (?:max )?context length` | Ollama explicit overflow error | `prompt too long; exceeded`           |
| `exceeds the available context size`                | llama.cpp server               | `exceeds the available context size`  |

So the sentence around each fragment in `overflow-lmstudio`, `overflow-ollama`
and `overflow-llamacpp` is the capture's, and the fragment is the server's as
OpenCode records it. What the legs show for real is that opencode 1.18.30
carries such a message to stderr inside `AI_APICallError: `, and that it names
each of them `ContextOverflowError` itself.

## What the captures change

They are the observations ADR-022 § Failure wording records. Two assumptions
of #1631 did not hold: a down local server never prints `ECONNREFUSED`, and a
model the loopback LM Studio lacks is not an error. The classification rules
follow what was observed.

## Re-capturing

```bash
bash scripts/capture-opencode-failure-fixture.sh
```

It needs `opencode` 1.18.30, `go`, `jq`, `git`, `perl`, `python3` and `curl`,
takes about three minutes (the down-server and 500 legs wait out OpenCode's
retries), and writes nothing unless every file passes the credential scan.
Raising the version re-captures this directory in the same change as ADR-022
§ 20's other captures.
