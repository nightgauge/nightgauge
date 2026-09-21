package gates

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

// The gates package shells out to `gh` for every post-condition check. Those
// calls spend from the same GraphQL budget as everything else and, before
// #1913, neither waited on the headroom gate nor left a ledger record.
func TestExecGhDefaultIsGatedAndLedgered(t *testing.T) {
	gateEveryGhSubprocess(t)

	_, err := execGh(context.Background(), "pr", "view", "1")
	if !errors.Is(err, github.ErrRateLimitGated) {
		t.Fatalf("err = %v, want ErrRateLimitGated — the default execGh must route through github.RunGhSubprocess", err)
	}
}

// The test seam stays a seam: every existing test assigns execGh directly.
func TestExecGhRemainsStubbable(t *testing.T) {
	orig := execGh
	t.Cleanup(func() { execGh = orig })
	execGh = func(context.Context, ...string) ([]byte, error) { return []byte("stubbed"), nil }

	out, err := execGh(context.Background(), "pr", "view", "1")
	if err != nil || string(out) != "stubbed" {
		t.Fatalf("stub not honoured: out=%q err=%v", out, err)
	}
}
