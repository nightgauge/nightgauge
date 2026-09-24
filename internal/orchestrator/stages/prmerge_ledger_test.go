package stages

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
		`{"version":2,"entries":{"default|graphql":{"remaining":1,"limit":5000,"resetAt":%d,"checkedAt":%d}}}`,
		time.Now().Add(30*time.Minute).Unix(), time.Now().Unix())
	if err := os.WriteFile(tracker, []byte(body), 0o644); err != nil {
		t.Fatalf("seed tracker: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("NIGHTGAUGE_GITHUB_RATELIMIT_FLOOR", "100")
	t.Setenv("NIGHTGAUGE_GITHUB_RATELIMIT_NO_WAIT", "1")
}

// The deterministic pr-merge runner is the single heaviest `gh` user in the
// pipeline: it views a PR, merges it, then re-verifies (#1913).
func TestExecGhClientIsGatedAndLedgered(t *testing.T) {
	gateEveryGhSubprocess(t)
	c := &execGhClient{}

	if _, err := c.View(context.Background(), 1); !errors.Is(err, github.ErrRateLimitGated) {
		t.Errorf("View err = %v, want ErrRateLimitGated", err)
	}
	if err := c.Merge(context.Background(), 1); !errors.Is(err, github.ErrRateLimitGated) {
		t.Errorf("Merge err = %v, want ErrRateLimitGated", err)
	}
}
