package stages

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestPhaseSpecsCoverEveryRegistryPhase pins the "named explicitly rather than
// inferred" half of #1247. The failure it guards against is not a crash: an
// unroled phase would simply never be reported and never be skipped, and the
// stage would quietly go back to showing fewer phases than it has.
func TestPhaseSpecsCoverEveryRegistryPhase(t *testing.T) {
	for _, spec := range phaseSpecs {
		if len(spec.roles) != len(spec.order) {
			t.Errorf("%s: %d roles for %d phases — every registry phase needs exactly one",
				spec.stage, len(spec.roles), len(spec.order))
		}
		for _, name := range spec.order {
			if _, ok := spec.roles[name]; !ok {
				t.Errorf("%s: phase %q has no role — it would be neither reported nor skipped", spec.stage, name)
			}
		}
		for name := range spec.roles {
			if spec.index(name) < 0 {
				t.Errorf("%s: role declared for %q, which is not in the registry order", spec.stage, name)
			}
		}
	}
}

// TestPhaseRegistryParityWithTypeScript reads PHASE_REGISTRY itself. The Go
// tables are a mirror, and a mirror that can drift is worse than no mirror:
// a renamed phase would be reported under a name no consumer knows, and the
// tree would show it as an extra row while the real one stayed unreported.
func TestPhaseRegistryParityWithTypeScript(t *testing.T) {
	root := repoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "packages", "nightgauge-sdk", "src", "events", "phaseRegistry.ts"))
	if err != nil {
		t.Fatalf("read phaseRegistry.ts: %v", err)
	}
	for _, spec := range phaseSpecs {
		want := parseTSStagePhases(t, string(src), spec.stage)
		if len(want) == 0 {
			t.Fatalf("%s: no phases parsed out of phaseRegistry.ts", spec.stage)
		}
		if len(want) != len(spec.order) {
			t.Fatalf("%s: registry has %d phases, Go mirror has %d", spec.stage, len(want), len(spec.order))
		}
		for i := range want {
			if want[i] != spec.order[i] {
				t.Errorf("%s: phase %d is %q in phaseRegistry.ts and %q in the Go mirror",
					spec.stage, i, want[i], spec.order[i])
			}
		}
	}
}

var tsPhaseEntry = regexp.MustCompile(`\{\s*name:\s*"([a-z0-9-]+)",\s*index:\s*(\d+)\s*\}`)

func parseTSStagePhases(t *testing.T, src, stage string) []string {
	t.Helper()
	block := regexp.MustCompile(`(?s)"` + regexp.QuoteMeta(stage) + `":\s*\[(.*?)\],\n`)
	m := block.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("stage %q not found in phaseRegistry.ts", stage)
	}
	var names []string
	for _, e := range tsPhaseEntry.FindAllStringSubmatch(m[1], -1) {
		names = append(names, e[1])
	}
	return names
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

// recordingReporter captures the transition stream so a test can assert on
// order and status without standing up a RuntimeState.
type recordingReporter struct {
	events []phaseEvent
}

type phaseEvent struct {
	stage, name, status string
	index, total        int
}

func (r *recordingReporter) PhaseStart(stage, name string, index, total int) {
	r.events = append(r.events, phaseEvent{stage, name, "running", index, total})
}
func (r *recordingReporter) PhaseComplete(stage, name string) {
	r.events = append(r.events, phaseEvent{stage: stage, name: name, status: "complete", index: -1})
}
func (r *recordingReporter) PhaseFail(stage, name string, index, total int) {
	r.events = append(r.events, phaseEvent{stage, name, "failed", index, total})
}
func (r *recordingReporter) PhaseSkip(stage, name string, index, total int) {
	r.events = append(r.events, phaseEvent{stage, name, "skipped", index, total})
}

func (r *recordingReporter) namesWithStatus(status string) []string {
	var out []string
	for _, e := range r.events {
		if e.status == status {
			out = append(out, e.name)
		}
	}
	return out
}

func (r *recordingReporter) has(name, status string) bool {
	for _, e := range r.events {
		if e.name == name && e.status == status {
			return true
		}
	}
	return false
}

func reporterCtx(r PhaseReporter) context.Context {
	return WithPhaseReporter(context.Background(), r)
}
