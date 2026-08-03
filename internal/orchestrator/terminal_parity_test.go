package orchestrator

// Terminal-path behavior parity (#257).
//
// Nightgauge has two pipeline execution paths — the Go scheduler loop
// (runPipeline) and the extension path (ConcurrentPipelineManager →
// HeadlessOrchestrator + the IPC pipeline.notifyComplete funnel). A behavior
// wired to only one of them is invisible on the other with no error and no
// failed test (#210, #254). This suite enforces the shared manifest
// testdata/terminal_behaviors.json:
//
//  1. Every behavior's anchor must exist in its file on each path (or the
//     side must carry an explicit pathSpecific reason).
//  2. The fenced terminal-funnel regions are content-pinned by sha256 of
//     their normalized source. Any edit inside a fence fails here until the
//     manifest is updated — which is the moment to answer the review
//     question: which of the two paths reaches the new behavior, and is the
//     other intentionally excluded?
//
// The TypeScript twin (packages/nightgauge-vscode/tests/services/
// terminalParity.test.ts) runs the same checks from the same manifest, so
// drift fails whichever suite CI reaches first.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type paritySide struct {
	File         string `json:"file"`
	Anchor       string `json:"anchor"`
	PathSpecific string `json:"pathSpecific"`
}

type parityBehavior struct {
	Name      string      `json:"name"`
	Note      string      `json:"note"`
	Go        *paritySide `json:"go"`
	Extension *paritySide `json:"extension"`
}

type parityFence struct {
	ID     string `json:"id"`
	File   string `json:"file"`
	Begin  string `json:"begin"`
	End    string `json:"end"`
	SHA256 string `json:"sha256"`
}

type parityManifest struct {
	Behaviors []parityBehavior `json:"behaviors"`
	Fences    []parityFence    `json:"fences"`
}

func loadParityManifest(t *testing.T) parityManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "terminal_behaviors.json"))
	if err != nil {
		t.Fatalf("read terminal_behaviors.json: %v", err)
	}
	var m parityManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse terminal_behaviors.json: %v", err)
	}
	if len(m.Behaviors) == 0 || len(m.Fences) == 0 {
		t.Fatalf("terminal_behaviors.json must list behaviors and fences (got %d behaviors, %d fences)",
			len(m.Behaviors), len(m.Fences))
	}
	return m
}

func repoRootFile(t *testing.T, rel string) string {
	t.Helper()
	path := filepath.Join("..", "..", filepath.FromSlash(rel))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// normalizeFence must stay identical to the TS twin: per line, trim
// whitespace; drop empty lines; drop lines starting with "//"; join "\n".
func normalizeFence(src string) string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		out = append(out, trimmed)
	}
	return strings.Join(out, "\n")
}

func TestTerminalBehaviorAnchors(t *testing.T) {
	m := loadParityManifest(t)
	sources := map[string]string{}
	source := func(rel string) string {
		if s, ok := sources[rel]; ok {
			return s
		}
		s := repoRootFile(t, rel)
		sources[rel] = s
		return s
	}

	checkSide := func(behavior, pathName string, side *paritySide) {
		if side == nil {
			t.Errorf("%s: %s side missing — declare an anchor or a pathSpecific reason", behavior, pathName)
			return
		}
		hasAnchor := side.Anchor != ""
		hasReason := side.PathSpecific != ""
		if hasAnchor == hasReason {
			t.Errorf("%s: %s side must have exactly one of anchor/pathSpecific", behavior, pathName)
			return
		}
		if hasReason {
			return // explicitly path-specific, with its reason recorded
		}
		if side.File == "" {
			t.Errorf("%s: %s anchor needs a file", behavior, pathName)
			return
		}
		if !strings.Contains(source(side.File), side.Anchor) {
			t.Errorf("%s: anchor %q not found in %s — the %s-path call site moved or was removed. "+
				"If the behavior was rewired, update testdata/terminal_behaviors.json; if it was removed "+
				"from this path only, record a pathSpecific reason (and an issue if the gap is a defect).",
				behavior, side.Anchor, side.File, pathName)
		}
	}

	for _, b := range m.Behaviors {
		if b.Name == "" {
			t.Error("behavior with empty name in terminal_behaviors.json")
			continue
		}
		checkSide(b.Name, "go", b.Go)
		checkSide(b.Name, "extension", b.Extension)
	}
}

func TestTerminalFunnelFencesPinned(t *testing.T) {
	m := loadParityManifest(t)
	for _, f := range m.Fences {
		src := repoRootFile(t, f.File)
		lines := strings.Split(src, "\n")
		beginIdx, endIdx := -1, -1
		for i, line := range lines {
			if strings.Contains(line, f.Begin) && !strings.Contains(line, "\"") {
				if beginIdx >= 0 {
					t.Fatalf("%s: begin marker %q appears more than once in %s", f.ID, f.Begin, f.File)
				}
				beginIdx = i
			}
			if strings.Contains(line, f.End) && !strings.Contains(line, "\"") {
				if endIdx >= 0 {
					t.Fatalf("%s: end marker %q appears more than once in %s", f.ID, f.End, f.File)
				}
				endIdx = i
			}
		}
		if beginIdx < 0 || endIdx < 0 || endIdx <= beginIdx {
			t.Fatalf("%s: fence markers not found or out of order in %s (begin=%d end=%d)",
				f.ID, f.File, beginIdx, endIdx)
		}
		normalized := normalizeFence(strings.Join(lines[beginIdx+1:endIdx], "\n"))
		sum := sha256.Sum256([]byte(normalized))
		got := hex.EncodeToString(sum[:])
		if got != f.SHA256 {
			t.Errorf("%s: terminal funnel content changed in %s.\n"+
				"  got:  %s\n  want: %s\n"+
				"This fence pins the terminal-path funnel (#257). If your change is deliberate:\n"+
				"  1. Answer the parity question: does the OTHER execution path need the same behavior?\n"+
				"     (Go: runPipeline terminal defer; extension: runSlotPipeline finally + pipeline.notifyComplete)\n"+
				"  2. Add/update the behavior row in internal/orchestrator/testdata/terminal_behaviors.json\n"+
				"     (anchor on both paths, or pathSpecific with a reason/issue).\n"+
				"  3. Update this fence's sha256 to the 'got' value above.",
				f.ID, f.File, got, f.SHA256)
		}
	}
}
