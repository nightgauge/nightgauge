package skillrender

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pr-merge's Step 7.1–7.2 issue close (#2674, #2840): each fenced block runs
// in a fresh shell, so the block itself must find the PR number and pass
// --pr, or a `Refs #N` PR closes the issue it only partly delivered.

const prMergePostMergeRel = "skills/nightgauge-pr-merge/_includes/post-merge.md"

func TestPostMergePR_IssueClosePassesPR(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(prMergePostMergeRel)))
	if err != nil {
		t.Fatalf("read %s: %v", prMergePostMergeRel, err)
	}
	block := nthFencedBashAfter(t, string(data), "#### Step 7.1–7.2: Post-Merge Label, Board, and Closure", 1)
	// Run only the close; the verification after it calls the forge.
	if i := strings.Index(block, "# Step 7.2.1.5"); i > 0 {
		block = block[:i]
	}

	for _, tc := range []struct {
		name   string
		prJSON string
	}{
		{"from pr context", `{"pr_number": 77}`},
		{"from merged PR only", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			if tc.prJSON != "" {
				if err := os.WriteFile(filepath.Join(state, "pr-42.json"), []byte(tc.prJSON), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			calls := filepath.Join(t.TempDir(), "calls.log")
			stub := filepath.Join(t.TempDir(), "nightgauge")
			script := "#!/bin/sh\n" +
				"echo \"$*\" >> \"$CALLS\"\n" +
				"case \"$1 $2\" in\n" +
				"  'layout path') echo \"$STATE/$4\" ;;\n" +
				"  'pr view') echo '{\"number\":77,\"state\":\"MERGED\"}' ;;\n" +
				"  'issue close') echo '{\"result\":\"skipped\",\"reason\":\"Refs\"}' ;;\n" +
				"esac\n"
			if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "-c", block)
			cmd.Dir = t.TempDir()
			cmd.Env = []string{
				"PATH=" + sandboxPath(t),
				"HOME=" + t.TempDir(),
				"NIGHTGAUGE_BIN=" + stub,
				"NIGHTGAUGE_REPO=o/r",
				"NIGHTGAUGE_ISSUE_NUMBER=42",
				"CALLS=" + calls,
				"STATE=" + state,
			}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("block failed: %v\n%s", err, out)
			}
			log, _ := os.ReadFile(calls)
			var closeCall string
			for _, l := range strings.Split(string(log), "\n") {
				if strings.HasPrefix(l, "issue close ") {
					closeCall = l
				}
			}
			if !strings.Contains(closeCall, "--pr 77") {
				t.Errorf("issue close argv = %q, want --pr 77\ncalls:\n%s", closeCall, log)
			}
			if !strings.Contains(string(out), "issue_kept_open: true") {
				t.Errorf("output lacks issue_kept_open: true:\n%s", out)
			}
		})
	}
}
