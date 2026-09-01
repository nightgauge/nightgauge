package testexec

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nightgauge/nightgauge/internal/scan"
)

// ResolvedCommand is the repo's test command as the pipeline would actually
// run it, together with the invocation lines it expands to.
//
// Command and Invocations are separate because they answer different
// questions. `make test` is what a human runs and what the config declares;
// `flutter test --exclude-tags=app-e2e` is where the exclusion lives. A
// detector handed only the former sees no flags at all and reports nothing —
// which is precisely the shape of the bug this package exists to catch, so the
// expansion is not a convenience.
type ResolvedCommand struct {
	// Command is the declared entry point ("make test", "npm test", …).
	Command string
	// Source names where it came from: "config" for an explicit
	// pipeline.test_execution.command, otherwise scan's own source string.
	Source string
	// Invocations are the concrete command lines Command expands to, in file
	// order. When no expansion is possible this is just [Command].
	Invocations []string
	// Warnings records anything noticed during resolution. Never fatal.
	Warnings []string
}

// Resolved reports whether a usable command was found. An unresolvable command
// is not an error and not a finding — it is simply no evidence, and no evidence
// must never read as an accusation.
func (c ResolvedCommand) Resolved() bool { return strings.TrimSpace(c.Command) != "" }

// Text is every invocation joined, for flag scanning.
func (c ResolvedCommand) Text() string { return strings.Join(c.Invocations, "\n") }

// ResolveTestCommand determines the effective test command for a repo.
//
// Precedence: an explicit configured command, then whatever the repo itself
// declares (scan.SelectTestCommand), then a Flutter fallback that scan does not
// model. Each is re-derived per run: a repo that adds or removes an exclusion
// is re-evaluated on its next run rather than grandfathered against a value
// captured when the gate first saw it.
func ResolveTestCommand(repoRoot, configured string) ResolvedCommand {
	out := ResolvedCommand{}
	if s := strings.TrimSpace(configured); s != "" {
		out.Command = s
		out.Source = "config"
	} else if res, err := scan.SelectTestCommand(context.Background(), scan.TestCommandOptions{Workdir: repoRoot}); err == nil && res != nil {
		out.Command = res.Command
		out.Source = res.Source
		out.Warnings = append(out.Warnings, res.Warnings...)
	}
	if !out.Resolved() {
		// scan has no Dart branch: a Flutter package declares neither a
		// package.json test script nor a Makefile target, so it falls off the
		// end of scan's precedence chain entirely. Without this the detector
		// most needed here never gets a command to read.
		if fileExists(repoRoot, "pubspec.yaml") {
			out.Command = "flutter test"
			out.Source = "framework_fallback"
		}
	}
	if !out.Resolved() {
		return out
	}
	out.Invocations = expandInvocations(repoRoot, out.Command)
	return out
}

// makeTargetRe matches a `test:` Makefile target header.
var makeTargetRe = regexp.MustCompile(`^test\s*:`)

// expandInvocations resolves an indirect entry point down to the command lines
// it runs. One level only, and deliberately so: a script that shells out to
// another script is where a mechanical expander starts guessing, and a wrong
// expansion produces a false accusation, which is worse here than no finding.
func expandInvocations(repoRoot, command string) []string {
	fields := strings.Fields(command)
	if len(fields) >= 2 {
		switch {
		case fields[0] == "make":
			if lines := makefileRecipe(repoRoot, fields[1]); len(lines) > 0 {
				return lines
			}
		case fields[1] == "test" && (fields[0] == "npm" || fields[0] == "yarn" || fields[0] == "pnpm" || fields[0] == "bun"):
			if body := packageJSONScript(repoRoot, "test"); body != "" {
				return []string{body}
			}
		}
	}
	return []string{command}
}

// makefileRecipe returns the recipe lines of a Makefile target, with the
// leading tab and any @/- prefix removed.
func makefileRecipe(repoRoot, target string) []string {
	data, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		return nil
	}
	var (
		out    []string
		inside bool
		header = regexp.MustCompile(`^` + regexp.QuoteMeta(target) + `\s*:`)
	)
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "\t") {
			if inside {
				out = append(out, strings.TrimLeft(strings.TrimPrefix(line, "\t"), "@-"))
			}
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		inside = header.MatchString(trimmed) || (target == "test" && makeTargetRe.MatchString(trimmed))
	}
	return out
}

// packageJSONScript returns the body of one npm script, or "".
func packageJSONScript(repoRoot, name string) string {
	data, err := os.ReadFile(filepath.Join(repoRoot, "package.json"))
	if err != nil {
		return ""
	}
	var top struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &top); err != nil {
		return ""
	}
	return strings.TrimSpace(top.Scripts[name])
}

func fileExists(dir, name string) bool {
	st, err := os.Stat(filepath.Join(dir, name))
	return err == nil && !st.IsDir()
}
