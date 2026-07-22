# Public Release Operations

The authoritative public-release runbook is maintained privately by the owner.
This repository intentionally contains only safe, reusable release tooling.

The release strategy uses a certified one-commit export in a fresh repository.
Never replace this repository's history in place, force-push an orphan branch,
or change repository visibility based solely on instructions in the public tree.

Before any namespace or visibility change, the owner must verify the certified
tree, GitHub-hosted CI, runner isolation, repository IDs, and the private
execution record.
