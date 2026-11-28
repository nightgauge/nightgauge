package ipc

import (
	"context"
	"log"
	"strings"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/internal/state"
)

// needsDecompositionDetail is the failure text the label-and-comment action
// parses: the failing stage's last gate result, reason then evidence joined
// the way the scheduler's gateFailureError joins them, so the key=value counts
// and sizes survive however the extension worded errMsg. With no gate result
// it falls back to errMsg.
func needsDecompositionDetail(results []state.StageGateResult, errMsg string) string {
	if len(results) == 0 {
		return errMsg
	}
	last := results[len(results)-1]
	if len(last.Evidence) == 0 {
		if last.Reason != "" {
			return last.Reason
		}
		return errMsg
	}
	return strings.TrimPrefix(last.Reason+"; "+strings.Join(last.Evidence, "; "), "; ")
}

// labelForDecomposition labels the issue needs-decomposition and posts the one
// explanatory comment for an extension run booked needs_decomposition (#3684).
// It runs in the background so pipeline.notifyComplete is not held on the
// forge; it is best-effort, bounded and logged, and never changes the record.
func (s *Server) labelForDecomposition(repo string, issue int, detail string) {
	if repo == "" || issue <= 0 {
		return
	}
	apply := s.applyNeedsDecompositionOverride
	if apply == nil {
		apply = func(ctx context.Context, repo string, issue int, detail string) error {
			resolve := func(ctx context.Context, owner, name string) (*gh.Client, error) {
				return s.resolveClientForRequest(ctx, "", owner, name)
			}
			return orchestrator.ApplyNeedsDecompositionForge(ctx, resolve, repo, issue, detail)
		}
	}
	s.needsDecompositionWG.Add(1)
	go func() {
		defer s.needsDecompositionWG.Done()
		ctx, cancel := context.WithTimeout(context.Background(), orchestrator.NeedsDecompositionTimeout)
		defer cancel()
		if err := apply(ctx, repo, issue, detail); err != nil {
			log.Printf("ipc: %v", err)
		}
	}()
}
