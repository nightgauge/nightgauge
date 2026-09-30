package adapters

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/execution/opencodeplugin"
)

// #1826: a Bash-capable stage can set core.fsmonitor in the worktree's
// repo-local config. The tamper gate's own `git status` must not run it.
func TestOpenCodeTamperGateIgnoresRepoLocalFsmonitor(t *testing.T) {
	wt := openCodeFixtureRepo(t, tamperFixtureBaseFiles)
	marker := filepath.Join(t.TempDir(), "PWNED")
	cmd := exec.Command("git", "-C", wt, "config", "core.fsmonitor", "touch '"+marker+"'; false")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config: %v\n%s", err, out)
	}

	// The probe from the issue: unhardened, git runs the configured command.
	// Skip when this git does not, since the regression below would then
	// prove nothing.
	probe := exec.Command("git", "-C", wt, "status", "--porcelain=v1", "--ignored", "--untracked-files=all", "--", "opencode.json", "opencode.jsonc", ".opencode")
	_ = probe.Run()
	if _, err := os.Stat(marker); err != nil {
		t.Skip("this git does not run core.fsmonitor from git status; the probe cannot show the hole")
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}

	if err := openCodeProjectConfigTamperCheck(context.Background(), wt); err != nil {
		t.Fatalf("a clean worktree was refused: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the tamper gate ran the worktree's core.fsmonitor command in the orchestrator process")
	}
}

func TestOpenCodeGitCommandIsHardened(t *testing.T) {
	cmd := openCodeGitCommand(context.Background(), "/wt", "status")
	argv := strings.Join(cmd.Args, " ")
	for _, want := range []string{"-c core.fsmonitor=false", "-c core.hooksPath=/dev/null", "-C /wt status"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q lacks %q", argv, want)
		}
	}
	found := false
	for _, e := range cmd.Env {
		if e == "GIT_CONFIG_NOSYSTEM=1" {
			found = true
		}
	}
	if !found {
		t.Error("the environment does not set GIT_CONFIG_NOSYSTEM=1")
	}
}

// #1827: opencode's matcher is case-sensitive, so on a case-insensitive
// filesystem a case variant of a denied path reaches the same file while
// matching no deny. The plugin folds case before the tool runs; this pins
// that the folded path still hits the map's deny, and that the raw variant
// really does slip past the map (why the plugin check exists).
func TestOpenCodeCaseVariantsHitTheDenyOnceFolded(t *testing.T) {
	pm := openCodePermissionMap(RunOptions{AllowedTools: []string{"Read", "Edit", "Write"}}, "")
	cases := []struct {
		name string
		pm   *openCodePatternMap
		path string
	}{
		{"read .ENV", pm.Read, ".ENV"},
		{"read nested .ENV.local", pm.Read, "apps/web/.ENV.local"},
		{"edit OPENCODE.JSON", pm.Edit, "OPENCODE.JSON"},
		{"edit .OpenCode plugin", pm.Edit, ".OpenCode/plugins/x.ts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := openCodePatternAction(tc.pm, strings.ToLower(tc.path)); got != openCodeDeny {
				t.Errorf("folded %q resolves to %q, want deny", strings.ToLower(tc.path), got)
			}
			if got := openCodePatternAction(tc.pm, tc.path); got == openCodeDeny {
				t.Errorf("raw %q is already denied by opencode's own matcher; the case-fold gap this test documents is gone, so revisit #1827", tc.path)
			}
		})
	}
}

// The plugin's case-folded deny lists (gates.js) must stay equal to the
// permission map's own backstops.
func TestOpenCodePluginCaseFoldListsMatchTheGuard(t *testing.T) {
	plugin, err := opencodeplugin.Files()
	if err != nil {
		t.Fatal(err)
	}
	src, err := fs.ReadFile(plugin, "nightgauge/gates.js")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]string{
		"SECRET_DENY_PATTERNS":         openCodeSecretDenyBackstop,
		"PROJECT_CONFIG_DENY_PATTERNS": openCodeProjectConfigDenyBackstop,
	} {
		re := regexp.MustCompile(`(?s)export const ` + name + ` = Object\.freeze\(\[(.*?)\]\);`)
		m := re.FindSubmatch(src)
		if m == nil {
			t.Fatalf("gates.js has no %s", name)
		}
		var got []string
		for _, q := range regexp.MustCompile(`"([^"]*)"`).FindAllSubmatch(m[1], -1) {
			got = append(got, string(q[1]))
		}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("gates.js %s = %q, want %q", name, got, want)
		}
	}
}
