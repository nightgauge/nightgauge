package hooks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/gateledger"
)

// goTestPassRecord is written by scripts/ci-local-go-test.sh after `go test`
// passes, relative to the worktree. It holds the tree that passed.
var goTestPassRecord = filepath.Join(".ci-local-logs", "go-test-pass.tree")

// goTestTimerHeadroom is how far ahead of the gate's deadline Go's per-package
// timer aims, and the shortest timer it is given. A shorter budget lets the
// gate's deadline fire instead of failing packages that are merely starting.
const goTestTimerHeadroom = 60 * time.Second

// goTestTimeoutArg sets Go's per-package timer from what remains of the gate's
// deadline, so a hung package panics with its goroutine dump naming the test
// before the gate kills it with truncated output. 0 is Go's own "no limit".
func goTestTimeoutArg(ctx context.Context, timeoutSeconds int) string {
	if timeoutSeconds <= 0 {
		return "0"
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return strconv.Itoa(timeoutSeconds) + "s"
	}
	timer := max(time.Until(deadline)-goTestTimerHeadroom, goTestTimerHeadroom)
	return strconv.Itoa(int(timer.Seconds())) + "s"
}

// decideGoTestSkip decides whether the test phase can be skipped on the merged
// tree. The only proof is ci-local's pass record for this exact tree: a change
// outside Go files can still fail a Go test, because many read skills/, docs/
// and scripts/. It fails closed: anything it cannot prove runs the suite. The
// returned reason says why either way.
func decideGoTestSkip(ctx context.Context, runner CmdRunner, workDir string) (bool, string) {
	recorded, err := os.ReadFile(filepath.Join(workDir, goTestPassRecord))
	if err != nil {
		return false, "ci-local recorded no pass"
	}
	want := strings.TrimSpace(string(recorded))
	got, err := TreeFingerprint(ctx, runner, workDir)
	if err != nil {
		return false, fmt.Sprintf("the tree could not be fingerprinted: %v", err)
	}
	if want == "" || want != got {
		return false, "ci-local's pass record is for another tree"
	}
	return true, fmt.Sprintf("ci-local already passed go test on tree %s", got)
}

// TreeFingerprint names the working tree the way the gate ledger keys it:
// gateledger.Fingerprint, run through runner.
func TreeFingerprint(ctx context.Context, runner CmdRunner, workDir string) (string, error) {
	return gateledger.Fingerprint(ctx, runner.Run, workDir)
}
