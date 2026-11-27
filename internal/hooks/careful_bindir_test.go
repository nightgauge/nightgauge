package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/careful"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

func carefulPayload(t *testing.T, cwd, cmd string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"tool_name": "Bash", "cwd": cwd,
		"tool_input": map[string]string{"command": cmd},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// binDirFixture makes a bin dir holding a nightgauge binary and a cwd outside
// it; the careful lock is never set, so every deny is the bin-dir rule.
func binDirFixture(t *testing.T) (bin, cwd string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(dir, "nightgauge")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	cwd = filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NIGHTGAUGE_ADAPTER", "opencode")
	t.Setenv("NIGHTGAUGE_BIN", bin)
	return bin, cwd
}

func TestCarefulGateBinDirRuntime(t *testing.T) {
	bin, cwd := binDirFixture(t)
	dir := filepath.Dir(bin)
	cases := []struct {
		name string
		cmd  string
		deny bool
	}{
		{"AC1 var-assembled target", `d="$(dirname "$NIGHTGAUGE_BIN")"; cp x "$d/gh"`, true},
		{"AC2 quote-split target", `d="$(dirname "$NIGHTGAUGE_BIN")"; cp x "$d"'/gh'`, true},
		{"AC3 relative after cd", `cd "$(dirname "$NIGHTGAUGE_BIN")" && cp x gh`, true},
		{"AC3 cd pwd substitution", `cd "$(dirname "$NIGHTGAUGE_BIN")"; cp x "$(pwd)/gh"`, true},
		{"literal absolute target", `cp x ` + dir + `/gh`, true},
		{"backslash verb", `d=$(dirname $NIGHTGAUGE_BIN); \cp x $d/gh`, true},
		{"path verb with braces", `/bin/mv ${NIGHTGAUGE_BIN} /tmp/y`, true},
		{"rm the binary", `rm -f "$NIGHTGAUGE_BIN"`, true},
		{"redirection", `d=$(dirname "$NIGHTGAUGE_BIN"); echo hi > "$d/gh"`, true},
		{"fd redirection", `echo hi 2>> "$(dirname "$NIGHTGAUGE_BIN")/log"`, true},
		{"sh -c", `sh -c 'cp x "$(dirname "$NIGHTGAUGE_BIN")/gh"'`, true},
		{"export then tee", `export D="$(dirname "$NIGHTGAUGE_BIN")"; echo x | tee "$D/gh"`, true},
		{"cp -t", `cp -t "$(dirname "$NIGHTGAUGE_BIN")" x`, true},
		{"dd of=", `dd if=x of="$(dirname "$NIGHTGAUGE_BIN")/gh"`, true},
		{"curl -o", `curl -o "$(dirname "$NIGHTGAUGE_BIN")/gh" https://x`, true},
		{"wget into cwd", `cd "$(dirname "$NIGHTGAUGE_BIN")" && wget https://x/gh`, true},
		{"sudo prefix", `sudo cp x "$(dirname "$NIGHTGAUGE_BIN")/gh"`, true},
		{"AC4 write elsewhere", `cp x /tmp/x`, false},
		{"AC4 copy the binary out", `cp "$NIGHTGAUGE_BIN" /tmp/copy`, false},
		{"AC4 relative write in cwd", `cp a b && echo ok > out.txt`, false},
		{"AC4 unresolved untainted", `cp x "$(mktemp -d)/y"`, false},
		{"AC5 list bin dir", `ls "$(dirname "$NIGHTGAUGE_BIN")"`, false},
		{"AC5 run the binary", `"$NIGHTGAUGE_BIN" doctor`, false},
		{"AC5 run and read stderr", `"$NIGHTGAUGE_BIN" doctor 2>&1 | head`, false},
		{"AC5 quoted prose", `echo "cp x into the bin dir"`, false},
		{"AC7 unresolved tainted substitution", `cp x "$(cat f)$NIGHTGAUGE_BIN"`, true},
		{"AC7 cd to unresolved tainted var", `NG_UNKNOWN="$(foo "$NIGHTGAUGE_BIN")"; cd "$NG_UNKNOWN" && cp x gh`, true},
		{"AC7 unresolved sh -c script", `sh -c "$(printf %s "$NIGHTGAUGE_BIN")"`, true},
		{"reserved word !", `! cp x "$(dirname "$NIGHTGAUGE_BIN")/gh"`, true},
		{"brace group", `{ cp x "$(dirname "$NIGHTGAUGE_BIN")/gh"; }`, true},
		{"for loop body", `for f in a; do cp x "$(dirname "$NIGHTGAUGE_BIN")/gh"; done`, true},
		{"if body", `if true; then cp x "$(dirname "$NIGHTGAUGE_BIN")/gh"; fi`, true},
		{"function body", `f() { cp x "$(dirname "$NIGHTGAUGE_BIN")/gh"; }; f`, true},
		{"unresolved verb", `v=$(echo cp); $v x "$(dirname "$NIGHTGAUGE_BIN")/gh"`, true},
		{"sed -i", `sed -i s/a/b/ "$(dirname "$NIGHTGAUGE_BIN")/gh"`, true},
		{"cp --target-directory=", `cp --target-directory="$(dirname "$NIGHTGAUGE_BIN")" x`, true},
		{"curl -O into cwd", `cd "$(dirname "$NIGHTGAUGE_BIN")" && curl -O https://x/gh`, true},
		{"wget -P", `wget -P "$(dirname "$NIGHTGAUGE_BIN")" https://x/gh`, true},
		{"curl -sSLo bundled", `curl -sSLo "$(dirname "$NIGHTGAUGE_BIN")/gh" https://x`, true},
		{"curl -sSLo via var", `d="$(dirname "$NIGHTGAUGE_BIN")"; curl -sSLo "$d/gh" https://x`, true},
		{"wget -qO bundled", `wget -qO "$(dirname "$NIGHTGAUGE_BIN")/gh" https://x`, true},
		{"curl -sO into cwd", `cd "$(dirname "$NIGHTGAUGE_BIN")" && curl -sO https://x/gh`, true},
		{"curl -Lo in-token", `curl -Lo"$(dirname "$NIGHTGAUGE_BIN")/gh" https://x`, true},
		{"wget -qP bundled", `wget -qP "$(dirname "$NIGHTGAUGE_BIN")" https://x/gh`, true},
		{"wget -qO in-token", `wget -qO"$(dirname "$NIGHTGAUGE_BIN")/gh" https://x`, true},
		{"curl -sSL no output", `curl -sSL https://x`, false},
		{"curl -sSLo tmp", `curl -sSLo /tmp/gh https://x`, false},
		{"wget -qO tmp", `wget -qO /tmp/gh https://x`, false},
		{"wget -qP tmp", `wget -qP /tmp https://x/gh`, false},
		{"curl -do is -d value", `curl -do "$NIGHTGAUGE_BIN" https://x`, false},
		{"curl -sH value skipped", `curl -sH "X: y" -o /tmp/gh https://x`, false},
		{"curl -sH swallows -o", `curl -sH -o "$(dirname "$NIGHTGAUGE_BIN")/gh"`, false},
		{"wget -eO is -e value", `wget -eO "$NIGHTGAUGE_BIN" https://x`, false},
		{"wget -qe swallows -O", `wget -qe -O "$(dirname "$NIGHTGAUGE_BIN")/gh" -P /tmp https://x`, false},
		{"ln one operand in bin dir", `cd "$(dirname "$NIGHTGAUGE_BIN")"; ln -s /x`, true},
		{"backtick substitution", "cp x `dirname $NIGHTGAUGE_BIN`/gh", true},
		{"default expansion", `cp x "${NG_NOPE:-$(dirname "$NIGHTGAUGE_BIN")}/gh"`, true},
		{"unterminated substitution does not panic", `echo $(`, false},
		{"dev null after cd", `cd "$(dirname "$NIGHTGAUGE_BIN")" && echo x > /dev/null && cp x /dev/null`, false},
		{"fd duplication after cd", `cd "$(dirname "$NIGHTGAUGE_BIN")" && ls 2>&1 >&2`, false},
		{"cd back out", `cd "$(dirname "$NIGHTGAUGE_BIN")" && cd /tmp && cp x gh`, false},
		{"sed without -i", `sed s/a/b/ "$NIGHTGAUGE_BIN"`, false},
		{"eval tainted", `eval "cp x $(dirname "$NIGHTGAUGE_BIN")/gh"`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := EvaluateCarefulGate(carefulPayload(t, cwd, tc.cmd))
			if got := d.Decision == "block"; got != tc.deny {
				t.Fatalf("%s: deny=%v want %v (reason %q)", tc.cmd, got, tc.deny, d.Reason)
			}
			if tc.deny && !strings.Contains(d.Reason, "OpenCode bin-dir guard") {
				t.Errorf("reason %q lacks the bin-dir guard prefix", d.Reason)
			}
		})
	}
}

