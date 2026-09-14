# Pinned OpenCode config schema (#1634)

`opencode-config.schema.json` is the config schema OpenCode publishes at
`https://opencode.ai/config.json`, **byte for byte as fetched**. It is not
hand-edited and not reformatted (`.prettierignore` and `.gitattributes` exclude
it). The published schema has no `$id` or version of its own, so its identity
is its SHA-256, which `config_schema_sha256` in
`internal/adaptercompat/manifests/opencode.json` records.

`../../schema_contract_test.go` validates every per-run config the builder
generates against it, across a matrix of dispatches, and fails when:

- a generated config does not validate (`TestGeneratedConfigsValidate`);
- a generated config sets a property the schema marks deprecated
  (`TestNoDeprecatedKeys`), or `maxSteps` or `autoshare` by name;
- a config with an unknown top-level key validates, which would mean the
  validator enforces nothing (`TestValidatorRejectsUnknownKey`);
- a safety key is missing, has another value, or is not a property the
  schema defines (`TestSecurityKeysPresentAndKnown`);
- the manifest's hash is not this file's (`TestManifestSchemaHash`).

The tests read only this file. They never fetch the schema.

## What was captured

| Field            | Value                                                              |
| ---------------- | ------------------------------------------------------------------ |
| URL              | `https://opencode.ai/config.json`                                  |
| Fetched          | 2026-09-14                                                         |
| SHA-256          | `e8cb6e287a3852ee3403f4803be5ad6b19db94948037eaa9672125c333427922` |
| Size             | 39039 bytes, no final newline                                      |
| Draft            | JSON Schema 2020-12 (`$schema`)                                    |
| OpenCode version | `1.18.30`, the compat manifest's `max_tested`                      |
| Verified against | all 83 description strings found verbatim in the 1.18.30 binary    |
| Redaction        | none; `capture.sh` checks that no address but 127.0.0.1 appears    |

On the fetch date `1.18.30` was also the newest `opencode-ai` release on npm.
The response's `ETag` was the first 32 hex digits of the SHA-256 above.

## How the schema marks a deprecated property

The 1.18.30 schema does not use the draft 2020-12 `deprecated` keyword. It
marks a deprecated property with a description that starts `@deprecated`,
carried over from the source's doc comments. The test treats either form as
deprecated. On this schema they are: `autoshare` (use `share`), `reference`
(use `references`), `mode` (use `agent`), `layout`, and, on an agent,
`maxSteps` (use `steps`) and `tools` (use `permission`).

The builder sets `mode` on purpose: 1.18.30 merges every `mode.<agent>` over
`agent.<agent>` after every config layer, so the per-run config sets the mode
entry of each built-in primary agent (ADR-022 § 8). It is the one exception the
deprecation test allows, and the test refuses the exception once no generated
config needs it.

## The one external reference

`model`, `small_model` and an agent's `model` also `$ref`
`https://models.dev/model-schema.json#/$defs/Model`, an enum of every model id
in the models.dev catalog. That catalog changes independently of OpenCode
releases, and OpenCode 1.18.30 runs models it does not list, such as a declared
endpoint's. The test gives that URL a stand-in whose `Model` is a string, the
type the pinned schema already declares, and refuses to load any other
external document.

## Re-pinning

Re-pin when the version policy raises `max_tested`:

```bash
bash internal/execution/adapters/testdata/opencode-config-schema/capture.sh
```

With the new version installed, the script fetches the schema and checks it
against that binary. Only after every check passes does it write the file here
and print the SHA-256. Set `config_schema_sha256` to that value and update the
table above in the same change.
