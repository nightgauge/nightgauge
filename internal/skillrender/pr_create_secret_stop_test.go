package skillrender

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// pr-create's Step 2.5.4 secret-scan stop (#2677, #2840), run as the skill
// runner runs it: NIGHTGAUGE_BIN is exported and `nightgauge` is not on PATH.
// Without the record the orchestrator's fallback opens the PR anyway.

const prCreateSecurityRel = "skills/nightgauge-pr-create/_includes/security-and-scope.md"

func TestPrCreateSecretStop_WritesRecordViaNightgaugeBin(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(prCreateSecurityRel)))
	if err != nil {
		t.Fatalf("read %s: %v", prCreateSecurityRel, err)
	}
	block := nthFencedBashAfter(t, string(data), "### Step 2.5.4: Evaluate findings and set status", 1)

	repo := gittest.InitRepo(t, t.TempDir(), "-q", "-b", "main")
	gittest.Run(t, repo, "-c", "user.email=t@example.com", "-c", "user.name=T",
		"-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "base")
	head := gittest.Run(t, repo, "rev-parse", "HEAD")

	outDir := t.TempDir()
	stub := filepath.Join(t.TempDir(), "nightgauge")
	script := "#!/bin/sh\n" +
		"if [ \"$1 $2 $3\" = \"layout write pipeline\" ]; then cat > \"$OUT_DIR/$4\"; exit 0; fi\n" +
		"exit 2\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	sandbox := sandboxPath(t)
	date, err := exec.LookPath("date")
	if err != nil {
		t.Skip("date is not on PATH; the skill's shell needs it")
	}
	if err := os.Symlink(date, filepath.Join(sandbox, "date")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", block)
	cmd.Dir = repo
	cmd.Env = []string{
		"PATH=" + sandbox,
		"HOME=" + t.TempDir(),
		"NIGHTGAUGE_BIN=" + stub,
		"NIGHTGAUGE_ISSUE_NUMBER=42",
		"OUT_DIR=" + outDir,
		"SECURITY_CRITICAL=1",
		"SECURITY_WARNINGS=0",
	}
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
		t.Fatalf("want exit 1, got %v\n%s", err, out)
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "pr-create-gate-stop-42.json"))
	if err != nil {
		t.Fatalf("no gate-stop record written: %v\noutput:\n%s", err, out)
	}
	var rec struct {
		Gate    string `json:"gate"`
		HeadSHA string `json:"head_sha"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("record is not JSON: %v\n%s", err, raw)
	}
	if rec.Gate != "security_scan" || rec.HeadSHA != head {
		t.Errorf("record = %+v, want gate security_scan at %s", rec, head)
	}
}