func TestCarefulGateBinDirSymlinkedDir(t *testing.T) {
	bin, cwd := binDirFixture(t)
	link := filepath.Join(filepath.Dir(cwd), "link")
	if err := os.Symlink(filepath.Dir(bin), link); err != nil {
		t.Fatal(err)
	}
	d := EvaluateCarefulGate(carefulPayload(t, cwd, "cp x "+link+"/gh"))
	if d.Decision != "block" {
		t.Fatalf("write through a symlink to the bin dir allowed: %+v", d)
	}
}

func TestCarefulGateBinDirInactiveOutsideOpenCode(t *testing.T) {
	_, cwd := binDirFixture(t)
	t.Setenv("NIGHTGAUGE_ADAPTER", "claude")
	d := EvaluateCarefulGate(carefulPayload(t, cwd, `cp x "$(dirname "$NIGHTGAUGE_BIN")/gh"`))
	if d.Decision == "block" {
		t.Fatalf("rule fired outside OpenCode: %q", d.Reason)
	}
}

func TestCarefulGateBinDirRelativeBinInactive(t *testing.T) {
	_, cwd := binDirFixture(t)
	t.Setenv("NIGHTGAUGE_BIN", "bin/nightgauge")
	d := EvaluateCarefulGate(carefulPayload(t, cwd, `cp x "$(cat f)$NIGHTGAUGE_BIN"`))
	if d.Decision == "block" {
		t.Fatalf("rule fired for a relative NIGHTGAUGE_BIN: %q", d.Reason)
	}
}

// With the careful lock held in an OpenCode stage both rules still apply: a
// bin-dir write gets the bin-dir reason and a destructive command the careful
// reason.
func TestCarefulGateBinDirWithCarefulLock(t *testing.T) {
	binDirFixture(t)
	root := layouttest.Repo(t)
	if err := careful.Enable(root, 0, ""); err != nil {
		t.Fatal(err)
	}
	d := EvaluateCarefulGate(carefulPayload(t, root, `cp x "$(dirname "$NIGHTGAUGE_BIN")/gh"`))
	if d.Decision != "block" || !strings.Contains(d.Reason, "OpenCode bin-dir guard") {
		t.Fatalf("bin-dir write under careful lock: %+v", d)
	}
	d = EvaluateCarefulGate(carefulPayload(t, root, "docker compose down -v"))
	if d.Decision != "block" || !strings.Contains(d.Reason, "/careful is ON") {
		t.Fatalf("destructive command under careful lock: %+v", d)
	}
}
