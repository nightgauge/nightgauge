package orchestrator

import (
	"log"
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
