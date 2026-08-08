# Process-table fixture (#341)

`ps-snapshot.txt` is a **captured, redacted process table from a real machine**,
not a hand-authored example. `doctor`'s orphaned-process carrier parses exactly
this text, and the shape of `ps` output — right-aligned pid column, variable
etime widths, three different etime formats, argv containing spaces — is the
part that is easy to get wrong from memory and impossible to notice being
wrong when both the fixture and the parser are invented together.

## What was captured

| Field       | Value                                             |
| ----------- | ------------------------------------------------- |
| Host OS     | macOS 26.0 (Darwin 27.0.0, arm64)                 |
| Captured at | 2026-08-08T20:04Z                                 |
| Command     | `ps -axo pid=,etime=,command=`                    |
| Rows        | 7 (1 nightgauge, 6 representative non-nightgauge) |

The capturing machine was running the VS Code extension host's `nightgauge
serve` daemon, which is the reason it is in the fixture: `serve` is the one
long-lived nightgauge process a healthy workstation always has, and it is the
process the classifier must **never** report. A fixture without it could not
express the rule.

## By what

`capture-ps-snapshot.sh`, committed beside the fixture so the capture is
repeatable:

```bash
bash internal/doctor/testdata/orphaned-processes/capture-ps-snapshot.sh \
  > internal/doctor/testdata/orphaned-processes/ps-snapshot.txt
```

## What the script scrubs

- The capturing machine's **home directory path** → `/Users/operator`.
- The capturing machine's **login name**, anywhere it appears → `operator`.
- **Non-nightgauge processes' arguments** — those rows are reduced to their
  executable path. A third-party process's argv is where private paths, ticket
  ids, and occasionally secrets live; its _columns_ are all this fixture needs
  from it.
- All but **two non-nightgauge rows per etime format**. What remains covers
  each of the three formats `ps` emits, which is the coverage the parser needs:

  | Format        | Example       | Row              |
  | ------------- | ------------- | ---------------- |
  | `mm:ss`       | `15:29`       | the serve daemon |
  | `hh:mm:ss`    | `20:04:18`    | pid 5308         |
  | `dd-hh:mm:ss` | `02-23:14:35` | pid 1 (launchd)  |

Nightgauge rows keep their **argv intact** — the subcommand token is the
evidence the classifier reads — and every row keeps its original column
spacing byte for byte outside those substitutions.

## Derived lines (read before adding a test)

The capturing machine was **clean**: it had no orphaned nightgauge process, so
the fixture contains none. Cases that need one — an aged
`autonomous run --dry-run`, a sidecar-owned run, a malformed row — are built in
`orphaned_processes_test.go` by **deriving** from captured rows: the serve
daemon's row supplies the real binary path and column spacing, and only the pid,
the etime, and the subcommand tokens are substituted. Nothing in the test suite
invents a `ps` line from scratch, and new cases must not either.

The incident this carrier exists for is on the derived side by necessity: a
`nightgauge autonomous run --dry-run` that had been running for **31 hours**,
holding a slot and reported by nothing. Reproducing it live would mean leaking a
process for a day and a half.
