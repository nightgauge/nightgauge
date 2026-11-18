package skillrender

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// Step 2.5.3 calls `scan diff-secrets` (#2702). Run 2.5.3 and 2.5.4 together
// against a stub verb: a critical count stops with the record, a warnings-only
// count passes without one, and a verb that cannot run stops with the same
// security_scan record, so no fallback PR opens without a scan (#3421).
func TestPrCreateDiffScan_FeedsTheStop(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(prCreateSecurityRel)))
	if err != nil {
		t.Fatalf("read %s: %v", prCreateSecurityRel, err)
	}
	scanBlock := nthFencedBashAfter(t, string(data), "### Step 2.5.3: Binary secret scan", 1)
	stopBlock := nthFencedBashAfter(t, string(data), "### Step 2.5.4: Evaluate findings and set status", 1)
	block := scanBlock + "\n" + stopBlock

	cases := []struct {
		name       string
		scanJSON   string
		scanExit   int
		wantExit   int
		wantRecord bool
		wantOut    string
	}{
		{"critical", `{"v":1,"critical":1,"warnings":0,"files":[{"path":"a","critical":1,"warnings":0}]}`, 1, 1, true, "a: critical=1 warnings=0"},
		{"warnings-only", `{"v":1,"critical":0,"warnings":2,"files":[{"path":"a","critical":0,"warnings":2}]}`, 0, 0, false, "WARNING: 2 potential"},
		{"verb-error", `{"v":1,"critical":0,"warnings":0,"files":[]}`, 2, 1, true, "could not run"},
		// An older binary without the verb exits 1 with nothing on stdout.
		{"exit1-no-json", ``, 1, 1, true, "could not run"},
		{"exit2-no-json", ``, 2, 1, true, "could not run"},
		{"exit1-no-critical", `{"v":1,"critical":0,"warnings":0,"files":[]}`, 1, 1, true, "could not run"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := gittest.InitRepo(t, t.TempDir(), "-q", "-b", "main")
			gittest.Run(t, repo, "-c", "user.email=t@example.com", "-c", "user.name=T",
				"-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "base")

			outDir := t.TempDir()
			stub := filepath.Join(t.TempDir(), "nightgauge")
			script := "#!/bin/sh\n" +
				"if [ \"$1 $2 $3\" = \"layout write pipeline\" ]; then cat > \"$OUT_DIR/$4\"; exit 0; fi\n" +
				"if [ \"$1 $2\" = \"scan diff-secrets\" ]; then printf '%s' '" + c.scanJSON + "'; exit " +
				strconv.Itoa(c.scanExit) + "; fi\n" +
				"exit 3\n"
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
				"BASE_BRANCH=main",
				"SECURITY_CRITICAL=0",
				"SECURITY_WARNINGS=0",
			}
			out, err := cmd.CombinedOutput()
			got := 0
			if ee, ok := err.(*exec.ExitError); ok {
				got = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if got != c.wantExit {
				t.Fatalf("exit = %d, want %d\n%s", got, c.wantExit, out)
			}
			if !strings.Contains(string(out), c.wantOut) {
				t.Errorf("output lacks %q:\n%s", c.wantOut, out)
			}
			rec, statErr := os.ReadFile(filepath.Join(outDir, "pr-create-gate-stop-42.json"))
			if (statErr == nil) != c.wantRecord {
				t.Fatalf("record present = %v, want %v\n%s", statErr == nil, c.wantRecord, out)
			}
			if c.wantRecord {
				var r struct {
					Gate    string `json:"gate"`
					Reason  string `json:"reason"`
					HeadSHA string `json:"head_sha"`
				}
				if err := json.Unmarshal(rec, &r); err != nil {
					t.Fatalf("record is not JSON: %v\n%s", err, rec)
				}
				head := strings.TrimSpace(gittest.Run(t, repo, "rev-parse", "HEAD"))
				if r.Gate != "security_scan" || r.HeadSHA != head || r.Reason == "" {
					t.Errorf("record = %+v, want gate security_scan, head_sha %s, a reason", r, head)
				}
				if wantRun := c.wantOut == "could not run"; wantRun != strings.Contains(r.Reason, "could not run") {
					t.Errorf("reason %q does not match the stop path", r.Reason)
				}
			}
		})
	}
}
