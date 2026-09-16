# `go test` output captures for `opencode_canary_failing_line` (#1639 round 4)

Both files here are **real, captured `go test` output**, not hand-written.
`opencode_canary_failing_line` (`scripts/adapter-canary.sh`) parses whichever
shape `cmd_opencode_canary`'s invocation produces, and
`scripts/test-adapter-canary.sh` replays both to prove the parser handles
each ordering deterministically.

## What was captured

A throwaway test file (`internal/execution/zzfixture_canary_test.go`, build
tag `canaryfixture`, never committed) defined two tests:

```go
func TestOpenCodeCanaryFixturePass(t *testing.T) {
	t.Log("opencode 1.19.0 is installed (canary: pin relaxed from the 1.18.30 baseline observed for ADR-022)")
}

func TestOpenCodeCanaryFixtureFailure(t *testing.T) {
	t.Log("opencode 1.19.0 is installed (canary: pin relaxed from the 1.18.30 baseline observed for ADR-022)")
	t.Fatalf("checkOpenCodeCanaryStream: 1 problem(s) on the stream:\nunexpected event type\nline: %s", `{"type":"not-a-type"}`)
}
```

This mirrors the two real shapes `opencode_canary_test.go` produces: the
`t.Logf` "pin relaxed" notice (`internal/execution/opencode_isolation_integration_test.go:99`)
that fires on nearly every canary run once the installed version differs from
the pinned baseline, and a multi-line `t.Fatalf` failure message (the same
shape `openCodeCanaryProblem.String()` and `RunStage: %v\n%s` produce). In
both files the notice line precedes the real failure line — the case that
broke the round-3 parser, which only ever walked backward from `--- FAIL:`.

Captured with:

```bash
go test -tags canaryfixture ./internal/execution -run 'TestOpenCodeCanaryFixture' -count=1
go test -tags canaryfixture -v ./internal/execution -run 'TestOpenCodeCanaryFixture' -count=1
```

on `go version go1.26.6 darwin/arm64`, then the throwaway file was deleted.
Nothing else in the tree changed for the capture: no network, no CLI, no
stub. The redirected `run -count=1` output (no `-v`) is what
`cmd_opencode_canary` actually invokes; the `-v` capture is kept alongside it
because the parser must not regress if the invocation ever adds `-v`.

## Files

| File                              | Shape                                               | `go test` flags used to produce it |
| --------------------------------- | --------------------------------------------------- | ---------------------------------- |
| `opencode-canary-fail-no-v.txt`   | `--- FAIL:` header first, log lines after (no `-v`) | (none) `-count=1`                  |
| `opencode-canary-fail-with-v.txt` | log lines before `--- FAIL:` (with `-v`)            | `-v -count=1`                      |

## What reads them

`scripts/test-adapter-canary.sh`'s `opencode_canary_failing_line` section
feeds both files to the function directly and asserts the detail returned is
`checkOpenCodeCanaryStream: 1 problem(s) on the stream:` (the failing
message's first line) in both cases, never the pin-relaxed notice line.
