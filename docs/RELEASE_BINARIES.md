# Release Binaries Ship Unstripped

**Date:** 2026-11-09
**Status:** Decided
**Issue:** #3091, #3102, #3104, #3161, #3192

---

## Decision

Every Go binary Nightgauge ships is built **without** the linker strip flags
`-s` and `-w`. That covers the darwin and linux binaries in the release
archives, the binaries bundled in each VSIX, and the Homebrew cask's. Each
binary keeps its symbol table and its DWARF debug information, and the release
targets keep that DWARF uncompressed (`-compressdwarf=false`). The toolchain is
go1.27.4. Every RC also scans its shipped binaries at three neighbouring version
strings, and a release is promoted only from an RC whose margin scan was clean
([Linux amd64 margin and the margin scan](#linux-amd64-margin-and-the-margin-scan-3192)).

A scan finding is fixed at its cause. Owner rule: a finding is never reported
to the vendor as a false positive (no WDSI or "incorrectly detected"
submission), never allowlisted, and never called a false positive as a reason
to publish. A false-positive submission once got the Marketplace publishing
account banned.

One standing exception predates the rule, and the owner kept it (#3019,
reconfirmed in #3106): the `Windows_API_Function` entry in `scripts/vt-scan.sh`'s
YARA allowlist. InQuest's rule matches the Windows API names that every Go
binary's standard library carries (`os.ReadFile`, `os.WriteFile`). It describes
itself as "not an indication of malicious behavior", gives no verdict, and is
not an engine detection, so it does not count in VirusTotal's detection ratio.
It is never hidden: every scan report records it under `yara_allowlisted`, and
the summary line names it for each file. The rule above governs everything
else. Any engine verdict, and any other YARA match, is a finding fixed at its
cause. Adding an allowlist entry is an owner decision, never a session's.

## Context

The bounded RC scan of v0.8.3-rc.2 (`44d3e102`) reported
`engine Microsoft: malicious Trojan:Script/Wacatac.C!ml` on
`nightgauge_0.8.3-rc.2_linux_amd64.tar.gz/nightgauge` (sha256 `d52cc29c…`),
the same binary the linux-x64 VSIX bundles. Every other engine, YARA rule and
file was clean. v0.8.2's Linux binary (`72250c70…`) scanned clean. It was
built with the same toolchain (go1.26.10) and the same flags, and it is 4 KB
smaller. No embedded file or script-like string differs between the two. The
model scores this Go ELF right at its threshold, and a small code change
tipped it over.

The flagged binary was reproduced byte for byte from `44d3e102`. Variants that
differ only in linker flags were then built with
`CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "<flags> -X main.version=<v> -X 'main.vendor=Edibu, LLC'"`
and each was scanned with `scripts/vt-scan.sh`:

| Build                                 | Linker strip flags | Symbol table | DWARF | sha256 (first 12) | Microsoft    |
| ------------------------------------- | ------------------ | ------------ | ----- | ----------------- | ------------ |
| 0.8.3-rc.2 (shipped bytes)            | `-s -w`            | no           | no    | `d52cc29caf10`    | Wacatac.C!ml |
| 0.8.3 final (what release.yml builds) | `-s -w`            | no           | no    | `7322424b3632`    | Wacatac.B!ml |
| 0.8.3-rc.2                            | `-w`               | yes          | no    | `4fd21621f311`    | Wacatac.C!ml |
| 0.8.3 final                           | `-w`               | yes          | no    | `989965811236`    | Wacatac.C!ml |
| 0.8.3-rc.2                            | none               | yes          | yes   | `78205f1a011a`    | clean        |
| 0.8.3 final                           | none               | yes          | yes   | `0db431e73d44`    | clean        |

In this sample every build without DWARF was flagged and both builds with it
were clean. The symbol table alone did not help. That made DWARF the best
available lever, not a guarantee: v0.8.4 below shipped an unstripped binary
with DWARF and was still flagged. Across the builds scanned so far, all four
stripped or `-w` builds were flagged and three of four unstripped builds were
clean. Each file's report is on the VirusTotal web UI
under its sha256.

## v0.8.4 counterexample

The v0.8.4 release scan (`release.yml` run 42611150909, tag `v0.8.4` at
`f8d3fe03`) held the draft. Microsoft flagged the final unstripped Linux binary
(sha256 `3ba7e66c…`) as `Wacatac.B!ml` with DWARF present, and
`release-dwarf-check` printed `ok` for it. The v0.8.4-rc.1 binary built from
the same commit (`29fe75c6…`) scanned clean; the two differ only in the
embedded version string. Unstripped binaries remain the release rule, but DWARF
does not keep a binary clean. The cause was the dependency set; see
[Dependencies that move the Microsoft model's score](#dependencies-that-move-the-microsoft-models-score).

| Build                    | Linker strip flags | Symbol table | DWARF | sha256 (first 8) | Microsoft    |
| ------------------------ | ------------------ | ------------ | ----- | ---------------- | ------------ |
| 0.8.4-rc.1 (`f8d3fe03`)  | none               | yes          | yes   | `29fe75c6`       | clean        |
| 0.8.4 final (`f8d3fe03`) | none               | yes          | yes   | `3ba7e66c`       | Wacatac.B!ml |

The same scan matched InQuest's YARA rule `Microsoft_Excel_Hidden_Macrosheet`
in `extension/dist/extension.cjs`: the bundle assigned the string `hidden` to
a `displayState` field. That is fixed at its cause in
`StallStatusBarItem.ts`, and the check below keeps it from shipping again.

## YARA text-pattern check

`cmd/release-text-pattern-check` fails a release when a shipped text file
matches a YARA text pattern that has already flagged a Nightgauge release.

- The patterns live in `configs/release-text-patterns.yaml`. Each entry carries
  the `rule`, its `source`, the `release` it hit, and the rule's string as a Go
  RE2 `pattern` (`(?i)` for YARA's `/i`). It is seeded with both text
  alternatives of `Microsoft_Excel_Hidden_Macrosheet` (v0.8.4).
- It reads every regular entry of each `.tar.gz` and `.vsix` (or a raw file).
  An entry is text when its first 8 KiB hold no NUL byte and it is not an ELF
  or Mach-O binary; each text entry is matched line by line.
- It exits `0` when at least one text file was checked and none matched, and
  `1` on any match, naming the entry, line, rule and release. It exits `2` with
  a `HARNESS ERROR` line when it asserted nothing: bad usage, an unreadable
  path or patterns file, an empty or invalid pattern list, or no text file.
- `staging.yml` runs it in the `dry-run` job and `release.yml` runs it before
  the draft is scanned, both right after the DWARF check. Tests in the same
  package prove the step's position and that it fails closed, and that a
  fixture containing `displayState = "hidden"` fails while the fixed bundle
  text passes. `scripts/ci-local.sh` runs them through `go test ./...`.
- When a new YARA rule matches a release, add its text strings here with the
  release that hit, and fix the matching text at its cause. Never allowlist it.

## Rejected: `-w` only

Dropping `-s` but keeping `-w` restores the symbol table and still omits DWARF.
Rows 3 and 4 show that it was still flagged. It saves size and fixes nothing.

## Cost

Each binary grows from about 36.7 MB to about 51.7 MB. That is the price of
not shipping a binary Defender quarantines. `scripts/vt-scan.sh` already sends
files over 32 MB through VirusTotal's large-file upload URL (#3011), so the
scan needs no change.

## Enforcement

- `Makefile` `LDFLAGS`/`RELEASE_LDFLAGS` and `.goreleaser.yml` `ldflags` carry
  no `-s` or `-w`. Each has a comment that cites this record.
  `TestNoLinkerStripFlags` in `cmd/release-dwarf-check` fails, naming this
  record, if either flag returns. `TestReleaseBuildsKeepDWARFUncompressed`
  fails if a release target in the Makefile, or the `.goreleaser.yml` ldflags,
  lose `-compressdwarf=false`.
- `cmd/release-dwarf-check` reads the packaged files, not the flags. It opens
  every Go binary in each `.tar.gz` and `.vsix` (or a raw binary) with Go's
  `debug/elf` or `debug/macho` and requires `DWARF()` to yield data, stored
  uncompressed: no ELF `.debug_*` section flagged `SHF_COMPRESSED`, no
  `.zdebug_*` and no Mach-O `__zdebug_*`. It exits `0` when every binary
  carries uncompressed DWARF and `1` when any lacks DWARF or compresses it. It exits `2`
  with a `HARNESS ERROR` line when it checked nothing: no path, an unreadable
  path, or an archive with no Go binary.
- `staging.yml` runs it in the `dry-run` job, which the RC's `rc-scan` job
  needs. `release.yml` runs it before the draft is scanned. Both run it right
  after `scripts/verify-archive-binaries.sh`, and both fail on exit `1` or `2`.
  Tests in the same package prove the step's position and that it fails
  closed.
- The detector's regression tests build a small program for linux/amd64 and
  darwin/arm64 stripped, with compressed DWARF and with uncompressed DWARF, and
  check each raw, in a `.tar.gz` and in a `.vsix`. `scripts/ci-local.sh` runs
  them through `go test ./...`.
- The margin scan and the release gate are described in
  [their own section](#linux-amd64-margin-and-the-margin-scan-3192).

## A Microsoft `!ml` finding on a Go binary

Check it against this record first. Are the binaries still unstripped, and did
`release-dwarf-check` pass in the run that built them? If not, restore DWARF.
A finding in an RC's margin builds counts the same as one in its own files; see
[the margin scan](#linux-amd64-margin-and-the-margin-scan-3192).
Then check that `go.mod` still holds the pinned dependency set below
(`TestScoreSensitiveModulesStayPinned`). If they are unstripped, pinned and
still flagged, the cause is something else. Find it and fix it there. Never report it to the vendor as a
false positive, never allowlist it, and never publish on the grounds that it
is one.

## Dependencies that move the Microsoft model's score

v0.8.4's unstripped Linux binary was flagged too (`Trojan:Script/Wacatac.B!ml`,
sha256 `3ba7e66c…`), with DWARF present. The rc.1 build of the same commit was
clean, and the two differ only in their version string. Session 86 rescanned the
published v0.7.2 binary on that day's model (clean, 0/64) and then swapped
dependency sets between 0.7.2's code and newer code. Every build was Linux
amd64, scanned with `scripts/vt-scan.sh`:

| Dependency set                                                                                | Code                                  | Builds | Flagged by Microsoft |
| --------------------------------------------------------------------------------------------- | ------------------------------------- | ------ | -------------------- |
| `modernc.org/sqlite` 1.60.1, `libc` 1.77.1, `go-humanize` 1.0.1, `oapi-codegen/runtime` 1.7.0 | 0.7.2, 0.8.4 and `main` at `8c0df4d4` | 11     | 0                    |
| `modernc.org/sqlite` 1.61.1, `libc` 1.79.0, `go-humanize` 1.1.0, `oapi-codegen/runtime` 1.8.0 | 0.8.2 to 0.8.5                        | 12     | 6                    |
| As above, with `oapi-codegen/runtime` 1.7.0                                                   | 0.8.4                                 | 1      | 1                    |

On the newer set the verdict flips with the version string alone, and no
script-like string differs between the two sets. The newer `libc` and
`sqlite`, a C-to-Go translation of SQLite, add about 3,000 unique strings of
translated C and symbol data. That shifts the statistical profile the model
scores. Stripping does not decide it. DWARF made unstripped builds better
odds, but it is not a guarantee.

So these four modules are pinned to the clean set (#3104).
`TestScoreSensitiveModulesStayPinned` in `cmd/release-dwarf-check` fails if
`go.mod` moves any of them, and `.github/dependabot.yml` ignores version updates
for them. Advisories still surface as Dependabot alerts. `go-humanize` moves
with `sqlite`, because `go mod tidy` raises it to 1.1.0 when `sqlite` 1.61.x is
required.

**Raising a pinned module.** Build the Linux amd64 binary from the candidate
`go.mod` at four or more version strings, for example `X.Y.Z-rc.1`, `X.Y.Z`,
`X.Y.Z-rc.2` and `X.Y+1.0-rc.1`. Build each in a clone that holds only the tag
`vX.Y.Z…`:
`CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-X main.version=<v> -X 'main.vendor=Edibu, LLC'" ./cmd/nightgauge`.
Scan each one with `scripts/vt-scan.sh`, and raise the pin in the same pull
request only if every build is clean. If a security advisory forces a raise
that does not scan clean, the release waits until a clean set exists.

## darwin/amd64 and the code signature (#3161)

v0.8.6-rc.2's darwin/amd64 binary was flagged (`Trojan:Script/Wacatac.B!ml`,
sha256 `10008b958313…`). The VSIX and the tarball ship that one build (#3064),
so unifying them cannot fix it. Every variant below is darwin/amd64, built
`CGO_ENABLED=0 go build -trimpath` with go1.26.10 and scanned with
`scripts/vt-scan.sh`. "Signed" means ad-hoc `codesign --sign - --options
runtime`, which reproduces the Developer ID verdicts.

**Session 88 (rounds 1 to 3).** Unsigned default builds were flagged at 2 of 4
version strings. Unsigned `-compressdwarf=false` builds were clean at 7 of 7.
Signing put the score back over the threshold. Signed, the default build was
clean at 1 of 3 version strings, `-compressdwarf=false` at 2 of 3, and
`CGO_ENABLED=1 -linkmode=external` at 2 of 3. The full table is on #3161.

**Round 4.** Every candidate uses `-compressdwarf=false` and is signed.

| Candidate                                    | 0.8.6-rc.3             | 0.8.6                | 0.8.6-rc.4           | 0.8.7-rc.1                 | 0.8.7                      |
| -------------------------------------------- | ---------------------- | -------------------- | -------------------- | -------------------------- | -------------------------- |
| `CGO_ENABLED=1 -linkmode=external`           | `cc58ce923761` flagged | not scanned          | not scanned          | not scanned                | not scanned                |
| `codesign --pagesize 16384`                  | `35f857bbf97f` flagged | not scanned          | not scanned          | not scanned                | not scanned                |
| `codesign --pagesize 65536`                  | `f7c1a48ba209` clean   | not scanned          | not scanned          | not scanned                | not scanned                |
| `GOAMD64=v3`                                 | `49a479dddf18` flagged | not scanned          | not scanned          | not scanned                | not scanned                |
| Probe manifest not embedded (diagnostic)     | `34e54fd85b19` clean   | `d9e78389fc61` clean | `55fb76f74ae0` clean | `843069d4ebd1` **flagged** | `008b1c75820a` **flagged** |
| OpenCode plugin JS not embedded (diagnostic) | `ae87835f7ec2` clean   | `e1f3a73bd89e` clean | `3be849d9bd7d` clean | `827c339484ba` clean       | `a947d90717d1` **flagged** |

A candidate flagged at `0.8.6-rc.3` was not scanned further. A `--pagesize`
other than 4096 cannot ship whatever it scans. XNU refuses a CodeDirectory
whose page shift is neither 4K nor the kernel's own page size
(`bsd/kern/ubc_subr.c`), and an Intel Mac's page size is 4K. The two
diagnostic builds replaced the embedded files' content and are not shippable
code.

**Round 5: universal Mach-O.** darwin/amd64 and darwin/arm64 built as above
(`-compressdwarf=false`, the release's version and vendor strings), joined with
`lipo -create`, then signed. Each file is 162 MB and carries `x86_64 arm64`, and
`release-dwarf-check` passes. The round spent 31 VirusTotal requests.

| Version      | sha256         | Verdict                |
| ------------ | -------------- | ---------------------- |
| `0.8.6-rc.3` | `ad838c98ee55` | clean, 0 of 75 engines |
| `0.8.6`      | `bfb622fdf0d2` | clean, 0 of 75 engines |
| `0.8.6-rc.4` | `60e88d257564` | clean, 0 of 75 engines |
| `0.8.7-rc.1` | `748130d4d4fe` | clean, 0 of 75 engines |
| `0.8.7`      | `42b363b59461` | clean, 0 of 75 engines |

**Cause.** The thin darwin/amd64 Mach-O sits at the threshold of Microsoft's
model. The version string or a few kilobytes of embedded data flip the verdict,
and the 4K-page code signature pushes the score up. No build, link or signing
option for the thin file was clean when signed at every version string tested.
The signed universal file was clean at all five, including the two
(`0.8.7-rc.1`, `0.8.7`) that flagged both round 4 diagnostic builds.

**What ships.** `make build-all` builds the darwin binaries with
`-compressdwarf=false`, and `make darwin-universal` joins them into
`bin/nightgauge-darwin-universal`. staging.yml and release.yml sign that one
file and copy it over `bin/nightgauge-darwin-amd64` and
`bin/nightgauge-darwin-arm64`, so both darwin VSIXs, both darwin archives and
the cask carry identical bytes. `scripts/verify-darwin-universal.sh` fails the
run when a darwin VSIX or bin/ binary is not that file. Each darwin download is
about twice the size of a thin one. The universal file stayed clean on go1.27.3
at the same five version strings (#3192).

**Rule.** A darwin binary change ships only if the signed build is clean at five
or more version strings, `0.8.6-rc.3` among them. Shipping a thin darwin binary
again is such a change. Nothing here is allowlisted or reported as a false
positive. A flagged darwin binary holds the release.

## Linux amd64 margin and the margin scan (#3192)

v0.8.6-rc.3's Linux amd64 binary was flagged (`Trojan:Script/Wacatac.B!ml`,
sha256 `fc38f3df…`, reproduced byte for byte from `adc53828`). 86 builds were
measured, each scanned with `scripts/vt-scan.sh` only. Every build is Linux
amd64, `CGO_ENABLED=0 go build -trimpath` with the release's version and
vendor strings, and differs from rc.3's tree by the one named change. Each was
built at several version strings and nothing else.

| Variant                                                      | Builds | Flagged by Microsoft |
| ------------------------------------------------------------ | -----: | -------------------: |
| rc.3's tree as staging built it, go1.26.11                   |      6 |                    3 |
| go1.26.10 (toolchain bump reverted)                          |      5 |                    3 |
| `x/sys` 0.49.0, `x/oauth2` 0.37.0 (dependency bump reverted) |      6 |                    1 |
| rc.2's code (`10a3d0f3`) on go1.26.11                        |      3 |                    2 |
| `-compressdwarf=false`                                       |      6 |                    1 |
| Embedded text assets gzipped (diagnostic)                    |      6 |                    4 |
| go1.27.3                                                     |      5 |                    1 |
| `-buildmode=pie`                                             |      5 |                    3 |
| `GOAMD64=v2`                                                 |      5 |                    3 |
| v0.8.5's tree (`a70689f4`) on go1.26.11                      |      5 |                    1 |
| `8c0df4d4`, before the #3104 pin                             |      5 |                    5 |
| `modernc.org/sqlite` driver import removed (diagnostic)      |      6 |                    2 |
| **go1.27.3 and `-compressdwarf=false`**                      | **18** |      **1** (`0.8.9`) |
| darwin universal, go1.27.3 (the #3161 recipe)                |      5 |                    0 |

The per-build sha256 and verdicts are on #3192.

**Cause.** No change between rc.2 and rc.3 caused it. Each suspected change,
reverted alone, was still flagged. The verdict flips with the version string in
every variant: rc.3's tree was flagged at `0.8.6-rc.3`, `0.8.6` and `0.8.7`,
and clean at `0.8.6-rc.4`, `0.8.6-rc.5` and `0.8.7-rc.1`. The Linux ELF as a
whole sits at this model's threshold, at least since v0.8.5. rc.1 and rc.2 were
each clean at one string, and that was read as a fix. It was not.

**What ships.** `-compressdwarf=false` on every release target
(`RELEASE_LDFLAGS` in the `Makefile`, mirrored in `.goreleaser.yml`) on the
go1.27 toolchain: the lowest rate measured, 1 of 18 on go1.27.3, against 3 of 6
as rc.3 was built. Neither change alone did as well. `go.mod` names go1.27.4,
not go1.27.3: GO-2026-6859, a `net/http` vulnerability this code reaches, is
fixed only in go1.27.4 (main's go1.26.11 already had the fix), so go1.27.3
would fail `govulncheck`. go1.27.4 is unmeasured; the first RC's own scan and
its three margin strings measure it at four version strings, and the release
gate holds the release until they are clean. The residual is not zero, so the
build is paired with the margin scan. Uncompressed DWARF adds about 17 MB to
the Linux binary before compression.

**Rule.** One clean scan is not evidence. A build change, or a raise of a
score-sensitive pin, is accepted only when it is clean at four or more version
strings, the release string among them. A margin finding is fixed by changing
the build, never by picking another version number. It is never reported to
the vendor as a false positive and never allowlisted.

**The margin scan.** For an RC `X.Y.Z-rc.N`, `scripts/release-margin-builds.sh`
builds the Linux amd64 binary and the signed universal darwin file at `X.Y.Z`
(the release string), `X.Y.Z-rc.(N+1)` and `X.Y.(Z+1)-rc.1`. It uses the
Makefile's `build-release-targets` and `darwin-universal` targets, so the flags
are the RC's own, and writes six raw, executable `nightgauge-margin_<v>_<target>`
binaries and a `margin-manifest.txt` (`<version> <file> <sha256>` per line).
They are not archived: the scan gate scans an archive and then the binary
inside it, so an archive cost each build two uploads (#3202). In `staging.yml`:

- `dry-run` builds them on RC tag pushes, DWARF-checks them and uploads
  `rc-margin-<tag>`. They never enter `rc-artifacts-*` or a GitHub Release.
- `rc-scan` fails unless it holds six binaries and the manifest, then scans them
  with the RC's own files in one `release-scan-gate.sh local` call. Any finding
  fails the RC. After a clean scan it uploads `rc-margin-scan-<tag>` (the
  manifest and the report), kept 90 days.

**The release gate.** `release.yml`'s first step after checkout runs
`scripts/release-rc-margin-check.sh`. It passes only when an RC tag
`vX.Y.Z-rc.N` points at the release tag's commit, that tag's staging run has a
successful `rc-scan`, and its unexpired `rc-margin-scan-*` manifest names
`X.Y.Z`. Otherwise nothing is built. It has no override. An expired record
(after 90 days) means a new RC on the commit.

**Tests.** `TestStagingWorkflowBuildsMarginBinaries`,
`TestRCScanScansMarginBinaries` and `TestReleaseWorkflowRequiresMarginScannedRC`
in `cmd/release-dwarf-check` fail if a step is removed, moved, made
non-failing, or stops feeding the scan. `scripts/test-release-margin.sh` (run by
`scripts/ci-local.sh`) covers the version strings, the six-binary count, and
each verdict of the release gate against a stub `gh`.

### The RC scan request budget

The free VirusTotal key allows 4 requests a minute and 500 a day. One RC scan,
margin builds included, is budgeted at 160 requests (`VT_MAX_REQUESTS: "160"`
on `staging.yml`'s rc-scan step), so a second RC and a release fit in the same
day. A scan that would pass the budget stops before sending the request, with a
HARNESS ERROR, and the RC fails.

- **The RC's own files: about 60.** rc.3 sent 108 requests for 31 unique files.
  63 of them re-analysed 21 unchanged, clean hooks and scripts whose last
  analysis was older than 24 hours. A text file now reuses a clean, complete
  report for 14 days (`VT_REUSE_MAX_AGE_TEXT`), so it costs one lookup.
  Binaries, VSIXes and archives keep the 24-hour window
  (`VT_REUSE_MAX_AGE`): a classifier verdict on them moves without a byte
  changing. A report with any finding is never reused under the long window.
  The kind comes from the bytes: an ELF, Mach-O, PE, zip or gzip header, or a
  NUL byte in the first 8 KiB, makes a file a binary. `scripts/test-vt-scan.sh`
  replays rc.3's file list through the offline API double: 108 requests
  recorded, 52 under these rules.
- **The text files' window running out: about 58 more.** Reuse never moves
  VirusTotal's report date, so an RC cut 14 or more days after the text files
  were last analysed re-analyses all of them again. The same replay with every
  text file re-analysed takes 111 requests: rc.3's 21 reanalyses took 35
  polls, not one each. Their new reports then carry the next 14 days.
- **The six margin binaries: about 24.** Each is new to VirusTotal: a lookup,
  `upload_url` (they are over 32 MB), the upload and a poll.
- **Headroom: about 16** for polls of slow analyses. A report counts as
  reanalysed only when its date is later than the lookup's and within the
  file's window: 24 hours for a binary, VSIX, archive or any report with a
  finding, the long window only for a clean text file. A file poll that shows no
  newer date switches to polling the analysis object alone, one request per
  poll, until it completes; then the file is read again. When that analysis
  completes without a newer report (2026-11-11), the scan stops at once with
  exit 2 and the step is re-run; the lookup's report is never judged as the
  reanalysis. A newer analysis may mean the file object lags, so the file is
  read up to three more times; if its date still has not moved, the scan stops
  with exit 2 the same way (VirusTotal dates an analysis when it is created, so
  a folded one can look newer).

Every `vt-scan.sh` run ends with a line counting its requests by class
(lookups, uploads, reanalyses, polls), and the report carries the same counts
as `requests_by_class`; each entry records the reuse window that applied
(`reuse_window`) and the analysis date.

A measurement scan, such as an ad-hoc `vt-scan.sh` or `release-scan-gate.sh
local` run to test a hypothesis, spends the same daily quota as the release
path. Plan it against the day's remaining quota before the first request is
sent: count the requests already spent today (each report's `requests`), add
the planned scan's estimate from the rules above, and keep room for any RC or
release still due that day. Set `VT_MAX_REQUESTS` to that estimate so the scan
stops instead of spending the rest.
