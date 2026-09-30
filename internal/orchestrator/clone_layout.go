package orchestrator

import (
	"log"
	"path"
	"path/filepath"
	"sync"

	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/state"
)

// pipelineStatePath is the path of name inside root's pipeline state
// directory, resolved through layout.PipelineStateDir (ADR-024, #2033).
//
// It is for READ-side sites (os.ReadFile, os.Stat, os.Remove) whose failure
// mode is already "the file is absent". For an empty or relative root it
// returns "" — every os call on "" fails with a not-exist error — rather than a
// path relative to the process's working directory. A site that WRITES calls
// layout.PipelineStateDir itself and handles the error.
//
// The resolver error is logged once per root, so an unresolvable root is
// distinguishable from an absent file in the log without repeating on every
// read.
func pipelineStatePath(root, name string) string {
	dir, err := layout.PipelineStateDir(root)
	if err != nil {
		if _, seen := unresolvedPipelineRoots.LoadOrStore(root, struct{}{}); !seen {
			log.Printf("WARN orchestrator: pipeline state directory not resolved, reading as absent: %v", err)
		}
		return ""
	}
	return filepath.Join(dir, name)
}

// unresolvedPipelineRoots records the roots pipelineStatePath has already
// logged a resolver error for.
var unresolvedPipelineRoots sync.Map

// persistPipelineState writes runtime's snapshot into root's pipeline state
// directory. An empty or relative root is an error (layout.PipelineStateDir),
// reported through the caller's existing persist-failure log line instead of
// writing under the process's working directory.
func persistPipelineState(runtime *state.RuntimeState, root string) error {
	dir, err := layout.PipelineStateDir(root)
	if err != nil {
		return err
	}
	return runtime.Persist(dir)
}

// checkoutStatePath is the path of name inside root's CHECKOUT, the
// per-checkout directory that holds the unkeyed run-control singletons
// (current-run.json, queue-state.json, run-state.json, ...), resolved through
// layout.CheckoutPath (ADR-024 § 7).
//
// Like pipelineStatePath it is for READ-side sites: an unresolvable root (empty,
// relative, or not inside a git checkout) yields "" and is logged once, so the
// read fails as "absent" instead of resolving against the process's working
// directory. A site that WRITES uses layout.WriteCheckoutFile and handles the
// error.
func checkoutStatePath(root, name string) string {
	p, err := layout.CheckoutPath(root, name)
	if err != nil {
		if _, seen := unresolvedCheckoutRoots.LoadOrStore(root, struct{}{}); !seen {
			log.Printf("WARN orchestrator: per-checkout directory not resolved, reading as absent: %v", err)
		}
		return ""
	}
	return p
}

// unresolvedCheckoutRoots records the roots checkoutStatePath has already
// logged a resolver error for.
var unresolvedCheckoutRoots sync.Map

// AutonomousStateName is the autonomous scheduler's state file inside
// CHECKOUT (ADR-024 § 7): one autonomous scheduler per checkout. Pass it to
// layout.WriteCheckoutFile; read through AutonomousStatePath.
var AutonomousStateName = path.Join(layout.CheckoutAutonomous, "state.json")

// AutonomousStatePath is the autonomous scheduler's state file for the
// checkout root is in: CHECKOUT/autonomous/state.json. It errors for a root
// that is empty, relative or outside a git checkout; it never falls back to
// the working tree.
func AutonomousStatePath(root string) (string, error) {
	return layout.CheckoutPath(root, AutonomousStateName)
}
