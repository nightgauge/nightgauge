package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

// TestRetryInPlaceOnTimeout: only a feature-validate stage timeout that
// follows a finished feature-dev (dev-{N}.json written) retries in place;
// a real stall, a feature-dev timeout, or a missing dev handoff still
// takes the rewind path.
func TestRetryInPlaceOnTimeout(t *testing.T) {
	ws := t.TempDir()
	const timeout = "exit -1: [stage-timeout] stage stopped at its stage timeout of 2h15m0s after 2h15m0s elapsed"
	const stall = "exit -1: [stall-killed] no output for 20m"

	if RetryInPlaceOnTimeout(state.StageFeatureValidate, timeout, ws, 4) {
		t.Fatal("no dev handoff yet: must not retry in place")
	}
	devPath := pipelineStatePath(ws, "dev-4.json")
	if err := os.MkdirAll(filepath.Dir(devPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(devPath, []byte(`{"issue_number":4}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !RetryInPlaceOnTimeout(state.StageFeatureValidate, timeout, ws, 4) {
		t.Error("validate timeout after dev finished: want retry in place")
	}
	if RetryInPlaceOnTimeout(state.StageFeatureValidate, stall, ws, 4) {
		t.Error("a genuine stall must still rewind")
	}
	if RetryInPlaceOnTimeout(state.StageFeatureDev, timeout, ws, 4) {
		t.Error("a feature-dev timeout must still rewind")
	}
}
