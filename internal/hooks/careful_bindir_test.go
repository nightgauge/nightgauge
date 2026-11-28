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
		{"heredoc to bash", "d=$(dirname \"$NIGHTGAUGE_BIN\"); bash <<EOF\ncp x $d/gh\nEOF", true},
		{"heredoc quoted delim to sh", "d=$(dirname \"$NIGHTGAUGE_BIN\"); sh <<'EOF'\ncp x $d/gh\nEOF", true},
		{"heredoc dash tabs", "d=$(dirname \"$NIGHTGAUGE_BIN\"); bash <<-EOF\n\tcp x \"$d/gh\"\n\tEOF", true},
		{"heredoc piped to bash", "d=$(dirname \"$NIGHTGAUGE_BIN\"); cat <<EOF | bash\ncp x $d/gh\nEOF", true},
		{"heredoc bash -s", "d=$(dirname \"$NIGHTGAUGE_BIN\"); bash -s <<EOF\ncp x $d/gh\nEOF", true},
		{"heredoc bash -", "d=$(dirname \"$NIGHTGAUGE_BIN\"); bash - <<EOF\ncp x $d/gh\nEOF", true},
		{"heredoc env bash", "d=$(dirname \"$NIGHTGAUGE_BIN\"); env bash <<EOF\ncp x $d/gh\nEOF", true},
		{"heredoc source stdin", "d=$(dirname \"$NIGHTGAUGE_BIN\"); source /dev/stdin <<EOF\ncp x $d/gh\nEOF", true},
		{"heredoc body redirection", "d=$(dirname \"$NIGHTGAUGE_BIN\"); bash <<EOF\necho x > $d/gh\nEOF", true},
		{"heredoc nested", "d=$(dirname \"$NIGHTGAUGE_BIN\"); bash <<A\nbash <<B\ncp x $d/gh\nB\nA", true},
		{"heredoc inside sh -c", "d=$(dirname \"$NIGHTGAUGE_BIN\"); sh -c 'bash <<E\ncp x '\"$d\"'/gh\nE'", true},
		{"heredoc literal dir", "bash <<EOF\ncp x " + dir + "/gh\nEOF", true},
		{"heredoc prose to cat", "d=$(dirname \"$NIGHTGAUGE_BIN\"); cat <<EOF\ncp x $d/gh is prose\nEOF", false},
		{"heredoc commit message", "d=$(dirname \"$NIGHTGAUGE_BIN\"); git commit -F - <<EOF\nrm $d/gh\nEOF", false},
		{"heredoc benign bash", "d=$(dirname \"$NIGHTGAUGE_BIN\"); bash <<EOF\necho hello\nEOF", false},
		{"heredoc bash script operand", "d=$(dirname \"$NIGHTGAUGE_BIN\"); bash script.sh <<EOF\ncp x $d/gh\nEOF", false},
		{"heredoc bash -c stdin data", "d=$(dirname \"$NIGHTGAUGE_BIN\"); bash -c 'echo hi' <<EOF\ncp x $d/gh\nEOF", false},
		{"heredoc in substitution rm", "rm $(cat <<EOF\nfoo\nEOF\n) " + dir + "/nightgauge", true},
		{"heredoc in substitution mv", "mv a $(cat <<EOF\nb\nEOF\n) " + dir + "/", true},
		{"heredoc bash unresolved operand", "d=$(dirname \"$NIGHTGAUGE_BIN\"); bash \"$(cat f)\" <<EOF\ncp x $d/gh\nEOF", true},
		{"heredoc bash --", "d=$(dirname \"$NIGHTGAUGE_BIN\"); bash -- <<EOF\ncp x $d/gh\nEOF", true},
		{"heredoc bash -- /dev/stdin", "d=$(dirname \"$NIGHTGAUGE_BIN\"); bash -- /dev/stdin <<EOF\ncp x $d/gh\nEOF", true},
		{"heredoc bash -o pipefail", "d=$(dirname \"$NIGHTGAUGE_BIN\"); bash -o pipefail <<EOF\ncp x $d/gh\nEOF", true},
		{"heredoc bash -x", "d=$(dirname \"$NIGHTGAUGE_BIN\"); bash -x <<EOF\ncp x $d/gh\nEOF", true},
		{"heredoc bash --norc", "d=$(dirname \"$NIGHTGAUGE_BIN\"); bash --norc <<EOF\ncp x $d/gh\nEOF", true},
		{"heredoc bash --rcfile", "d=$(dirname \"$NIGHTGAUGE_BIN\"); bash --rcfile /dev/null <<EOF\ncp x $d/gh\nEOF", true},
		{"heredoc dot fd0", "d=$(dirname \"$NIGHTGAUGE_BIN\"); . /dev/fd/0 <<EOF\ncp x $d/gh\nEOF", true},
		{"heredoc sh proc fd0", "d=$(dirname \"$NIGHTGAUGE_BIN\"); sh /proc/self/fd/0 <<EOF\ncp x $d/gh\nEOF", true},
		{"heredoc brace group", "d=$(dirname \"$NIGHTGAUGE_BIN\"); {\nbash\n} <<EOF\ncp x $d/gh\nEOF", true},
		{"heredoc outer cd flows in", "cd \"$(dirname \"$NIGHTGAUGE_BIN\")\"; bash <<EOF\ncp x gh\nEOF", true},
		{"heredoc body cd does not leak", "d=$(dirname \"$NIGHTGAUGE_BIN\"); bash <<EOF\ncd $d\nEOF\ncp x gh", false},
		{"heredoc body reassigns var", "d=$(dirname \"$NIGHTGAUGE_BIN\"); bash <<'EOF'\nd=/tmp\ncp x $d/gh\nEOF", false},
		{"heredoc later non-shell body", "d=$(dirname \"$NIGHTGAUGE_BIN\"); bash <<A\nA\ncat <<B\ncp x $d/gh\nB", false},
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

