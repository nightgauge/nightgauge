# Local model server evidence (#1633)

The JSON files here are **captured, real responses** from an LM Studio server and
an Ollama server on loopback, not hand-authored examples. `local_test.go` in
`../..` serves them from `httptest` servers and checks what local model
discovery (`ResolveLocal`, `DiscoverLocal` in `../../local.go`) reads from them.

`capture.sh` regenerates all three and applies the redaction described in its
header. Re-capture when the server versions below change, and update the tables
in the same change.

## What was captured

| File                              | Request                                     | Server                     |
| --------------------------------- | ------------------------------------------- | -------------------------- |
| `lmstudio-api-v0-models.json`     | `GET /api/v0/models`, all fields            | LM Studio 0.4.24+1 (macOS) |
| `ollama-api-show-num-ctx.json`    | `POST /api/show` `{"model":"qwen3-ctx32k"}` | Ollama 0.32.11 (Homebrew)  |
| `ollama-api-show-no-num-ctx.json` | `POST /api/show` `{"model":"qwen3:0.6b"}`   | Ollama 0.32.11 (Homebrew)  |

| Field       | Value                                                                                      |
| ----------- | ------------------------------------------------------------------------------------------ |
| Host OS     | macOS 27.0 (Darwin 27.0.0, arm64)                                                          |
| Captured at | 2026-09-14                                                                                 |
| LM Studio   | `qwen/qwen3.8-27b` (MLX 8-bit) loaded; an embedding model not loaded                       |
| Ollama      | `qwen3:0.6b` as pulled; `qwen3-ctx32k` is `FROM qwen3:0.6b` plus `PARAMETER num_ctx 32768` |
| Addresses   | Both servers on `127.0.0.1`; `capture.sh` refuses any other root                           |
| Redaction   | `capture.sh`; Ollama's bulk fields and `modified_at` are dropped, whitespace is Prettier's |

## Field names observed

- **LM Studio `GET /api/v0/models`**: `data[]` entries with `id`, `object`,
  `type`, `publisher`, `arch`, `compatibility_type`, `quantization`, `state`
  (`loaded` or `not-loaded`), `max_context_length`, `loaded_context_length`
  (loaded models only) and `capabilities` (`["tool_use"]` for the Qwen model,
  absent for the embedding model). The loaded Qwen model reports
  `max_context_length` 262144 and `loaded_context_length` 131072. No field
  reports an output cap or whether the model reasons. (LM Studio's newer
  `GET /api/v1/models` does report reasoning; discovery does not read it.)
- **Ollama `POST /api/show`**: `parameters` (the Modelfile's parameters, one
  `name value` pair per line), `details`, `model_info` and `capabilities`
  (`["completion","tools","thinking"]`), beside the dropped `license`,
  `modelfile`, `template`, `tensors` and `modified_at`. `num_ctx` is in
  `parameters` only when the Modelfile sets it. `model_info` carries
  `qwen3.context_length` 40960, the context the model was trained for, not
  the one it is loaded with. An unknown model is HTTP 404
  `{"error":"model '<name>' not found"}`.

## Behaviour observed on the same versions

- Ollama loads a model whose Modelfile sets no `num_ctx` with a default it
  derives from the machine's memory, capped at the trained context. With both
  models loaded, `GET /api/ps` reported `context_length` 40960 for
  `qwen3:0.6b` and 32768 for `qwen3-ctx32k`. `/api/ps` lists a model only while
  it is loaded, so discovery, which asks once per process, does not read it,
  and a model without `num_ctx` is unresolved.
- The Ollama server was started for the capture with its own temporary model
  directory and `OLLAMA_HOST=127.0.0.1:<port>`, and removed afterwards.
