package orchestrator

import (
	"log"
	"strings"

	"github.com/nightgauge/nightgauge/internal/depgraph"
)

// recordClosedStopLocked notes that a run for key ("repo#N") ended
// issue_closed (#3081). The scheduler then leaves the issue out of its
// candidate set: a stop on an already-closed issue is a skip, and a board row
// still showing Ready, or a graph that has not caught up with the close, must
// not dispatch it again every cycle. Caller holds as.mu.
func (as *AutonomousScheduler) recordClosedStopLocked(key, at string) {
	if as.state.ClosedStops == nil {
		as.state.ClosedStops = make(map[string]string)
	}
	as.state.ClosedStops[key] = at
}

// closedStopSetLocked returns the issues held by an issue_closed stop, first
// releasing every hold whose close the graph now shows (#3081): the node is
// gone from the graph or reads CLOSED. From then on the graph's own closed
// check keeps the issue out, and the issue showing OPEN again is a reopen that
// makes it a candidate again. A hold whose node still reads OPEN stays: the
// graph has not caught up with the close the run saw. Caller holds as.mu.
func (as *AutonomousScheduler) closedStopSetLocked(g *depgraph.Graph) map[string]bool {
	held := make(map[string]bool, len(as.state.ClosedStops))
	released := false
	for key := range as.state.ClosedStops {
		node, present := g.Nodes[key]
		if !present || strings.EqualFold(node.State, "CLOSED") {
			delete(as.state.ClosedStops, key)
			released = true
			log.Printf("autonomous: %s — the graph shows the close; releasing the issue_closed hold (#3081)", key)
			continue
		}
		held[key] = true
	}
	if released {
		if len(as.state.ClosedStops) == 0 {
			as.state.ClosedStops = nil
		}
		as.persistStateLocked()
	}
	return held
}