func TestBinDirHeredocReasonNamesVerb(t *testing.T) {
	bin, cwd := binDirFixture(t)
	r := binDirWriteReason("d=$(dirname \"$NIGHTGAUGE_BIN\"); bash <<EOF\ncp x $d/gh\nEOF", cwd, bin)
	if !strings.Contains(r, "cp writes") || !strings.Contains(r, "gh") {
		t.Errorf("reason = %q, want the body's verb and target", r)
	}
	deep := "true $NIGHTGAUGE_BIN\n"
	for i := 0; i < 7; i++ {
		deep += "bash <<E" + string(rune('0'+i)) + "\n"
	}
	deep += "echo hi"
	if r := binDirWriteReason(deep, cwd, bin); !strings.Contains(r, "nested script writes") {
		t.Errorf("deep nesting reason = %q, want a named verb", r)
	}
}

// TestCarefulGateBinDirWrappers covers wrapper options that take values or
// change the inner command: env -u/-C/-S, sudo -u/-D, timeout and chrt
// leading operands after `--`, and the stdin item xargs appends (#3650).
func TestCarefulGateBinDirWrappers(t *testing.T) {
	_, cwd := binDirFixture(t)
	const d = `d="$(dirname "$NIGHTGAUGE_BIN")"; `
	cases := []struct {
		cmd  string
		deny bool
	}{
		// env value options (AC1) and chdir/split (AC2).
		{`env -u X cp x "$d/gh"`, true},
		{`env -uX cp x "$d/gh"`, true},
		{`env --unset X cp x "$d/gh"`, true},
		{`env --unset=X cp x "$d/gh"`, true},
		{`env --uns X cp x "$d/gh"`, true},
		{`env -i -u X cp x "$d/gh"`, true},
		{`env -C /tmp cp x "$d/gh"`, true},
		{`env -C "$d" cp x gh`, true},
		{`env --chdir="$d" cp x gh`, true},
		{`env --chdir "$d" cp x gh`, true},
		{`env -C"$d" cp x gh`, true},
		{`env -C -L cp x "$d/gh"`, true},
		{`env -P /bin cp x "$d/gh"`, true},
		{`env -L u cp x "$d/gh"`, true},
		{`env -U u cp x "$d/gh"`, true},
		{`env -S "cp x $d/gh"`, true},
		{`env -S"cp x $d/gh"`, true},
		{`env --split-string="cp x $d/gh"`, true},
		{`env -S "cp x" "$d/gh"`, true},
		{`env -S "$(cat f)"`, true},
		{`env -C "$d" env -S 'cp x gh'`, true},
		{`env -- cp x "$d/gh"`, true},
		{`env --bogus-opt cp x "$d/gh"`, true},
		// sudo and the other wrappers.
		{`sudo -u root cp x "$d/gh"`, true},
		{`sudo -D "$d" cp x gh`, true},
		{`sudo --chdir="$d" cp x gh`, true},
		{`sudo env -C "$d" cp x gh`, true},
		{`sudo -u root -- cp x "$d/gh"`, true},
		{`doas -u root cp x "$d/gh"`, true},
		{`nice -n 5 cp x "$d/gh"`, true},
		{`nice --adjustment 5 cp x "$d/gh"`, true},
		{`ionice -c 2 -n 0 cp x "$d/gh"`, true},
		{`stdbuf --output L cp x "$d/gh"`, true},
		{`timeout -k 1 5 cp x "$d/gh"`, true},
		{`timeout --kill-after 1 5 cp x "$d/gh"`, true},
		{`time -o "$d/gh" true`, true},
		{`env -C "$d" time -o gh true`, true},
		// AC4: `--` still leaves the duration and the priority.
		{`timeout -- 5 cp x "$d/gh"`, true},
		{`chrt -- 10 cp x "$d/gh"`, true},
		{`timeout -s KILL -- 5 cp x "$d/gh"`, true},
		{`chrt -f -- 10 cp x "$d/gh"`, true},
		{`chrt -f 10 cp x "$d/gh"`, true},
		// AC3: the stdin item xargs appends.
		{`echo "$d/gh" | xargs cp x`, true},
		{`echo "$d/gh" | xargs rm`, true},
		{`echo "$d/gh" | xargs sed -i s/a/b/`, true},
		{`echo "$d" | xargs cp x -t`, true},
		{`echo "$d" | xargs cp x --target-directory`, true},
		{`echo "$d/gh" | xargs -n1 cp x`, true},
		{`echo "$d/gh" | xargs -d '\n' cp x`, true},
		{`echo "$d/gh" | xargs -I{} cp x {}`, true},
		{`echo "$d/gh" | xargs -i cp x {}`, true},
		{`echo "$d/gh" | xargs -iR cp x R`, true},
		{`echo "$d/gh" | xargs --replace=R cp x R`, true},
		{`echo cp | xargs -I{} {} x "$d/gh"`, true},
		{`echo cp | xargs --r {} x "$d/gh"`, true},
		{`echo "$d" | xargs -I{} env -C {} cp x gh`, true},
		{`echo "$d/gh" | xargs -I{} sh -c 'cp x {}'`, true},
		{`echo "cp x $d/gh" | xargs sh -c`, true},
		{`echo "$d/gh" | xargs env -S "cp x"`, true},
		{`echo "$d/gh" | xargs -es cp x`, true},
		{`echo "$d/gh" | xargs --max cp x`, true},
		{`echo "$d/gh" | xargs rsync -t x`, true},
		{`rsync -t x "$d/gh"`, true},
		{`echo "$d/gh" | sudo -u root xargs cp x`, true},
		// getopt_long abbreviations and sudo assignments.
		{`sudo --us root cp x "$d/gh"`, true},
		{`sudo --chd "$d" cp x gh`, true},
		{`sudo A=b cp x "$d/gh"`, true},
		{`nice --adj 5 cp x "$d/gh"`, true},
		{`stdbuf --out L cp x "$d/gh"`, true},
		{`ionice --cl 2 cp x "$d/gh"`, true},
		{`chrt --sched-run 5 -f 10 cp x "$d/gh"`, true},
		{`env time --out "$d/gh" true`, true},
		// env -S escapes are not shell: unresolved, so refused here.
		{`env -S 'cp\_x\_'"$d"'/gh'`, true},
		{`env -C "$d" env -S 'cp\_x\_gh'`, true},
		// Here-documents run by a shell behind a wrapper.
		{"env -C \"$d\" bash <<EOF\ncp x gh\nEOF", true},
		{"sudo -D \"$d\" bash <<EOF\ncp x gh\nEOF", true},
		{"env -S 'bash' <<EOF\ncp x $d/gh\nEOF", true},
		{"env -C \"$d\" env -S 'bash -s' <<EOF\ncp x gh\nEOF", true},
		{"xargs bash <<EOF\ncp x $d/gh\nEOF", true},
		// Allowed.
		{"env -C /tmp bash <<EOF\ncp x gh\nEOF", false},
		{`env --uns X ls`, false},
		{`env --ch /tmp cp x y`, false},
		{`sudo --preserve-env ls`, false},
		{`sudo --preserve-g ls`, false},
		{`sudo --pres cp x "$d/gh"`, true},
		{`env -C "$d" true; cp x gh`, false},
		{`time -o gh env -C "$d" true`, false},
		{`env A=b git status`, false},
		{`env -u X ls`, false},
		{`env -C /tmp cp x y`, false},
		{`env -C "$d" ls`, false},
		{`env -S "echo hi"`, false},
		{`timeout -- 5 ls`, false},
		{`chrt -- 10 ls`, false},
		{`timeout 5 cp x /tmp/y`, false},
		{`sudo -u root ls "$d"`, false},
		{`echo "$d" | xargs -I R cp x /tmp/y`, false},
		{`echo "$d" | xargs ls`, false},
	}
	for _, tc := range cases {
		cmd := d + tc.cmd
		t.Run(tc.cmd, func(t *testing.T) {
			got := EvaluateCarefulGate(carefulPayload(t, cwd, cmd))
			if (got.Decision == "block") != tc.deny {
				t.Fatalf("%s: deny=%v want %v (reason %q)", cmd, got.Decision == "block", tc.deny, got.Reason)
			}
		})
	}
	// No NIGHTGAUGE_BIN mention: an unresolved xargs item stays allowed (ADR-002).
	for _, cmd := range []string{
		`find . -name '*.o' | xargs rm`, `echo a | xargs cp x /tmp/y`, `echo a | xargs cp x`,
		`env -S "$(cat f)"`, `nice --frobnicate 5 ls`,
	} {
		if got := EvaluateCarefulGate(carefulPayload(t, cwd, cmd)); got.Decision == "block" {
			t.Errorf("%s: blocked without naming %s: %q", cmd, "NIGHTGAUGE_BIN", got.Reason)
		}
	}
}

// Every prefix wrapper the shared tokenizer knows has an option row, so the
// two lists cannot drift.
func TestWrapperOptsCoverPrefixWrappers(t *testing.T) {
	for name := range prefixWrappers {
		if _, ok := wrapperOpts[name]; !ok {
			t.Errorf("prefixWrappers has %q but wrapperOpts has no row", name)
		}
	}
}

// A deny behind a chdir clone or an env -S sub-run names the inner verb.
func TestBinDirWrapperReasonNamesVerb(t *testing.T) {
	bin, cwd := binDirFixture(t)
	for _, cmd := range []string{
		`d="$(dirname "$NIGHTGAUGE_BIN")"; env -C "$d" cp x gh`,
		`d="$(dirname "$NIGHTGAUGE_BIN")"; env -S "cp x $d/gh"`,
	} {
		if r := binDirWriteReason(cmd, cwd, bin); !strings.Contains(r, "cp writes") {
			t.Errorf("%s: reason = %q, want the inner cp", cmd, r)
		}
	}
}
