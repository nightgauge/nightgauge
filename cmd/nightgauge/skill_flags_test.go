package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// #2066: skills call `nightgauge` verbs with flags the binary does not have
// (`forge repo view -q`, `forge issue view --jq`), behind `2>/dev/null`, so the
// failure is an empty variable rather than an error. Resolving every
// invocation in a skill's shell against the real cobra tree catches the whole
// class, not only the call sites someone happened to find.

// reInvocation finds `nightgauge` in command position: at the start of a
// line or after a shell operator, `$(`, or a backtick. A `nightgauge` inside a
// word (`nightgauge-vscode`, `nightgauge:baseline`) is not an invocation.
var reInvocation = regexp.MustCompile("(?:^|[\\s;|&(`]|\\$\\()nightgauge[ \\t]+")

// reSubcommandWord is a word shaped like a subcommand name; a placeholder or
// prose ("…", "<name>") in that position is not a claim about the tree.
var reSubcommandWord = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// reFence opens or closes a fenced code block.
var reFence = regexp.MustCompile("^\\s*(```|~~~)")

// skillInvocation is one `nightgauge …` command found in a skill.
type skillInvocation struct {
	file string
	line int
	args []string
}

// shellWords splits the command after `nightgauge` into words, stopping at
// the first operator, redirection, substitution close or comment: the words
// after that belong to another command.
func shellWords(s string) []string {
	var words []string
	var cur strings.Builder
	inWord := false
	quote := byte(0)
	flush := func() {
		if inWord {
			words = append(words, cur.String())
		}
		cur.Reset()
		inWord = false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
			continue
		}
		switch {
		case c == '\'' || c == '"':
			// A quote opens a value at the start of a word or after `=`
			// (`--body="a b"`). Anywhere else it closes an enclosing
			// string, and the command ends with it.
			if inWord && s[i-1] != '=' {
				flush()
				return words
			}
			quote = c
			inWord = true
		case c == '\\':
			flush()
			return words // an escaped backtick closing a Markdown code span
		case c == ' ' || c == '\t':
			flush()
		case c == '<' && i+1 < len(s) && (s[i+1] >= 'a' && s[i+1] <= 'z' || s[i+1] >= 'A' && s[i+1] <= 'Z') &&
			strings.IndexByte(s[i:], '>') > 0 && !strings.ContainsAny(s[i:i+strings.IndexByte(s[i:], '>')], " \t"):
			// A `<number>` placeholder is a word, not a redirection.
			end := i + strings.IndexByte(s[i:], '>')
			cur.WriteString(s[i : end+1])
			inWord = true
			i = end
		case strings.IndexByte("|;&)<>`", c) >= 0:
			flush()
			return words
		case c == '#' && !inWord:
			return words
		case c >= '0' && c <= '9' && !inWord && i+1 < len(s) && s[i+1] == '>':
			return words // 2>/dev/null
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	flush()
	return words
}

// skillInvocations returns every `nightgauge` invocation inside a fenced code
// block under root. Continuation lines are joined first so a flag on the next
// line is still seen.
func skillInvocations(t *testing.T, root string) []skillInvocation {
	t.Helper()
	var out []skillInvocation
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		inFence, n, start := false, 0, 0
		var joined strings.Builder
		for sc.Scan() {
			n++
			line := sc.Text()
			if reFence.MatchString(line) {
				inFence = !inFence
				joined.Reset()
				continue
			}
			if !inFence {
				continue
			}
			if joined.Len() == 0 {
				start = n
			}
			if strings.HasSuffix(line, "\\") {
				joined.WriteString(strings.TrimSuffix(line, "\\") + " ")
				continue
			}
			joined.WriteString(line)
			full := strings.TrimPrefix(strings.TrimSpace(joined.String()), "$ ")
			joined.Reset()
			if strings.HasPrefix(full, "#") {
				continue
			}
			for _, loc := range reInvocation.FindAllStringIndex(full, -1) {
				// Inside a quoted string (an echo'd hint, a jq literal) the
				// words are prose, unless a `$(` makes them a command.
				before := full[:loc[0]]
				if !strings.HasPrefix(full[loc[0]:], "$(") && !strings.HasSuffix(before, "$(") &&
					(strings.Count(before, "\"")%2 == 1 || strings.Count(before, "'")%2 == 1) {
					continue
				}
				out = append(out, skillInvocation{file: path, line: start, args: shellWords(full[loc[1]:])})
			}
		}
		return sc.Err()
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// unknownFlags reports the flags in args that cmd (resolved from args by
// root.Find) does not define, locally or inherited. It checks nothing for an
// invocation that does not resolve past the root, or whose command parses
// no flags of its own.
func unknownFlags(root *cobra.Command, args []string) (string, []string) {
	cmd, rest, err := root.Find(args)
	if err != nil || cmd == root || cmd.DisableFlagParsing {
		return "", nil
	}
	// A group command left holding a positional word was asked for a
	// subcommand it does not have: `forge api`, `project audit`.
	if cmd.HasSubCommands() {
		for _, a := range rest {
			if strings.HasPrefix(a, "-") {
				break
			}
			if reSubcommandWord.MatchString(a) {
				return cmd.CommandPath(), []string{"subcommand " + a}
			}
		}
	}
	known := func(name string, short bool) bool {
		if name == "help" || (short && name == "h") {
			return true
		}
		if short {
			return cmd.Flags().ShorthandLookup(name) != nil || cmd.InheritedFlags().ShorthandLookup(name) != nil
		}
		return cmd.Flags().Lookup(name) != nil || cmd.InheritedFlags().Lookup(name) != nil
	}
	var bad []string
	for _, a := range args {
		if a == "--" {
			break
		}
		if strings.ContainsAny(a, "$<{[") || !strings.HasPrefix(a, "-") || a == "-" {
			continue
		}
		if strings.HasPrefix(a, "--") {
			name := strings.SplitN(a[2:], "=", 2)[0]
			if !known(name, false) {
				bad = append(bad, a)
			}
			continue
		}
		if len(a) >= 2 && (a[1] < '0' || a[1] > '9') && !known(a[1:2], true) {
			bad = append(bad, a)
		}
	}
	return cmd.CommandPath(), bad
}

func TestSkillsPassOnlyFlagsTheBinaryDefines(t *testing.T) {
	root := rootCmd()
	var failures []string
	checked := 0
	for _, inv := range skillInvocations(t, filepath.Join("..", "..", "skills")) {
		path, bad := unknownFlags(root, inv.args)
		if path == "" {
			continue
		}
		checked++
		if len(bad) > 0 {
			rel, _ := filepath.Rel(filepath.Join("..", ".."), inv.file)
			failures = append(failures, fmt.Sprintf("%s:%d: `%s` has no %s", rel, inv.line, path, strings.Join(bad, ", ")))
		}
	}
	if checked < 50 {
		t.Fatalf("HARNESS ERROR: only %d skill invocations resolved; the scanner is not reading the skills", checked)
	}
	sort.Strings(failures)
	for _, f := range failures {
		t.Error(f)
	}
}
