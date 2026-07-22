package hooks

import (
	"reflect"
	"strings"
	"testing"
)

func TestTokenizeQuotes(t *testing.T) {
	cases := []struct {
		cmd  string
		want []string
	}{
		{`git commit -m "fix push to main bug"`, []string{"git", "commit", "-m", "fix push to main bug"}},
		{`echo 'single quoted text'`, []string{"echo", "single quoted text"}},
		{`echo $'ansi\tquoted'`, []string{"echo", "ansi\\tquoted"}},
		{`git push origin feat/42`, []string{"git", "push", "origin", "feat/42"}},
		{`gh pr create --base main --title "x"`, []string{"gh", "pr", "create", "--base", "main", "--title", "x"}},
		{`echo "embedded \"quote\" here"`, []string{"echo", `embedded "quote" here`}},
	}
	for _, tc := range cases {
		got := tokenize(tc.cmd)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("tokenize(%q) = %#v, want %#v", tc.cmd, got, tc.want)
		}
	}
}

func TestSplitSegments(t *testing.T) {
	segs := SplitSegments(`git status && git push origin main || echo "done"; ls | grep x`)
	wantArgv0 := []string{"git", "git", "echo", "ls", "grep"}
	if len(segs) != len(wantArgv0) {
		t.Fatalf("got %d segments, want %d: %#v", len(segs), len(wantArgv0), segs)
	}
	for i, seg := range segs {
		if len(seg.Argv) == 0 || seg.Argv[0] != wantArgv0[i] {
			t.Errorf("segment %d argv[0] = %#v, want %q", i, seg.Argv, wantArgv0[i])
		}
	}
}

func TestCommandArgvStripsEnvPrefix(t *testing.T) {
	segs := SplitSegments(`GH_TOKEN=abc FOO=bar git push origin main`)
	if len(segs) != 1 {
		t.Fatalf("want 1 segment, got %d", len(segs))
	}
	argv := segs[0].CommandArgv()
	if len(argv) < 2 || argv[0] != "git" || argv[1] != "push" {
		t.Errorf("CommandArgv = %#v, want [git push ...]", argv)
	}
}

func TestStrippedTextRemovesQuotedProse(t *testing.T) {
	cases := []struct {
		cmd        string
		mustNot    string // substring that must be gone
		mayContain string // substring that must remain
	}{
		{`echo "rm -rf /"`, "rm -rf /", "echo"},
		{`git commit -m "reset --hard cleanup"`, "reset --hard", "git commit"},
		{`gh issue comment --body 'we should drop table'`, "drop table", "gh issue comment"},
		{`rm -rf /tmp/x`, "", "rm -rf /tmp/x"},
	}
	for _, tc := range cases {
		got := StrippedText(tc.cmd)
		if tc.mustNot != "" && strings.Contains(got, tc.mustNot) {
			t.Errorf("StrippedText(%q) = %q still contains %q", tc.cmd, got, tc.mustNot)
		}
		if tc.mayContain != "" && !strings.Contains(got, tc.mayContain) {
			t.Errorf("StrippedText(%q) = %q lost %q", tc.cmd, got, tc.mayContain)
		}
	}
}

func TestStripHeredocBody(t *testing.T) {
	cmd := "cat <<EOF > out.txt\ngit push origin main\nrm -rf /\nEOF\necho done"
	stripped := StrippedText(cmd)
	if strings.Contains(stripped, "git push origin main") || strings.Contains(stripped, "rm -rf /") {
		t.Errorf("heredoc body should be removed, got %q", stripped)
	}
	if !strings.Contains(stripped, "echo done") {
		t.Errorf("post-heredoc command lost, got %q", stripped)
	}
	// The heredoc body must not produce git/push segments either.
	for _, seg := range SplitSegments(cmd) {
		argv := seg.CommandArgv()
		if len(argv) >= 2 && argv[0] == "git" && argv[1] == "push" {
			t.Errorf("heredoc body produced a git push segment: %#v", argv)
		}
	}
}

func TestHeredocQuotedDelimAndDash(t *testing.T) {
	cmd := "cat <<-'EOF'\n\tsecret push to main\n\tEOF\ntrue"
	stripped := StrippedText(cmd)
	if strings.Contains(stripped, "secret push to main") {
		t.Errorf("<<- quoted heredoc body should be removed, got %q", stripped)
	}
}

func TestHeredocBackslashAndEmbeddedQuoteDelim(t *testing.T) {
	// bash treats <<\EOF and <<E'O'F as delimiter EOF; the body must be removed
	// and a trailing real command must survive (and be parseable).
	for _, cmd := range []string{
		"cat <<\\EOF > x\nbody line\nEOF\ngit push origin main",
		"cat <<E'O'F\nbody line\nEOF\ngit push origin main",
	} {
		stripped := StrippedText(cmd)
		if strings.Contains(stripped, "body line") {
			t.Errorf("heredoc body not removed for %q: %q", cmd, stripped)
		}
		var sawPush bool
		for _, seg := range SplitSegments(cmd) {
			a := seg.CommandArgv()
			if len(a) >= 2 && a[0] == "git" && a[1] == "push" {
				sawPush = true
			}
		}
		if !sawPush {
			t.Errorf("trailing `git push` swallowed for %q", cmd)
		}
	}
}

func TestExpandWrappers(t *testing.T) {
	cases := []struct {
		cmd         string
		wantGitVerb string
	}{
		{"bash -c 'git push origin main'", "push"},
		{`sh -c "git reset --hard"`, "reset"},
		{"sudo git push origin main", "push"},
		{"env A=b git push origin main", "push"},
		{"xargs git push origin main", "push"},
		{"timeout 5 git reset --hard HEAD", "reset"},
	}
	for _, tc := range cases {
		segs := ExpandWrappers(SplitSegments(tc.cmd))
		var found bool
		for _, seg := range segs {
			a := seg.CommandArgv()
			if len(a) >= 2 && a[0] == "git" && a[1] == tc.wantGitVerb {
				found = true
			}
		}
		if !found {
			t.Errorf("ExpandWrappers(%q) did not surface git %s; segs=%+v", tc.cmd, tc.wantGitVerb, segs)
		}
	}
}

func TestParserNoPanicOnMalformed(t *testing.T) {
	// Must never panic on adversarial/malformed input.
	for _, cmd := range []string{
		"", "'", `"`, "$'", "\\", "cat <<", "cat <<EOF", "git push 'main",
		"((((", "))))", "`", "| | |", "&& &&", "<<-", "$( ", "bash -c",
		"env", "sudo", "git -c", "git -C",
	} {
		_ = ExpandWrappers(SplitSegments(cmd))
		_ = StrippedText(cmd)
		_ = Pipelines(SplitSegments(cmd))
	}
}
