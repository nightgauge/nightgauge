# OpenCode release feed evidence

`releases.txt` and `tags.txt` are **captured, real** lists of the release and
tag names of the public `anomalyco/opencode` repository, not hand-written
examples. The opencode manifest's github feed carries a `tag_pattern`, and
`TestOpenCodeTagPatternAgainstCapturedTags` in `../../manifest_test.go` checks it
against both lists: every release named like a version must match, and no
other tag may.

`capture.sh` regenerates both files. Re-capture when the pattern changes or a
new non-release tag family appears upstream, and update the table below in the
same change.

## What was captured

| Field       | Value                                                            |
| ----------- | ---------------------------------------------------------------- |
| Captured at | 2026-09-13                                                       |
| Commands    | `gh api repos/anomalyco/opencode/releases --paginate` (names)    |
|             | `gh api repos/anomalyco/opencode/tags --paginate` (names)        |
| Order       | sorted bytewise (`LC_ALL=C sort`)                                |
| Releases    | 872 names; 866 are `v<major>.<minor>.<patch>`, newest `v1.18.30` |
| Tags        | 1106 names; 1013 are `v<major>.<minor>.<patch>`                  |
| Redaction   | none needed: public names only; `capture.sh` refuses odd lines   |

Every release name is also a tag name.

## Why the pattern is anchored

The repository tags far more than releases, and several non-release families
start with `v`, so neither a `v*` glob nor an unanchored `v[0-9]` prefix
selects releases:

| Family                                   | Count | Starts with `v` |
| ---------------------------------------- | ----: | --------------- |
| `vscode-v0.0.N` (editor extension)       |    13 | yes             |
| `v0.0.0-<timestamp>`, `v0.0.0-ci-<N>`    |    23 | yes             |
| `v0.1.0-beta<N>`                         |     3 | yes             |
| `v0.0.1-feature-bench`, `v0.0.2-…`       |     2 | yes             |
| `github-v1…` (GitHub Action)             |    39 | no              |
| `pr-<N>-screenshots` and similar assets  |     9 | no              |
| `latest`                                 |     1 | no              |
| `0.0.45`, `0.0.46`, `0.0.47` (no prefix) |     3 | no              |

Four of the `pr-…` tags and two of the unprefixed `0.0.4x` tags are also
published as releases, so filtering releases rather than tags does not remove
the need for the pattern. The manifest uses `^v[0-9]+\.[0-9]+\.[0-9]+$`. The
unprefixed `0.0.4x` releases predate the `v` scheme and are left out on purpose:
every OpenCode version Nightgauge supports is a `v` release.
