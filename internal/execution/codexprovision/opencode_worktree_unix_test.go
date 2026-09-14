//go:build unix

package codexprovision

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestOpenCodeProvisionReadsNoFIFO: a FIFO a stage leaves at the name of a
// steering or MCP source, docs/GIT_WORKFLOW.md and .mcp.json here, never
// finishes reading, so it would hang the preparation of every later stage.
// Neither is read, a warning names each, and the preparation returns.
func TestOpenCodeProvisionReadsNoFIFO(t *testing.T) {
	wt := openCodeRepo(t, map[string]string{"AGENTS.md": "# Contract\n\nRules.\n"})
	for _, name := range []string{"docs/GIT_WORKFLOW.md", ".mcp.json"} {
		path := filepath.Join(wt, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	type result struct {
		p   OpenCodeProvision
		err error
	}
	done := make(chan result, 1)
	go func() {
		p, err := provision(wt)
		done <- result{p, err}
	}()
	var r result
	select {
	case r = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("ProvisionOpenCode has not returned after 20s: it is reading a FIFO")
	}
	if r.err != nil {
		t.Fatal(r.err)
	}
	w := strings.Join(r.p.Warnings, "\n")
	for _, want := range []string{"docs/GIT_WORKFLOW.md is not a regular file", ".mcp.json is not a regular file"} {
		if !strings.Contains(w, want) {
			t.Errorf("no warning says %q:\n%s", want, w)
		}
	}
	if !strings.Contains(r.p.Steering, "Rules.") {
		t.Errorf("the steering lost the regular AGENTS.md beside the FIFOs:\n%s", r.p.Steering)
	}
}
