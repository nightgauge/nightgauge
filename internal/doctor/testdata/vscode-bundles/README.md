# VSCode extension bundle layout fixture (#356)

`bundle-layout.json` is a **captured, redacted snapshot of a real machine's
VSCode extension install**, not a hand-authored example. It is the artifact
that exposes the #356 defect and the input the regression tests build their
temporary `$HOME` trees from.

## What was captured

Every `~/.vscode/extensions/nightgauge.nightgauge-vscode-*/` directory on the
capturing machine, plus, for each, the `dist/bin/nightgauge` file's existence,
executable bit, mode string, byte size, SHA-256, and the first line of
`nightgauge version`.

## When and by what

- Captured `2026-08-06T13:17:51Z` while investigating issue #356.
- Captured by `scripts/capture-vscode-bundle-layout.sh` (committed alongside
  this fixture, so the capture is repeatable).
- Re-capture with:

  ```bash
  bash scripts/capture-vscode-bundle-layout.sh \
    > internal/doctor/testdata/vscode-bundles/bundle-layout.json
  ```

## What was redacted

Only the home directory path. The capture script rewrites a leading `$HOME`
in every emitted path to the literal placeholder `~`, so no username or home
directory appears in the committed file. Tests substitute their own
`t.TempDir()` for `~`.

Nothing else is rewritten. Bundle versions, executable bits, sizes, digests
and `nightgauge version` output are verbatim, because those are the fields
under test — a fixture with invented values would not be evidence of anything.

## Why this layout matters

Two bundles are installed. Their `dist/bin/nightgauge` files are
byte-identical (same SHA-256, same `nightgauge version`), so on the capturing
machine the wrong selection had no _behavioral_ consequence — but the
selection itself was wrong and unsignalled:

| bundle version   | glob order | newest? |
| ---------------- | ---------- | ------- |
| `0.1.1785906439` | first      | no      |
| `0.1.1785982325` | second     | **yes** |

Pre-fix, `guard.sh` step 4 and `ResolveBinary()` step 4 both took the **first**
executable glob match (bash globs and `filepath.Glob` are both sorted), i.e.
the **older** bundle, silently. The bundle version is an epoch-suffixed
`0.1.<seconds>` string set by the extension's build, so it — unlike the
binary's `git describe` stamp and unlike the hand-maintained plugin version —
gives a total order over candidates from the filesystem alone, with no `exec`.

Two properties of this real layout are load-bearing and would have been easy
to get wrong in an invented fixture:

- **mtime is not a usable "newest" signal.** On the capturing machine the
  older-versioned bundle's binary had the _later_ mtime (`Aug 5 20:15` vs
  `Aug 5 20:12`) — inverted relative to the bundle version. Ranking by mtime
  picks the wrong bundle and still passes a naive local check.
- **Lexicographic and numeric order coincide today** only because the epoch is
  a fixed 10 digits. The implementations compare component-wise numerically so
  a digit-count change (or any other scheme) cannot silently flip the order.

## Note on `bash_version`

The `bash_version` field records the shell that ran the capture script, which
is not necessarily the shell that runs the hooks. Hooks run under the system
`/bin/bash`, which on macOS is 3.2.57 — `guard.sh`'s version comparison is
written to that floor (no `mapfile`, no associative arrays, no `${var,,}`, no
`sort -V`).
