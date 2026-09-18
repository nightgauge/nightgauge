//go:build canary

package adapters

// The #1639 canary leg's own regression test for openCodeCanaryRelax
// (opencode_preflight.go, opencode_preflight_canary.go): compiled only under
// `-tags canary`, which scripts/adapter-canary.sh's cmd_opencode_canary runs
// as part of the opencode-canary CI job (both
// `./internal/execution` and `./internal/execution/adapters`), so this
// regression runs on every scheduled and pull_request canary invocation, not
// just by hand. It needs no live opencode binary: like the rest of this
// file's package, it pins a fake.

import (
	"context"
	"strings"
	"testing"
)

// TestOpenCodeCanaryRelaxDoesNothingWithoutTheEnvSignal: compiled with the
// "canary" tag alone, a dispatch above max-tested on a model server endpoint
// is still refused unless the explicit NIGHTGAUGE_CANARY=true signal is also
// present — the build tag alone is not the relaxation, so a canary-tagged
// binary invoked without the env var (a plain `go test -tags canary` by
// hand) still exercises the real refusal.
func TestOpenCodeCanaryRelaxDoesNothingWithoutTheEnvSignal(t *testing.T) {
	t.Setenv("NIGHTGAUGE_CANARY", "")
	preflightEnv(t)
	m := openCodeManifestForTest(t)
	above := patchStep(t, m.MaxTested, 1)
	fake := installFakeOpenCode(t, fakeOpenCodeBehavior{version: above, debugConfig: echoContent})
	err := pinnedAdapter(lmStudioSettings(), fake.path).PreDispatch(context.Background(), RunOptions{Model: "lmstudio/qwen/qwen3.8-27b"})
	if err == nil {
		t.Fatal("an endpoint dispatch above max-tested was allowed under the canary build with NIGHTGAUGE_CANARY unset")
	}
	if !strings.Contains(err.Error(), "adapter_incompatible") {
		t.Errorf("the refusal is not adapter_incompatible: %v", err)
	}
	if n := fake.count(t, "debug config"); n != 0 {
		t.Errorf("the self-test ran for a dispatch that is refused anyway")
	}
}

// TestOpenCodeCanaryRelaxLetsAnAboveMaxTestedEndpointDispatchRunItsSelfTest is
// the positive case: with NIGHTGAUGE_CANARY=true, the same dispatch that
// TestOpenCodeAboveMaxTestedRefusesAnEndpoint (opencode_preflight_test.go)
// and this file's own DoesNothingWithoutTheEnvSignal case both refuse instead
// runs the installed binary's self-test and, once it passes, the dispatch
// itself — the #1639 canary leg's whole point: judge the newest release on
// its own captured flags and config compatibility, not auto-fail it for
// being newer.
func TestOpenCodeCanaryRelaxLetsAnAboveMaxTestedEndpointDispatchRunItsSelfTest(t *testing.T) {
	t.Setenv("NIGHTGAUGE_CANARY", "true")
	preflightEnv(t)
	m := openCodeManifestForTest(t)
	above := patchStep(t, m.MaxTested, 1)
	fake := installFakeOpenCode(t, fakeOpenCodeBehavior{
		version:     above,
		debugConfig: echoContent,
		runHelp:     helpScript(t, capturedRunHelp(t)),
	})
	run := RunOptions{
		Stage:       "feature-dev",
		Model:       "lmstudio/qwen/qwen3.8-27b",
		WorktreeDir: gitInitTestWorktree(t),
	}
	var err error
	stderr := captureAdapterStderr(t, func() {
		err = pinnedAdapter(lmStudioSettings(), fake.path).PreDispatch(context.Background(), run)
	})
	if err != nil {
		t.Fatalf("PreDispatch = %v, want the canary relaxation to let the self-test decide:\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "canary relaxation") {
		t.Errorf("PreDispatch did not warn about the canary relaxation:\n%s", stderr)
	}
	if n := fake.count(t, "debug config"); n != 1 {
		t.Errorf("the self-test's `debug config` ran %d time(s), want 1: the relaxation must not skip it", n)
	}
	if n := fake.count(t, "run --help"); n != 1 {
		t.Errorf("the self-test's `run --help` ran %d time(s), want 1: the relaxation must not skip it", n)
	}
}
