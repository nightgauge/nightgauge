# Grok CLI catalog fixture (#551)

`grok-models.txt` is a **captured, real `grok models` invocation**, not a
hand-authored example. The doctor's catalog-drift probe (`internal/doctor/adapters.go`
`parseGrokCatalog`) parses exactly this text, and the shape — an auth-status
preamble line, a blank line, `Default model: <id>`, a blank line,
`Available models:`, then a bulleted list where the CLI's own default carries
a `*` marker and `(default)` suffix while every other entry carries a plain
`-` — is the part that is easy to get wrong from memory.

## What was captured

| Field       | Value                                |
| ----------- | ------------------------------------ |
| Host OS     | macOS 26.0 (Darwin 27.0.0, arm64)    |
| Captured at | 2026-08-15                           |
| Command     | `grok models`                        |
| CLI version | `grok 1.0.4 (d846eb93d94d) [stable]` |
| Exit code   | 0                                    |

```
You are logged in with grok.com.

Default model: grok-4.6

Available models:
  * grok-4.6 (default)
  - grok-4.5
```

## Auth does not gate the catalog

The same invocation was also observed **unauthenticated**, on the same CLI
version, with only the preamble line differing:

```
You are not authenticated.

Default model: grok-4.6

Available models:
  * grok-4.6 (default)
  - grok-4.5
```

Both were exit code 0 with an identical `Available models:` section — the
catalog listing is free even when logged out (also recorded as evidence
`M-cat` in `docs/spikes/568-model-identity-axes.md`). `parseGrokCatalog`
therefore never branches on the preamble line's wording; it scans past
whatever that line says and reads the bulleted list under `Available
models:`. The committed fixture keeps the authenticated preamble because
that is the steady-state a configured pipeline runs against.

## Nothing here is redacted

The output names no path, login, or secret — `grok.com` is the product's own
domain, not operator data — so the fixture is committed verbatim.

## Derived cases (read before adding a test)

Drift-detection tests (a registry-served model missing from the catalog, or
the catalog offering a model the registry does not mark served) are built in
`adapters_test.go` by **substituting a bullet line's model id** in this
captured text, not by inventing a new catalog shape from scratch. Malformed/
truncated-output tests follow the same rule: truncate or corrupt this real
capture rather than writing a synthetic shape that may not resemble anything
the CLI actually emits.
