#!/usr/bin/env bash
# scrub-evidence.sh — remove this process's own secret VALUES from an evidence
# directory before it is uploaded as a workflow artifact (#1335).
#
# WHY THIS EXISTS. The clean-install harness is handed real credentials
# (GH_TOKEN, ANTHROPIC_API_KEY, CLEAN_INSTALL_GH_TOKEN) and collects logs into
# /out, which the workflow uploads. GitHub Actions masks registered secrets in
# the WORKFLOW LOG, but an uploaded artifact gets no such treatment. In one run
# a `feature-dev` agent echoed GH_TOKEN, the value reached a VS Code output
# channel, the channel was copied into /out, and a live fine-grained token was
# downloadable from a public repo's artifact for about ten minutes.
#
# The product-side fix (redaction at the output-channel sink) is the first
# layer. This is the second, and it is deliberately DIFFERENT IN KIND: the
# sanitizer matches secret SHAPES, which can only ever cover the shapes someone
# thought of, while this matches the exact VALUES this process was given. A
# credential in a format nobody has a pattern for is still caught here.
#
# Usage: scrub-evidence.sh <directory>
set -euo pipefail

DIR="${1:?usage: scrub-evidence.sh <directory>}"
[[ -d "$DIR" ]] || { echo "scrub-evidence: $DIR is not a directory" >&2; exit 1; }

# Values shorter than this are not scrubbed. A short or empty value would match
# everywhere and shred the evidence — and an empty needle would corrupt every
# file it touched, which is a worse outcome than the leak this prevents.
MIN_SECRET_LEN=8

python3 - "$DIR" "$MIN_SECRET_LEN" <<'PY'
import os, sys, pathlib

root = pathlib.Path(sys.argv[1])
min_len = int(sys.argv[2])

# Named explicitly because they are what this harness is handed, plus anything
# whose NAME looks like a credential — "every secret value present in its own
# environment", rather than a list someone has to remember to extend.
EXPLICIT = ("GH_TOKEN", "GITHUB_TOKEN", "ANTHROPIC_API_KEY", "CLEAN_INSTALL_GH_TOKEN")
NAME_HINTS = ("TOKEN", "SECRET", "PASSWORD", "PASSWD", "API_KEY", "_KEY", "CREDENTIAL")

secrets = set()
for name, value in os.environ.items():
    if not value or len(value) < min_len:
        continue
    if name in EXPLICIT or any(h in name.upper() for h in NAME_HINTS):
        secrets.add(value)

if not secrets:
    print("scrub-evidence: no secret-shaped environment values to scrub")
    sys.exit(0)

# Longest first: if one secret contains another, replacing the shorter one
# first would leave a fragment of the longer one behind.
needles = sorted(secrets, key=len, reverse=True)

scanned = cleaned = 0
for path in root.rglob("*"):
    if not path.is_file() or path.is_symlink():
        continue
    try:
        raw = path.read_bytes()
    except OSError:
        continue
    scanned += 1
    out = raw
    for needle in needles:
        nb = needle.encode("utf-8", "surrogateescape")
        if nb in out:
            out = out.replace(nb, b"[REDACTED:ENV_SECRET]")
    if out is not raw and out != raw:
        try:
            path.write_bytes(out)
            cleaned += 1
        except OSError as err:
            # Loud: a file we could not clean is a file that ships the secret.
            print(f"scrub-evidence: FAILED to rewrite {path}: {err}", file=sys.stderr)
            sys.exit(1)

# Never print the secrets or their lengths — only the counts.
print(f"scrub-evidence: scanned {scanned} file(s), rewrote {cleaned} containing "
      f"{len(needles)} distinct environment secret(s)")
PY
