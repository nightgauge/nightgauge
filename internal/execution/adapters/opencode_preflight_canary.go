//go:build canary

package adapters

import "os"

// This file compiles into nothing but a test binary built with
// `-tags canary` — scripts/adapter-canary.sh's own `go test -tags canary`
// invocation (#1639) — never a production build (cmd/nightgauge, the VS Code
// extension bundle, or scripts/clean-install-e2e.sh's release build). Setting
// openCodeCanaryRelax here, rather than in opencode_preflight.go itself, is
// what makes the relaxation unreachable from a production spawn: the var
// stays nil unless this file was compiled in.
//
// Even compiled in, the closure below still requires the explicit
// NIGHTGAUGE_CANARY=true signal at call time (not just at init, so a test can
// toggle it with t.Setenv): scripts/adapter-canary.sh's cmd_opencode_canary
// sets it for exactly the `go test -tags canary` it runs, the same env var
// name internal/execution/opencode_isolation_integration_test.go's
// nightgaugeCanaryEnv relaxes realOpenCode's own version pin under.
func init() {
	openCodeCanaryRelax = func(model string) bool {
		return os.Getenv("NIGHTGAUGE_CANARY") == "true"
	}
}
