package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// preflightCascade returns PREFLIGHT.md's Layer 1 discovery block, cut after
// the ~/go/bin rung, with an `echo "$BINARY"` appended.
func preflightCascade(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRootForTest(t), "skills", "_shared", "PREFLIGHT.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "# Layer 1:")
	if start < 0 {
		t.Fatal("PREFLIGHT.md has no Layer 1 block")
	}
	block := text[start:]
	end := strings.Index(block, `BINARY="$HOME/go/bin/nightgauge"`)
	if end < 0 {
		t.Fatal("PREFLIGHT.md Layer 1 block has no ~/go/bin rung")
	}
	return block[:end+len(`BINARY="$HOME/go/bin/nightgauge"`)] + "\necho \"$BINARY\"\n"
}

func repoRootForTest(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestPreflightCascade_ShimRung runs the skill cascade itself (#2928): the
// per-user shim is chosen after PATH and before ~/go/bin, and on Darwin only
// when codesign verifies its target against bundleCodeRequirement.
func TestPreflightCascade_ShimRung(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	script := preflightCascade(t)
	// The stub codesign accepts only the pinned requirement and a target
	// without the NG_UNSIGNED marker; it records that it ran.
	codesign := "#!/bin/sh\n" +
		"echo ran >> \"$HOME/codesign.log\"\n" +
		"[ \"$3\" = '-R=" + bundleCodeRequirement + "' ] || exit 3\n" +
		"for last in \"$@\"; do :; done\n! grep -q NG_UNSIGNED \"$last\"\n"

	cases := []struct {
		name, uname string
		setup       func(t *testing.T, home, pathDir string)
		want        string // relative to home, or "PATH"
		codesignRan bool
	}{
		{"signed symlinked shim", "Darwin", func(t *testing.T, home, _ string) {
			writeExec(t, filepath.Join(home, "bundle", "nightgauge"), "#!/bin/sh\n")
			linkShim(t, home, "../../bundle/nightgauge")
		}, ".nightgauge/bin/nightgauge", true},
		{"unsigned target", "Darwin", func(t *testing.T, home, _ string) {
			writeExec(t, filepath.Join(home, "bundle", "nightgauge"), "#!/bin/sh\n# NG_UNSIGNED\n")
			linkShim(t, home, "../../bundle/nightgauge")
		}, "go/bin/nightgauge", true},
		{"dangling symlink", "Darwin", func(t *testing.T, home, _ string) {
			linkShim(t, home, "../../nope")
		}, "go/bin/nightgauge", false},
		{"not executable", "Darwin", func(t *testing.T, home, _ string) {
			if err := os.MkdirAll(filepath.Join(home, ".nightgauge", "bin"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, ".nightgauge", "bin", "nightgauge"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, "go/bin/nightgauge", false},
		{"no shim", "Darwin", func(*testing.T, string, string) {}, "go/bin/nightgauge", false},
		{"PATH wins over shim", "Darwin", func(t *testing.T, home, pathDir string) {
			writeExec(t, filepath.Join(home, ".nightgauge", "bin", "nightgauge"), "#!/bin/sh\n")
			writeExec(t, filepath.Join(pathDir, "nightgauge"), "#!/bin/sh\n")
		}, "PATH", false},
		{"no signature check off Darwin", "Linux", func(t *testing.T, home, _ string) {
			writeExec(t, filepath.Join(home, ".nightgauge", "bin", "nightgauge"), "#!/bin/sh\n# NG_UNSIGNED\n")
		}, ".nightgauge/bin/nightgauge", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			stubs := t.TempDir()
			writeExec(t, filepath.Join(stubs, "codesign"), codesign)
			writeExec(t, filepath.Join(stubs, "uname"), "#!/bin/sh\necho "+tc.uname+"\n")
			writeExec(t, filepath.Join(home, "go", "bin", "nightgauge"), "#!/bin/sh\n")
			tc.setup(t, home, stubs)
			cmd := exec.Command(bash, "-c", script)
			cmd.Dir = t.TempDir()
			cmd.Env = []string{"HOME=" + home, "PATH=" + stubs + ":/usr/bin:/bin"}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("cascade failed: %v\n%s", err, out)
			}
			want := filepath.Join(home, tc.want)
			if tc.want == "PATH" {
				want = filepath.Join(stubs, "nightgauge")
			}
			if got := strings.TrimSpace(string(out)); got != want {
				t.Fatalf("resolved %q, want %q", got, want)
			}
			_, logErr := os.Stat(filepath.Join(home, "codesign.log"))
			if ran := logErr == nil; ran != tc.codesignRan {
				t.Fatalf("codesign ran = %v, want %v", ran, tc.codesignRan)
			}
		})
	}
}

func linkShim(t *testing.T, home, target string) {
	t.Helper()
	dir := filepath.Join(home, ".nightgauge", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "nightgauge")); err != nil {
		t.Fatal(err)
	}
}

// TestSkillShimRungs_CarryBundleRequirement pins every shim rung under
// skills/, not only PREFLIGHT.md, to bundleCodeRequirement (#2928).
func TestSkillShimRungs_CarryBundleRequirement(t *testing.T) {
	root := filepath.Join(repoRootForTest(t), "skills")
	reqRE := regexp.MustCompile(`'-R=([^']*)'`)
	pinned := "'-R=" + bundleCodeRequirement + "'"
	rungs := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(raw), "\n")
		for i, line := range lines {
			if strings.TrimSpace(line) != `NG_SHIM="$HOME/.nightgauge/bin/nightgauge"` {
				continue
			}
			rungs++
			if i+2 >= len(lines) || !strings.Contains(lines[i+2], pinned) {
				t.Errorf("%s:%d: shim rung does not carry bundleCodeRequirement verbatim", path, i+1)
			}
		}
		for i, line := range lines {
			if m := reqRE.FindStringSubmatch(line); m != nil && m[1] != bundleCodeRequirement {
				t.Errorf("%s:%d: codesign requirement drifted from bundleCodeRequirement", path, i+1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if rungs == 0 {
		t.Fatal("found no shim rungs under skills/")
	}
}
