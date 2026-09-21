# Artifact Verification

How to independently verify that a published Nightgauge artifact is the one
this repository built, from the source it claims, without trusting anything we
say about it. Every command below runs against public data and public tooling.

This document exists because a scanner flagged one of our releases and the only
useful answer to that is evidence a reviewer can reproduce themselves.

## What ships

Each release publishes three per-target `.vsix` files (`darwin-arm64`,
`darwin-x64`, `linux-x64`) and three CLI archives, plus `checksums.txt`,
`manifest.json` and an SPDX SBOM per archive. Each `.vsix` bundles one
statically linked Go binary at `extension/dist/bin/nightgauge`, built from
`./cmd/nightgauge` in this repository. There is no runtime download: what the
`.vsix` contains is what runs.

## 1. Build provenance — the strongest check

Every `.vsix` and archive carries a signed GitHub build-provenance attestation.
This binds the artifact's digest to the workflow, the commit and the runner that
produced it, so a modified artifact cannot present a valid attestation.

```bash
gh attestation verify nightgauge-vscode-darwin-arm64-<version>.vsix \
  --repo nightgauge/nightgauge
```

Exit `0` means verified. Add `--format json` to read the detail — it names the
building workflow, the source commit and `runnerEnvironment: github-hosted`:

```text
workflow:  https://github.com/nightgauge/nightgauge/.github/workflows/release.yml@refs/tags/<tag>
repo:      https://github.com/nightgauge/nightgauge
sha:       <commit>
issuer:    https://token.actions.githubusercontent.com
runnerEnv: github-hosted
```

Releases are built on GitHub-hosted runners, never on self-hosted hardware, so
the attestation's trust root is GitHub's OIDC issuer rather than a machine we
control.

## 2. Apple code signature and notarization (macOS targets)

The bundled macOS binary is Developer ID signed with the hardened runtime and a
secure timestamp, and is notarized by Apple — meaning Apple's own malware
scanning has cleared that exact binary.

```bash
unzip -p <vsix> extension/dist/bin/nightgauge > /tmp/ng
codesign -dv --verbose=4 /tmp/ng     # Authority, TeamIdentifier, flags
codesign --verify --strict /tmp/ng   # signature integrity
spctl -a -vvv -t install /tmp/ng     # Gatekeeper + notarization (needs network)
```

Expected:

```text
Authority=Developer ID Application: Edibu LLC (RZJPN7Y7BG)
TeamIdentifier=RZJPN7Y7BG
CodeDirectory ... flags=0x10000(runtime)
source=Notarized Developer ID
```

A bare Mach-O cannot have a notarization ticket stapled to it — `stapler` works
on `.app`, `.dmg` and `.pkg` only — so `stapler validate` reports no ticket by
design. `spctl` performs the online check, and that is the authoritative one.

## 3. Checksums

`checksums.txt` on each GitHub Release lists the SHA-256 of every asset.

```bash
shasum -a 256 -c checksums.txt --ignore-missing
```

## 4. Rebuild and compare

Go builds are `-trimpath`'d in both the Makefile and `.goreleaser.yml`, so a
build of a given commit does not embed the builder's filesystem paths and can
be reproduced by anyone with the matching Go toolchain:

```bash
git checkout <tag>
make build-all VERSION=<version>
```

## 5. Inspect the dependency tree

The binary is stripped (`-s -w`) but retains Go build info, so the full module
graph is enumerable without our cooperation:

```bash
go version -m extension/dist/bin/nightgauge
```

This prints the main module, its version, and every dependency with its hash.
An SPDX SBOM is also attached to each release.

## What we scan before publishing

- **ClamAV** over every `.vsix` and every built binary, in both `release.yml`
  and `marketplace-publish.yml`, before attestation and before publish. The
  JSON report is retained as a run artifact. See `scripts/malware-scan.sh`.
- **`govulncheck`** over the Go module graph, gating on reachable
  vulnerabilities.
- **`npm audit`** over the JavaScript tree bundled into the extension.
- **CodeQL** static analysis, and a full-history credential scan.

The malware scan deliberately does not treat `clamscan`'s exit code as the
verdict. `clamscan` exits `0` both for a clean artifact and for one it never
read — with `--max-filesize=1M` the real `.vsix` reports `Data scanned: 0 B`
and still exits `0`. The gate additionally asserts a floor on bytes actually
scanned, so a misconfigured scan fails closed instead of reporting success.

## On antivirus false positives

A VS Code extension that bundles a ~28 MB statically linked Go binary is a
recurring false-positive shape for heuristic and ML-based engines, which score
"large stripped executable inside an archive" without a corresponding family
signature. Detections of this kind are generic by name (`Malware-gen`,
`TR/Malware`, `Trojan.Malware`, or a bare ML score) and appear on a small
minority of engines while every major vendor reports the file clean.

If you are assessing such a flag, the checks above are the ones that carry
information: a valid provenance attestation and an Apple notarization together
establish that the artifact is exactly what this repository built from the
named commit, and that Apple's malware scanning cleared it. A generic heuristic
name from a minority of engines, with no prevalence and no family attribution,
does not.

To report a suspected false positive or any security concern, see
[SECURITY.md](SECURITY.md).
