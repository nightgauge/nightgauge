package skillrender

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const prePushStepHeading = "### Step 2.7.1: Resolve Binary and Run Gate"

func prePushStep(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "skills", "nightgauge-feature-validate", "_includes", "ci-and-knowledge.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return fencedBashAfter(t, string(data), prePushStepHeading)
}

// feature-validate relies on the limit the verb resolves. A literal here would
// override the repository's hooks.pre_push.timeout_seconds (#2700).
func TestFeatureValidatePrePush_RelyOnResolvedLimit(t *testing.T) {
	if strings.Contains(prePushStep(t), "--timeout") {
		t.Error("Step 2.7.1 must not pass --timeout: the verb resolves the configured limit")
	}
}

// Runs the step's own shell against a stubbed binary per exit code.
func TestFeatureValidatePrePush_ExitCodeMapping(t *testing.T) {
	for _, tc := range []struct {
		exit, status, category string
	}{
		{"0", "passed", ""},
		{"1", "failed", ""},
		{"3", "timeout", "gate-timeout"},
	} {
		t.Run("exit "+tc.exit, func(t *testing.T) {
			dir := t.TempDir()
			stub := filepath.Join(dir, "nightgauge")
			script := "#!/bin/sh\ncase \"$1\" in pre-push) exit " + tc.exit + ";; esac\nexit 0\n"
			if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			body := "SKIPPED_PHASES='[]'\n" + prePushStep(t) +
				"\necho \"RESULT=$PREPUSH_STATUS/${ERROR_CATEGORY:-}\"\n"
			cmd := exec.Command("bash", "-c", body)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "NIGHTGAUGE_BIN="+stub, "NIGHTGAUGE_ISSUE_NUMBER=7",
				"PATH="+dir+":"+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			if want := "RESULT=" + tc.status + "/" + tc.category; !strings.Contains(string(out), want) {
				t.Errorf("want %s in:\n%s", want, out)
			}
		})
	}
}
