#!/usr/bin/env bash
#
# check-license-headers.sh — guard against proprietary license headers slipping
# back into the open-source (Apache-2.0) tree.
#
# This repository is licensed under Apache-2.0 (see LICENSE / NOTICE). The old
# "All Rights Reserved" proprietary headers were removed during the open-source
# relicense. This check fails CI if any ship-public file reasserts one.
#
# The internal `docs/strategy/open-source/` tree (launch planning, excluded
# from the public release) is not checked; docs/strategy/codex/ ships and is. The match is case-sensitive so factual prose such as
# "the platform ships under all rights reserved" does not trip the guard.
set -euo pipefail

# Match "All Rights" (not the full phrase) so a line-wrapped
# "... All Rights\nReserved" footer is still caught. Case-sensitive so factual
# lowercase prose ("... all rights reserved") does not trip the guard.
matches="$(git grep -n -I "All Rights" -- \
  ':!docs/strategy/open-source/' \
  ':!scripts/check-license-headers.sh' \
  ':!THIRD_PARTY_NOTICES' \
  ':!packages/*/THIRD_PARTY_NOTICES' || true)"

if [ -n "$matches" ]; then
  echo "❌ Proprietary 'All Rights Reserved' header found in ship-public files:"
  echo
  echo "$matches"
  echo
  echo "This repository is Apache-2.0. Use an SPDX-style header instead"
  echo "(e.g. 'license: Apache-2.0' in SKILL.md frontmatter). See LICENSE / NOTICE."
  exit 1
fi

# Build output (dist/) is gitignored, so the git grep above cannot see it. Scan
# for a proprietary first-party attribution specifically. Bundled artifacts
# intentionally embed THIRD_PARTY_NOTICES, whose Python/CNRI and other licenses
# contain attributed "All Rights Reserved" clauses that redistribution requires
# us to preserve.
dist_dirs=()
for d in dist packages/*/dist; do
  [ -d "$d" ] && dist_dirs+=("$d")
done
if [ "${#dist_dirs[@]}" -gt 0 ]; then
  dist_matches="$(grep -rniE -I \
    'Copyright.{0,120}(Edibu|Nightgauge).{0,120}All Rights|All Rights.{0,120}Copyright.{0,120}(Edibu|Nightgauge)' \
    "${dist_dirs[@]}" --exclude-dir=node_modules || true)"
  if [ -n "$dist_matches" ]; then
    echo "❌ Proprietary 'All Rights Reserved' header found in build output (dist/):"
    echo
    echo "$dist_matches"
    echo
    echo "A first-party file in dist/ asserts a proprietary header; it would ship"
    echo "in the released artifact. Fix the source before releasing. See LICENSE / NOTICE."
    exit 1
  fi
fi

echo "✓ No proprietary license headers in ship-public files or build output."
