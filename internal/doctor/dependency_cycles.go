package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/layout"
)

// Dependency cycles among open issues (#3081).
//
// Issues whose open native blockedBy edges form a cycle each wait on another,
// so none of them can ever dispatch. The autonomous scheduler detects the
// cycles on every prioritize pass (orchestrator.detectDependencyCycles),
// raises a dependency-cycle card per cycle, and records what it saw in
// dependency-cycles.json. doctor reads that record, so it reports a cycle
// without a forge call, as of the scheduler's last pass. No record — a
// scheduler that has not run in this checkout — passes.

const (
	dependencyCyclesCheck = "dependency_cycles"
	dependencyCyclesCode  = "NGD114"
)

// DependencyCyclesFile is the record of the cycles the scheduler last saw, in
// the pipeline state directory. The scheduler writes it and this check reads
// it; both name it through this constant.
const DependencyCyclesFile = "dependency-cycles.json"

type dependencyCycleRecord struct {
	ObservedAt string `json:"observed_at"`
	Cycles     []struct {
		Issues []string `json:"issues"`
		Edges  []struct {
			Blocked string `json:"blocked"`
			Blocker string `json:"blocker"`
		} `json:"edges"`
	} `json:"cycles"`
}

func init() {
	builtinChecks = append(builtinChecks, Check{
		ID: dependencyCyclesCheck, Title: "Dependency cycles among open issues", Group: "hygiene",
		Code: dependencyCyclesCode,
		Run: func(_ context.Context, env *Env) []Finding {
			fs, detail := dependencyCycleFindings(env.Cwd)
			env.SetDetail(dependencyCyclesCheck, detail)
			return fs
		},
	})
}

func dependencyCycleFindings(root string) ([]Finding, string) {
	dir, err := layout.PipelineStateDir(root)
	if err != nil {
		return nil, "skipped: no pipeline state directory: " + err.Error()
	}
	data, err := os.ReadFile(filepath.Join(dir, DependencyCyclesFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, "no record: the autonomous scheduler has not run in this checkout"
	}
	if err != nil {
		return nil, "skipped: " + err.Error()
	}
	var rec dependencyCycleRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, "skipped: unreadable record: " + err.Error()
	}
	if len(rec.Cycles) == 0 {
		return nil, "no dependency cycles as of " + rec.ObservedAt
	}
	findings := make([]Finding, 0, len(rec.Cycles))
	for _, c := range rec.Cycles {
		edges := make([]string, 0, len(c.Edges))
		for _, e := range c.Edges {
			edges = append(edges, e.Blocked+" → "+e.Blocker)
		}
		f := newFinding(dependencyCyclesCheck, dependencyCyclesCode, SeverityWarning,
			fmt.Sprintf("Dependency cycle among %d open issues: %s", len(c.Issues), strings.Join(c.Issues, ", ")),
			"These issues block each other through native blockedBy edges ("+strings.Join(edges, "; ")+
				"), so none of them can dispatch. As of the scheduler's pass at "+rec.ObservedAt+".",
			map[string]string{"issues": strings.Join(c.Issues, ","), "edges": strings.Join(edges, "; "),
				"observed_at": rec.ObservedAt},
			c.Issues,
			manualRemedy("remove-edge", "Remove the wrong blockedBy edge", dependencyCyclesCheck,
				"Use the dependency-cycle card in the Action Center, which offers each edge",
				"Or run `nightgauge issue remove-blocked-by <blocked> <blocker>`"))
		findings = append(findings, f)
	}
	return findings, fmt.Sprintf("%d dependency cycle(s) as of %s", len(rec.Cycles), rec.ObservedAt)
}
