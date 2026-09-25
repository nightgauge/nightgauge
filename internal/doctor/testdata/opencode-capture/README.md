# OpenCode doctor evidence (ADR-022 § 20)

The three `opencode-1.18.30-*.txt` files beside this directory are **captured,
real OpenCode output**, not hand-authored examples. `capture.sh` regenerates
them and applies the redaction described in its header. Re-capture when the
version policy raises the tested OpenCode version, and update the table below
in the same change.

| File                                        | What it holds                                                                                                                                                                                                                | Read by                                                                                                                             |
| ------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------- |
| `opencode-1.18.30-debug-config-invalid.txt` | `opencode debug config` with `OPENCODE_CONFIG_CONTENT` set to the per-run config `nightgauge opencode config` builds for `anthropic/claude-sonnet-5`: unchanged, with an unknown top-level key, and with `share` set to `42` | No test: evidence of how `debug config` treats a config (the self-test that read it was removed, ADR-022 § 20 2026-09-25 amendment) |
| `opencode-1.18.30-models-configured.txt`    | `opencode models` under the per-run config for `lmstudio/qwen/qwen3.8-27b`                                                                                                                                                   | `TestOpenCodeCatalogProbe` (`../../opencode_test.go`)                                                                               |
| `opencode-1.18.30-models-other.txt`         | `opencode models` under the per-run config for `lmstudio/stub/stub-model`, which does not declare `qwen/qwen3.8-27b`                                                                                                         | `TestOpenCodeCatalogProbe` (`../../opencode_test.go`)                                                                               |

## What was captured

| Field       | Value                                                                                   |
| ----------- | --------------------------------------------------------------------------------------- |
| Host OS     | macOS 27.0 (Darwin 27.0.0, arm64)                                                       |
| Captured at | 2026-09-13                                                                              |
| CLI version | `1.18.30` (`opencode --version`)                                                        |
| Install     | npm package `opencode-ai`                                                               |
| Isolation   | `env -i`; `HOME`, `TMPDIR` and the four XDG base directories in one throwaway directory |
| Endpoint    | the repository's loopback stub provider (`cmd/stub-provider`), never a model server     |
| Credentials | none; OpenCode received no API key                                                      |
| Redaction   | `capture.sh`: the throwaway directory's path and the stub's port; no address remained   |

## What the captures show

| Observation                                                                                                                                                    | Where it decides something                                             |
| -------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------- |
| With the per-run config unchanged, `debug config` exits 0 and its output holds every key the config sets, with the value it sets; `{env:...}` resolves to `""` | Evidence only                                                          |
| An unknown top-level key exits **0**: `debug config` drops it without a word, and the output does not name it                                                  | The exit code alone cannot catch a key a newer version stops accepting |
| A value of the wrong type (`share: 42`) exits **1** with `Configuration is invalid at OPENCODE_CONFIG_CONTENT` and the expected values                         | Evidence only                                                          |
| `opencode models` lists the configured endpoint's model beside the catalog's own `lmstudio` models, and sends the endpoint no request                          | The doctor's catalog probe runs offline                                |

A config the doctor builds for a declared endpoint's model always lists that
model, because the config writes the model's own entry. `models-other` is a
config built for another model, and stands for a listing that lacks the
configured one, which for a declared model only a binary that did not load the
per-run config would print.

The issue that asked for these checks assumed `debug config` exits non-zero on
an unknown key. On 1.18.30 it does not, so a check of a config cannot rely on
the exit code alone. ADR-022 § 20 records the finding; the dispatch-time
self-test that relied on it was removed by the section's 2026-09-25
amendment. `opencode models` was
also run with the stub provider stopped, and listed the same models, so the
catalog probe needs no model server.
