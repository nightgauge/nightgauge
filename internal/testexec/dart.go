package testexec

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// DartDetector recognises Dart and Flutter suites the configured `flutter test`
// / `dart test` invocation does not execute.
//
// Three mechanisms, in the order they are checked:
//
//  1. the file declares a tag the command excludes (`--exclude-tags`, `-x`);
//  2. the command restricts to a tag set (`--tags`, `-t`) the file does not
//     satisfy — including a file that declares no tags at all;
//  3. the command names explicit path targets and the file is under none.
//
// The third hid the downstream app's suites as thoroughly as the first:
// `flutter test` with no path argument runs `test/` and nothing else, so a file
// under `integration_test/` is unreachable no matter what it is tagged.
type DartDetector struct{}

// Name implements Detector.
func (DartDetector) Name() string { return "dart" }

// tagsAnnotationRe matches a file-level `@Tags([...])` annotation. Dart allows
// only one, and only above the library/import block, but this deliberately does
// not enforce position: a misplaced annotation is a Dart error the analyzer
// already reports, and re-litigating it here would only produce a second,
// worse message.
var tagsAnnotationRe = regexp.MustCompile(`(?s)@Tags\s*\(\s*\[(.*?)\]\s*\)`)

// tagsAnnotationOpenRe matches the start of a `@Tags(` annotation whose
// argument list this parser could not read to a close bracket. It exists only
// so a malformed annotation produces a warning instead of silence — a file
// whose tags could not be read is treated as untagged, never as excluded.
var tagsAnnotationOpenRe = regexp.MustCompile(`@Tags\s*\(`)

// dartStringRe matches a single- or double-quoted Dart string literal.
var dartStringRe = regexp.MustCompile(`'([^']*)'|"([^"]*)"`)

// Detect implements Detector.
func (d DartDetector) Detect(repoRoot string, cmd ResolvedCommand, files []string) ([]Exclusion, []string) {
	dartFiles := filterDart(files)
	if len(dartFiles) == 0 || !cmd.Resolved() {
		return nil, nil
	}
	runner, runnerLine := dartRunnerLine(cmd)
	if runner == "" {
		// The repo has Dart test files but its test command is not a Dart test
		// runner. That is a legitimate multi-language repo, not a finding.
		return nil, nil
	}

	excludeTags := flagValues(runnerLine, "exclude-tags", "x")
	includeTags := flagValues(runnerLine, "tags", "t")
	targets := pathTargets(runnerLine)

	var (
		out      []Exclusion
		warnings []string
	)
	for _, f := range dartFiles {
		tags, warn := dartFileTags(repoRoot, f)
		if warn != "" {
			warnings = append(warnings, warn)
		}

		if hit := intersect(tags, excludeTags); hit != "" {
			out = append(out, Exclusion{
				Path:        f,
				Detector:    d.Name(),
				Mechanism:   MechanismExcludedTag,
				Evidence:    fmt.Sprintf("command passes --exclude-tags=%s; %s declares @Tags(['%s'])", strings.Join(excludeTags, ","), f, hit),
				Remediation: fmt.Sprintf("%s --tags=%s %s", runner, hit, f),
			})
			continue
		}
		if len(includeTags) > 0 && intersect(tags, includeTags) == "" {
			declared := "no @Tags annotation"
			if len(tags) > 0 {
				declared = fmt.Sprintf("@Tags(['%s'])", strings.Join(tags, "','"))
			}
			out = append(out, Exclusion{
				Path:        f,
				Detector:    d.Name(),
				Mechanism:   MechanismTagFilter,
				Evidence:    fmt.Sprintf("command restricts to --tags=%s; %s has %s", strings.Join(includeTags, ","), f, declared),
				Remediation: fmt.Sprintf("%s %s", runner, f),
			})
			continue
		}
		if outsideTargets(f, targets, runner) {
			out = append(out, Exclusion{
				Path:        f,
				Detector:    d.Name(),
				Mechanism:   MechanismOutsidePaths,
				Evidence:    fmt.Sprintf("command runs %s; %s is under none of them", describeTargets(targets, runner), f),
				Remediation: dartPathRemediation(runner, f, tags),
			})
		}
	}
	return out, warnings
}

// filterDart keeps the Dart test files out of a change's file set. A
// non-test Dart file is irrelevant here: this package answers "you built a test
// and nothing runs it", not "your code is uncovered".
func filterDart(files []string) []string {
	var out []string
	for _, f := range files {
		clean := path.Clean(strings.ReplaceAll(f, "\\", "/"))
		if path.Ext(clean) != ".dart" {
			continue
		}
		if !strings.HasSuffix(strings.TrimSuffix(path.Base(clean), ".dart"), "_test") {
			continue
		}
		out = append(out, clean)
	}
	return out
}

// dartRunnerLine finds the invocation line that is a Dart test run, and the
// runner prefix to use in a remediation. Returns ("", "") when the command
// never invokes one.
func dartRunnerLine(cmd ResolvedCommand) (runner, line string) {
	for _, inv := range cmd.Invocations {
		l := strings.TrimSpace(inv)
		switch {
		case strings.Contains(l, "flutter test"):
			return "flutter test", l
		case strings.Contains(l, "dart test"):
			return "dart test", l
		}
	}
	return "", ""
}

