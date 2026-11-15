package preflight

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSkillPortability_FlagsBareBinaryInvocation pins the #2933 check: a bare
// `nightgauge <verb>` at command position in a bash fence is a finding; the
// NIGHTGAUGE_BIN form, "$BINARY", comments, backtick prose, prose outside a
// fence and non-bash fences are not.
func TestSkillPortability_FlagsBareBinaryInvocation(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "nightgauge-pr-create/SKILL.md", "---\nname: pr-create\n---\n"+
		"Run nightgauge doctor first.\n"+ // 4: prose
		"```bash\n"+ // 5
		"nightgauge layout path pipeline\n"+ // 6: flagged
		"X=$(nightgauge config show --key repo --raw)\n"+ // 7: flagged
		"true && nightgauge project sync-status 1 done\n"+ // 8: flagged
		"\"${NIGHTGAUGE_BIN:-nightgauge}\" layout path pipeline\n"+ // 9
		"BINARY=x \"$BINARY\" forge auth token\n"+ // 10
		"# nightgauge layout path pipeline\n"+ // 11
		"echo \"run \\`nightgauge doctor\\`\"\n"+ // 12
		"```\n"+
		"```text\n"+
		"nightgauge layout path pipeline\n"+
		"```\n")

	res, err := RunSkillPortabilityCheck(context.Background(), SkillPortabilityOptions{Root: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var lines []int
	for _, f := range res.Findings {
		if f.Check != CheckBareBinaryInvocation {
			t.Errorf("unexpected check %q: %+v", f.Check, f)
			continue
		}
		lines = append(lines, f.Line)
	}
	want := []int{6, 7, 8}
	if len(lines) != len(want) {
		t.Fatalf("finding lines = %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("finding lines = %v, want %v", lines, want)
		}
	}
}

// TestSkillPortability_BareBinaryInChangelogIgnored keeps history files out
// of the check: a changelog quotes the old text it replaced.
func TestSkillPortability_BareBinaryInChangelogIgnored(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "nightgauge-pr-create/CHANGELOG.md",
		"```bash\nnightgauge layout path pipeline\n```\n")
	res, err := RunSkillPortabilityCheck(context.Background(), SkillPortabilityOptions{Root: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("expected no findings, got %+v", res.Findings)
	}
}

// TestNightgaugeBinFormResolvesOffPath is AC 1 of #2933: with NIGHTGAUGE_BIN
// set to a binary that is not on PATH, the swept form runs it and a bare call
// is `command not found` (exit 127) — the defect the gate now rejects.
func TestNightgaugeBinFormResolvesOffPath(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "nightgauge-stub")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho \"stub:$*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=/usr/bin:/bin", "NIGHTGAUGE_BIN=" + stub}

	cmd := exec.Command("bash", "-c", `"${NIGHTGAUGE_BIN:-nightgauge}" layout path pipeline`)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("NIGHTGAUGE_BIN form failed: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "stub:layout path pipeline" {
		t.Fatalf("output = %q", got)
	}

	bare := exec.Command("bash", "-c", `nightgauge layout path pipeline`)
	bare.Env = env
	err = bare.Run()
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 127 {
		t.Fatalf("bare call: err = %v, want exit 127", err)
	}
}

// TestBareBinaryInvocationRE pins each command position the gate covers and
// the shapes it deliberately leaves alone.
func TestBareBinaryInvocationRE(t *testing.T) {
	for line, want := range map[string]bool{
		"  if nightgauge forge auth status; then":           true,
		"elif nightgauge config show --key repo; then":      true,
		"while nightgauge ci gate-status; do":               true,
		"for x in a; do nightgauge layout path plans; done": true,
		"a || nightgauge doctor":                            true,
		"jq . | nightgauge layout write pipeline x":         true,
		"true; nightgauge doctor":                           true,
		"! nightgauge doctor":                               true,
		"( nightgauge doctor )":                             true,
		`"${NIGHTGAUGE_BIN:-nightgauge}" doctor`:            false,
		"echo \"run \\`nightgauge doctor\\`\"":              false,
		"echo the nightgauge binary is missing":             false,
		"nightgauge-stub doctor":                            false,
	} {
		if got := bareBinaryInvocationRE.MatchString(line); got != want {
			t.Errorf("%q: match = %v, want %v", line, got, want)
		}
	}
}

// TestSkillPortability_BareBinaryInTildeShFence covers the other fence forms.
func TestSkillPortability_BareBinaryInTildeShFence(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "nightgauge-pr-create/SKILL.md",
		"~~~sh\nnightgauge doctor\n~~~\n```shell\nnightgauge doctor\n```\n")
	res, err := RunSkillPortabilityCheck(context.Background(), SkillPortabilityOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 2 {
		t.Fatalf("expected 2 findings, got %+v", res.Findings)
	}
}

// TestSkillPortability_FlagsUnsetBinaryRead pins the #3334 check: a block that
// reads $BINARY without assigning it is a finding; a block that assigns it is
// not, and NIGHTGAUGE_BINARY=, $BINARY_DIR and comments do not count.
func TestSkillPortability_FlagsUnsetBinaryRead(t *testing.T) {
	root := t.TempDir()
	writeSkillFile(t, root, "nightgauge-queue/SKILL.md", "---\nname: queue\n---\n"+
		"```bash\n"+ // 4
		"BINARY=/usr/local/bin/nightgauge\n"+ // 5
		"\"$BINARY\" queue list\n"+ // 6: assigned in block
		"```\n"+ // 7
		"```bash\n"+ // 8
		"\"$BINARY\" queue add 1\n"+ // 9: flagged
		"```\n"+ // 10
		"```bash\n"+ // 11
		"[ -n \"${BINARY:-}\" ] || exit 1\n"+ // 12: flagged
		"NIGHTGAUGE_BINARY=1 echo \"$BINARY_DIR\"\n"+ // 13: neither
		"# BINARY=x\n"+ // 14: comment, not an assignment
		"```\n"+ // 15
		"```bash\n"+ // 16
		"[ -x /a ] && BINARY=\"$cand\"\n"+ // 17
		"\"$BINARY\" queue clear\n"+ // 18: assigned after &&
		"```\n")
	writeSkillFile(t, root, "nightgauge-queue/CHANGELOG.md",
		"```bash\n\"$BINARY\" queue add 1\n```\n")

	res, err := RunSkillPortabilityCheck(context.Background(), SkillPortabilityOptions{Root: root})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var lines []int
	for _, f := range res.Findings {
		if f.Check != CheckUnsetBinaryRead {
			t.Errorf("unexpected check %q: %+v", f.Check, f)
			continue
		}
		if strings.HasSuffix(f.SkillFile, "CHANGELOG.md") {
			t.Errorf("history file flagged: %+v", f)
		}
		lines = append(lines, f.Line)
	}
	if got, want := lines, []int{9, 12}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("finding lines = %v, want %v", got, want)
	}
}
