# Release Binaries Ship Unstripped

**Date:** 2026-11-09
**Status:** Decided
**Issue:** #3091, #3102

---

## Decision

Every Go binary Nightgauge ships is built **without** the linker strip flags
`-s` and `-w`. That covers the darwin and linux binaries in the release
archives, the binaries bundled in each VSIX, and the Homebrew cask's. Each
binary keeps its symbol table and its DWARF debug information.

A scan finding is fixed at its cause. Owner rule: a finding is never reported
to the vendor as a false positive (no WDSI or "incorrectly detected"
submission), never allowlisted, and never called a false positive as a reason
to publish. A false-positive submission once got the Marketplace publishing
account banned. The `Windows_API_Function` entry in `scripts/vt-scan.sh`'s
YARA allowlist (#3019) predates this rule and is tracked for reconciliation.

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
does not keep a binary clean. The cause of this finding is tracked separately.

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

- `Makefile` `LDFLAGS` and `.goreleaser.yml` `ldflags` carry no `-s` or `-w`.
  Each has a comment that cites this record. `TestNoLinkerStripFlags` in
  `cmd/release-dwarf-check` fails, naming this record, if either flag returns.
- `cmd/release-dwarf-check` reads the packaged files, not the flags. It opens
  every Go binary in each `.tar.gz` and `.vsix` (or a raw binary) with Go's
  `debug/elf` or `debug/macho` and requires `DWARF()` to yield data. It exits
  `0` when every binary carries DWARF and `1` when any lacks it. It exits `2`
  with a `HARNESS ERROR` line when it checked nothing: no path, an unreadable
  path, or an archive with no Go binary.
- `staging.yml` runs it in the `dry-run` job, which the RC's `rc-scan` job
  needs. `release.yml` runs it before the draft is scanned. Both run it right
  after `scripts/verify-archive-binaries.sh`, and both fail on exit `1` or `2`.
  Tests in the same package prove the step's position and that it fails
  closed.
- The detector's regression tests build a small program for linux/amd64 and
  darwin/arm64 with and without `-s -w`, and check each raw, in a `.tar.gz` and
  in a `.vsix`. `scripts/ci-local.sh` runs them through `go test ./...`.

## A Microsoft `!ml` finding on a Go binary

Check it against this record first. Are the binaries still unstripped, and did
`release-dwarf-check` pass in the run that built them? If not, restore DWARF.
If they are unstripped and still flagged, as v0.8.4 was, the cause is
something else. Find it and fix it there. Never report it to the vendor as a
false positive, never allowlist it, and never publish on the grounds that it
is one.
