# OpenCode doctor and self-test evidence (ADR-022 § 20)

The three `opencode-1.18.30-*.txt` files beside this directory are **captured,
real OpenCode output**, not hand-authored examples. `capture.sh` regenerates
them and applies the redaction described in its header. Re-capture when the
version policy raises the tested OpenCode version, and update the table below
in the same change.

| File                                        | What it holds                                                                                                                                                                                                                | Read by                                                                          |
| ------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------- |
| `opencode-1.18.30-debug-config-invalid.txt` | `opencode debug config` with `OPENCODE_CONFIG_CONTENT` set to the per-run config `nightgauge opencode config` builds for `anthropic/claude-sonnet-5`: unchanged, with an unknown top-level key, and with `share` set to `42` | `TestOpenCodeSelfTestEvaluatesTheCapturedDebugConfig` (`../../opencode_test.go`) |
| `opencode-1.18.30-models-configured.txt`    | `opencode models` under the per-run config for `lmstudio/qwen/qwen3.8-27b`                                                                                                                                                   | `TestOpenCodeCatalogProbe` (`../../opencode_test.go`)                            |
| `opencode-1.18.30-models-other.txt`         | `opencode models` under the per-run config for `lmstudio/stub/stub-model`, which does not declare `qwen/qwen3.8-27b`                                                                                                         | `TestOpenCodeCatalogProbe` (`../../opencode_test.go`)                            |

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
| With the per-run config unchanged, `debug config` exits 0 and its output holds every key the config sets, with the value it sets; `{env:...}` resolves to `""` | The self-test compares the merged config with the content              |
| An unknown top-level key exits **0**: `debug config` drops it without a word, and the output does not name it                                                  | The exit code alone cannot catch a key a newer version stops accepting |
| A value of the wrong type (`share: 42`) exits **1** with `Configuration is invalid at OPENCODE_CONFIG_CONTENT` and the expected values                         | The self-test refuses a non-zero exit                                  |
| `opencode models` lists the configured endpoint's model beside the catalog's own `lmstudio` models, and sends the endpoint no request                          | The doctor's catalog probe runs offline                                |

The issue that asked for these checks assumed `debug config` exits non-zero on
an unknown key. On 1.18.30 it does not, so the self-test does not rely on the
exit code alone: it also requires the merged config to hold every key the
per-run config sets. ADR-022 § 20 records the finding. `opencode models` was
also run with the stub provider stopped, and listed the same models, so the
catalog probe needs no model server.
