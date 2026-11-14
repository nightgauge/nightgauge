//go:build darwin

package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestBundleCodeRequirement_RealCodesign runs the real codesign against the
// pinned requirement (#2776). The stub-based tests cannot tell a malformed or
// over-permissive requirement from a correct one; this test can: the
// requirement must compile, and both an ad-hoc signed binary (which passes a
// bare --verify) and an Apple platform binary must be refused.
func TestBundleCodeRequirement_RealCodesign(t *testing.T) {
	codesign, err := exec.LookPath("codesign")
	if err != nil {
		t.Skip("codesign not available")
	}
	if out, err := exec.Command("csreq", "-r="+bundleCodeRequirement, "-t").CombinedOutput(); err != nil {
		t.Fatalf("requirement does not compile: %v\n%s", err, out)
	}

	src, err := os.ReadFile("/bin/ls")
	if err != nil {
		t.Fatalf("read /bin/ls: %v", err)
	}
	adhoc := filepath.Join(t.TempDir(), "nightgauge")
	if err := os.WriteFile(adhoc, src, 0o755); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if out, err := exec.Command(codesign, "-s", "-", "-f", adhoc).CombinedOutput(); err != nil {
		t.Fatalf("ad-hoc sign: %v\n%s", err, out)
	}
	if err := exec.Command(codesign, "--verify", "--strict", adhoc).Run(); err != nil {
		t.Fatalf("precondition: bare --verify should accept the ad-hoc copy: %v", err)
	}

	prev := bundleSignatureGOOS
	bundleSignatureGOOS = "darwin"
	t.Cleanup(func() { bundleSignatureGOOS = prev })
	for name, path := range map[string]string{"ad-hoc signed": adhoc, "apple platform": "/bin/ls"} {
		if verifyBundleSignature(path) {
			t.Errorf("%s binary %s satisfied the bundle requirement; it must be refused", name, path)
		}
	}
}