// flagValues collects every value given to a flag across its long and short
// spellings, in `--flag=v`, `--flag v` and `-x v` forms.
func flagValues(line, long, short string) []string {
	fields := strings.Fields(line)
	var out []string
	add := func(raw string) {
		for _, t := range splitTagExpression(raw) {
			out = appendUnique(out, t)
		}
	}
	for i, f := range fields {
		switch {
		case strings.HasPrefix(f, "--"+long+"="):
			add(strings.TrimPrefix(f, "--"+long+"="))
		case f == "--"+long, short != "" && f == "-"+short:
			if i+1 < len(fields) {
				add(fields[i+1])
			}
		case short != "" && strings.HasPrefix(f, "-"+short) && len(f) > len(short)+1:
			add(f[len(short)+1:])
		}
	}
	return out
}

// pathTargets returns the positional path arguments of an invocation — the
// arguments that are neither the runner itself nor a flag or a flag's value.
//
// Scanning starts AFTER the runner's own `test` subcommand rather than
// skipping every field spelled "test". `flutter test test integration_test`
// names `test/` as a real target, and a parser that drops it reports the whole
// unit suite as unreachable — a false accusation on the most ordinary
// invocation there is, which is the one way this gate could do real damage.
func pathTargets(line string) []string {
	fields := strings.Fields(line)
	start := 0
	for i := 0; i+1 < len(fields); i++ {
		if (fields[i] == "flutter" || fields[i] == "dart" || strings.HasSuffix(fields[i], "/flutter") || strings.HasSuffix(fields[i], "/dart")) && fields[i+1] == "test" {
			start = i + 2
			break
		}
	}
	var (
		out      []string
		skipNext bool
	)
	// Flags whose value is a separate argument. Anything not listed is assumed
	// to be a boolean flag; a wrong guess here can only ADD a positional, and
	// an extra positional makes the detector claim LESS, which is the safe
	// direction.
	valued := map[string]bool{
		"--tags": true, "-t": true, "--exclude-tags": true, "-x": true,
		"--name": true, "-n": true, "--plain-name": true, "-N": true,
		"--reporter": true, "-r": true, "--concurrency": true, "-j": true,
		"--timeout": true, "--platform": true, "-p": true, "-d": true,
		"--device-id": true, "--dart-define": true, "--coverage-path": true,
	}
	for _, f := range fields[start:] {
		if skipNext {
			skipNext = false
			continue
		}
		if strings.HasPrefix(f, "-") {
			if valued[f] {
				skipNext = true
			}
			continue
		}
		out = append(out, path.Clean(strings.TrimSuffix(f, "/")))
	}
	return out
}

// defaultRootFor is the directory a runner scans when given no path argument.
// `flutter test` scans `test/` and nothing else — which is why a suite under
// `integration_test/` is unreachable regardless of its tags.
func defaultRootFor(runner string) string { return "test" }

// outsideTargets reports whether a file lies under none of the command's path
// targets (or, with no targets, outside the runner's default root).
func outsideTargets(file string, targets []string, runner string) bool {
	roots := targets
	if len(roots) == 0 {
		roots = []string{defaultRootFor(runner)}
	}
	for _, r := range roots {
		if r == "." || r == file || strings.HasPrefix(file, r+"/") {
			return false
		}
	}
	return true
}

func describeTargets(targets []string, runner string) string {
	if len(targets) == 0 {
		return fmt.Sprintf("only the default %s/ directory", defaultRootFor(runner))
	}
	return "only " + strings.Join(targets, ", ")
}

// dartPathRemediation names the command that would run a file outside the
// default root. A tagged file needs its tag re-enabled as well as its path
// named, or the operator runs the suggested command and watches it skip
// everything.
func dartPathRemediation(runner, file string, tags []string) string {
	if len(tags) > 0 {
		return fmt.Sprintf("%s --tags=%s %s", runner, tags[0], file)
	}
	return fmt.Sprintf("%s %s", runner, file)
}

// dartFileTags reads a file's `@Tags([...])` names. A file that cannot be read
// or whose annotation cannot be parsed yields no tags and a warning: unreadable
// is not the same as excluded, and only the exclusion may block a stage.
func dartFileTags(repoRoot, file string) ([]string, string) {
	data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(file)))
	if err != nil {
		return nil, fmt.Sprintf("%s: could not read for tag detection: %v", file, err)
	}
	src := string(data)
	m := tagsAnnotationRe.FindStringSubmatch(src)
	if m == nil {
		if tagsAnnotationOpenRe.MatchString(src) {
			return nil, fmt.Sprintf("%s: @Tags annotation could not be parsed; treated as untagged", file)
		}
		return nil, ""
	}
	var tags []string
	for _, lit := range dartStringRe.FindAllStringSubmatch(m[1], -1) {
		v := lit[1]
		if v == "" {
			v = lit[2]
		}
		if v = strings.TrimSpace(v); v != "" {
			tags = appendUnique(tags, v)
		}
	}
	if len(tags) == 0 {
		return nil, fmt.Sprintf("%s: @Tags annotation contained no readable tag names; treated as untagged", file)
	}
	return tags, ""
}

// intersect returns the first member of a present in b, or "".
func intersect(a, b []string) string {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return x
			}
		}
	}
	return ""
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}
