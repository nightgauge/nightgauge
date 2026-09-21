package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/github"
)

// gateEveryGhSubprocess makes the machine-wide rate-limit headroom gate trip
// for this test: a temporary $HOME whose tracker file reports an exhausted
// budget inside an un-elapsed window, with waiting disabled so the gate fails
// fast instead of sleeping out a real reset.
//
// It is how a test proves the DEFAULT (unstubbed) implementation goes through
// github.RunGhSubprocess: ErrRateLimitGated can only come from that path, and
// no `gh` runs, so the assertion needs neither the binary nor the network.
func gateEveryGhSubprocess(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".nightgauge"), 0o755); err != nil {
		t.Fatalf("seed home: %v", err)
	}
	tracker := filepath.Join(home, ".nightgauge", "rate-limit.json")
	body := fmt.Sprintf(
		`{"version":1,"entries":{"default":{"remaining":1,"limit":5000,"resetAt":%d,"checkedAt":%d}}}`,
		time.Now().Add(30*time.Minute).Unix(), time.Now().Unix())
	if err := os.WriteFile(tracker, []byte(body), 0o644); err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("NIGHTGAUGE_GITHUB_RATELIMIT_FLOOR", "100")
	t.Setenv("NIGHTGAUGE_GITHUB_RATELIMIT_NO_WAIT", "1")
}

// reconcileExecGhDefault captures the production implementation at package
// initialisation, before this binary's TestMain swaps the seam for the
// hermeticity guard (#492). Package variables are initialised before TestMain
// runs, so this is the only way to assert on the default here.
var reconcileExecGhDefault = reconcileExecGh

// Non-terminal reconcile runs on every ambiguous stage failure, in a loop
// (#1913).
func TestReconcileExecGhDefaultIsGatedAndLedgered(t *testing.T) {
	gateEveryGhSubprocess(t)

	_, err := reconcileExecGhDefault(context.Background(), "pr", "list", "--json", "number")
	if !errors.Is(err, github.ErrRateLimitGated) {
		t.Fatalf("err = %v, want ErrRateLimitGated — the default reconcileExecGh must route through github.RunGhSubprocess", err)
	}
}

func TestReconcileExecGhRemainsStubbable(t *testing.T) {
	orig := reconcileExecGh
	t.Cleanup(func() { reconcileExecGh = orig })
	reconcileExecGh = func(context.Context, ...string) ([]byte, error) { return []byte("stubbed"), nil }

	out, err := reconcileExecGh(context.Background(), "pr", "list")
	if err != nil || string(out) != "stubbed" {
		t.Fatalf("stub not honoured: out=%q err=%v", out, err)
	}
}
