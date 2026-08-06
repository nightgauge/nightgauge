# VSCode extension state fixtures (#356)

Two **captured, redacted snapshots of a real machine**, not hand-authored
examples, plus one synthetic file that exists purely to be **big**. Together
they are the artifact that exposes the #356 defect and the input the regression
tests build their temporary `$HOME` from.

| File                          | What it is                                                                          |
| ----------------------------- | ----------------------------------------------------------------------------------- |
| `bundle-layout.json`          | what is on **disk**: every `nightgauge.nightgauge-vscode-*` bundle directory        |
| `extensions-index.json`       | what VSCode **records**: a redacted copy of `~/.vscode/extensions/extensions.json`  |
| `extensions-index-large.json` | a **synthetic** 120-entry index in the same per-entry shape, minified onto one line |

The second file is the one that matters most. Step 4 of the binary cascade
selects the bundle VSCode records as installed — see
[docs/GO_BINARY.md](../../../../docs/GO_BINARY.md#which-bundle-the-hooks-run--vscodes-record-not-the-biggest-number).
A fixture that only described the disk could not express that rule at all.

## What was captured

- **`bundle-layout.json`** — every
  `~/.vscode/extensions/nightgauge.nightgauge-vscode-*/` directory on the
  capturing machine, plus, for each, the `dist/bin/nightgauge` file's
  existence, executable bit, mode string, **mtime**, byte size, SHA-256, the
  first line of `nightgauge version`, and whether it is the directory
  `extensions.json` records. The top-level `recorded_relative_location` field
  names that directory.
- **`extensions-index.json`** — the real `extensions.json` array, entry order
  preserved.

## When and by what

- Captured while investigating issue #356 (see `captured_at`).
- Captured by `scripts/capture-vscode-bundle-layout.sh`, committed alongside
  these fixtures so the capture is repeatable.
- Re-capture with:

  ```bash
  bash scripts/capture-vscode-bundle-layout.sh \
    > internal/doctor/testdata/vscode-bundles/bundle-layout.json
  bash scripts/capture-vscode-bundle-layout.sh --extensions-index \
    > internal/doctor/testdata/vscode-bundles/extensions-index.json
  ```

## What was redacted

**`bundle-layout.json`** — only the home directory path. A leading `$HOME` in
every emitted path becomes the literal placeholder `~`, so no username or home
directory appears in the committed file. Tests substitute their own
`t.TempDir()` for `~`. Nothing else is rewritten: bundle versions, executable
bits, mtimes, sizes, digests and `nightgauge version` output are verbatim,
because those are the fields under test.

**`extensions-index.json`** — the nightgauge entry is **verbatim** (minus the
home path), because it is our own extension and it is the record under test.
Every **other** publisher's entry is replaced with a structurally identical
anonymized placeholder (`publisherN.extensionN-1.N.0`), so no third-party
extension ids, versions, marketplace GUIDs, or install timestamps reach this
public repository. What survives from those entries is only their **shape and
position** — which is exactly what the parsers must cope with, and which is not
sensitive.

`loadCapturedBundleLayout` and `writeCapturedExtensionsIndex` re-assert the
redaction on every test run, so a careless re-capture cannot land a home path
in the repository unnoticed.

## Why this layout matters

Two bundles are on disk; VSCode records the **second one in glob order** as
installed, and the first is an unrecorded leftover that had persisted for two
days — absent from `extensions.json` and absent from `.obsolete`. Their
`dist/bin/nightgauge` files are byte-identical (same SHA-256, same
`nightgauge version`), so on the capturing machine the wrong selection had no
_behavioral_ consequence — but the selection itself was wrong and unsignalled:

| bundle version   | glob order | recorded installed? |
| ---------------- | ---------- | ------------------- |
| `0.1.1785906439` | first      | no                  |
| `0.1.1785982325` | second     | **yes**             |

Pre-fix, `guard.sh` step 4 and `ResolveBinary()` step 4 both took the **first**
executable glob match (bash globs and `filepath.Glob` are both sorted), i.e.
the unrecorded leftover, silently.

Two properties of this real layout are load-bearing and would have been easy
to get wrong in an invented fixture:

- **mtime is not a usable "newest" signal.** The `binary_mtime` field records
  both: the UNRECORDED bundle carries the LATER mtime. Ranking by mtime picks
  the wrong bundle and still passes a naive local check. The claim is now
  checkable from the artifact itself rather than asserted in a comment.
- **The recorded bundle is not the first in glob order.** `loadCapturedBundleLayout`
  asserts this: a capture where they coincide cannot distinguish the install
  record from first-glob-match, which is the entire point of the fixture.

## What this fixture does NOT represent (read before adding a test)

This capture is from a **maintainer dev install**, so its bundle versions are
the dev scheme: an unsuffixed, epoch-derived `0.1.<seconds>`.
`packages/nightgauge-vscode/scripts/dev-install.sh` runs `vsce package`
**without** `--target`, so its directory names carry no target platform.

**Released and RC installs do not look like this.** VSCode names
platform-specific extension directories
`<publisher>.<name>-<version>-<targetPlatform>`;
`.github/workflows/release.yml` and `.github/workflows/staging.yml` both
package with `vsce package --target <t>` (`darwin-arm64`, `darwin-x64`,
`linux-x64`), and staging runs `npm version 0.2.0-rc.NN` first. So real
directories look like `nightgauge.nightgauge-vscode-0.2.0-rc.23-darwin-arm64`.

That gap is not hypothetical — it shipped once. An earlier #356 attempt
compared bundle versions after trimming everything past the first `-`, which
collapsed `rc.22` and `rc.23` to `0.2.0`, tied them, and silently restored
first-glob-match for the entire RC population. The suite was green, because
this fixture and every synthetic version in the tests were pure dotted digits
and structurally could not express the shipped shape.

So: **a fixture-derived test is necessary but never sufficient here.** Cases
that must hold for real users belong in the tests that construct
`-rc.NN-darwin-arm64`, `-darwin-arm64` and `.vsctmp` directory names directly —
`TestScanVSCodeBundles_RecordedRCBundleWins`,
`TestScanVSCodeBundles_VsctmpOrphanNeverWins`, the record-authority parity
cases in `TestResolveBinary_GuardShParity`, and
`TestGuardShellRecordedBundleWins`.

## `extensions-index-large.json` — the size case

The captured index has 14 entries (~7 KB). That is a perfectly ordinary VS Code
install, and it is small enough to hide two different defects:

- **A superlinear parser.** The round-3 implementation of `guard.sh`'s record
  reader scanned the file with shell builtins only, which is quadratic in file
  size: 11.7 ms per record read at 14 entries, 109 ms at 60, 373 ms at 120 —
  paid 2-3 times per tool call. No test could see it, because every fixture in
  the suite was 1-14 entries.
- **An extraction that only works near the head of the file.** The nightgauge
  entry sits **100th of 120** here; `loadLargeExtensionsIndex` asserts it is not
  in the first ten.

The file is **synthetic and anonymized**, not a capture: a real 120-extension
index cannot be published (third-party ids, marketplace GUIDs, install
timestamps). Its per-entry shape is copied from the captured file, and it is
committed **minified onto a single line**, which is what VS Code actually writes
and the worst case for a text scan. It records exactly one nightgauge bundle,
`nightgauge.nightgauge-vscode-0.2.0-rc.23-darwin-arm64` — an RC + target-platform
directory name, i.e. the release shape rather than the dev-install shape the
capture carries.

There are **no timing assertions in CI**: wall-clock thresholds are flaky on
shared runners. The fixture pins _correctness_ at size; the measurements above
were taken by hand under `/bin/bash` 3.2.57 and recorded in the PR.

## Note on `bash_version`

The `bash_version` field records the shell that ran the capture script, which
is not necessarily the shell that runs the hooks. Hooks run under the system
`/bin/bash`, which on macOS is 3.2.57 — `guard.sh`'s record parser is written
to that floor (no `mapfile`, no associative arrays, no `${var,,}`, no
`[[ =~ ]]`), and the shell tests pin `/bin/bash` explicitly so a bash-4ism
cannot pass CI against Homebrew's 5.x.
