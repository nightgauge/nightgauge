package adapters

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode"

	"github.com/nightgauge/nightgauge/internal/adaptercompat"
)

// The flag contract (#1617): every flag a CLI adapter's BuildCommand can emit
// must be an option of that CLI, as the CLI's own `--help` at the compat
// manifest's max_tested version defines it. The help is captured, not written
// by hand: scripts/capture-cli-help.sh installs each CLI at max_tested into a
// throwaway prefix and writes testdata/cli-help, whose README.md records the
// provenance. An upstream that drops or renames a flag then fails a unit test
// instead of a live stage.

// flagContractHelpDirEnv names a directory of captures to read instead of
// testdata/cli-help, so TestFlagContract can run against help captured from
// the newest CLIs without a code change (#1639). A capture is found by name,
// <adapter>[-<sub>]-<version>.txt, and exactly one must match per adapter; its
// hidden-flag sidecar, if any, sits beside it (hiddenSidecarSuffix).
const flagContractHelpDirEnv = "NIGHTGAUGE_FLAG_CONTRACT_HELP_DIR"

// committedHelpDir holds the committed captures, each at its manifest's
// max_tested. The contract's own self-tests read it whatever the override
// says: they alter copies of these files and pin their versions.
var committedHelpDir = filepath.Join("testdata", "cli-help")

// flagContractHelpDir is the directory TestFlagContract reads, and whether the
// override set it. Only the committed captures are pinned to max_tested.
func flagContractHelpDir() (dir string, overridden bool) {
	if d := os.Getenv(flagContractHelpDirEnv); d != "" {
		return d, true
	}
	return committedHelpDir, false
}

// flagContractAdapters are the CLI adapters with a compat manifest: the ones
// that spawn an upstream CLI whose help can be captured. claude-sdk,
// gemini-sdk, lm-studio and ollama have none.
var flagContractAdapters = []string{"claude-headless", "codex", "copilot", "gemini", "grok", "opencode"}

// helpSubcommand is the subcommand an adapter's argv starts with. Its flags
// are options of that subcommand, so they are checked against
// `<binary> <sub> --help`. Keep in step with help_subcommand in
// scripts/capture-cli-help.sh.
var helpSubcommand = map[string]string{"codex": "exec", "opencode": "run"}

// helpNotCaptured are the adapters that have no captured help, and why. Their
// flags are not checked against help; their required_flags still are. An
// entry holds only while the manifest pins no max_tested, because capturing a
// CLI means pinning the version captured.
var helpNotCaptured = map[string]string{
	"copilot": "CLI not installed on the maintainer's machine",
	"gemini":  "CLI not installed on the maintainer's machine",
}

// knownBroken are flags an adapter emits that the captured CLI does not
// define, each with the bug that removes it. They are real defects, not
// exceptions: the entry keeps the rest of the adapter's argv under the
// contract while the fix is pending, and it fails the test as soon as it is
// not needed, when the flag appears in the help or BuildCommand stops
// emitting it. Probes: testdata/cli-help/README.md.
var knownBroken = map[string]map[string]int{
	// claude 2.1.258: `error: unknown option '--max-tokens'`.
	"claude-headless": {"--max-tokens": 1716},
	// codex-cli 0.145.0: `error: unexpected argument '--ask-for-approval'
	// found`. It is a top-level codex option, not an `exec` one.
	"codex": {"--ask-for-approval": 1715},
}

// hiddenSidecarSuffix names a capture's hidden-flag sidecar,
// <capture>.hidden: the flags that CLI version accepts without listing them
// in its help, one per line. Each was probed on the capture's version: the
// flag was accepted where a made-up flag was refused with the CLI's
// unknown-option error. The sidecar is data beside the capture, so recording
// a probe of a newer CLI (#1639) needs no code edit. Until a flag is probed on
// a capture's version, the contract reports it; a sidecar whose capture is
// gone is reported too. The committed sidecars' probes are in
// testdata/cli-help/README.md.
const hiddenSidecarSuffix = ".hidden"

// flagContractModel is a model each adapter's BuildCommand accepts: opencode
// takes only <provider>/<model>; the others resolve a tier.
func flagContractModel(adapter string) string {
	if adapter == "opencode" {
		return "lmstudio/qwen/qwen3.8-27b"
	}
	return "sonnet"
}

// flagContractToolSets are the allowed-tools values that change an argv: none,
// read-only, edit-only, and every tool. They are codex's three sandbox modes
// and claude's --allowedTools on and off.
var flagContractToolSets = [][]string{
	nil,
	{"Read", "Grep", "Glob"},
	{"Read", "Edit", "Write"},
	{"Bash", "Task", "WebFetch", "WebSearch", "mcp__github__create_issue", "Read", "Edit", "Write", "MultiEdit", "NotebookEdit", "Grep", "Glob"},
}

// expandOptions returns every option in in, once per value, with set applied.
func expandOptions[T any](in []RunOptions, values []T, set func(*RunOptions, T)) []RunOptions {
	out := make([]RunOptions, 0, len(in)*len(values))
	for _, o := range in {
		for _, v := range values {
			c := o
			set(&c, v)
			out = append(out, c)
		}
	}
	return out
}

// flagContractOptions is the cartesian product of the RunOptions values that
// change what a BuildCommand emits: model, effort, turn cap, token cap,
// allowed tools, cost budget, worktree, prompt and resume session (opencode's
// -s flag). TestFlagContractOptionsCoversEveryRunOptionsField holds this list,
// plus flagContractFixedFields below, to every field RunOptions declares.
func flagContractOptions(models, prompts []string) []RunOptions {
	opts := []RunOptions{{
		SkillPath:   "/skills/feature-dev/SKILL.md",
		ContextFile: "/ctx/in.json",
		OutputFile:  "/ctx/out.json",
		IssueNumber: 1617,
		Repo:        "nightgauge/nightgauge",
		Stage:       "feature-dev",
		TargetRepo:  "nightgauge/nightgauge",
		RunID:       "0199a8b2-0000-7000-8000-000000001617",
	}}
	opts = expandOptions(opts, models, func(o *RunOptions, v string) { o.Model = v })
	opts = expandOptions(opts, []string{"", "high"}, func(o *RunOptions, v string) { o.Effort = v })
	opts = expandOptions(opts, []int{0, 5}, func(o *RunOptions, v int) { o.MaxTurns = v })
	opts = expandOptions(opts, []int{0, 4096}, func(o *RunOptions, v int) { o.MaxTokens = v })
	opts = expandOptions(opts, flagContractToolSets, func(o *RunOptions, v []string) { o.AllowedTools = v })
	opts = expandOptions(opts, []float64{0, 1.5}, func(o *RunOptions, v float64) { o.CostBudget = v })
	opts = expandOptions(opts, []string{"", "/work/nightgauge-issue-1617"}, func(o *RunOptions, v string) { o.WorktreeDir = v })
	opts = expandOptions(opts, []string{"", "ses_1617abc"}, func(o *RunOptions, v string) { o.ResumeSessionID = v })
	return expandOptions(opts, prompts, func(o *RunOptions, v string) { o.Prompt = v })
}

// describeOptions names the varied fields that are set, for a failure message.
func describeOptions(o RunOptions) string {
	var parts []string
	add := func(set bool, format string, v any) {
		if set {
			parts = append(parts, fmt.Sprintf(format, v))
		}
	}
	add(o.Model != "", "Model=%s", o.Model)
	add(o.Effort != "", "Effort=%s", o.Effort)
	add(o.MaxTurns != 0, "MaxTurns=%d", o.MaxTurns)
	add(o.MaxTokens != 0, "MaxTokens=%d", o.MaxTokens)
	add(len(o.AllowedTools) > 0, "AllowedTools=%s", strings.Join(o.AllowedTools, ","))
	add(o.CostBudget != 0, "CostBudget=%g", o.CostBudget)
	add(o.WorktreeDir != "", "WorktreeDir=%s", o.WorktreeDir)
	add(o.ResumeSessionID != "", "ResumeSessionID=%s", o.ResumeSessionID)
	add(o.Prompt != "", "Prompt=%q", o.Prompt)
	if len(parts) == 0 {
		return "zero RunOptions"
	}
	return strings.Join(parts, " ")
}

// flagContractVariedFields are the RunOptions fields flagContractOptions
// varies across the option product (each set by one of its expandOptions
// calls).
var flagContractVariedFields = map[string]bool{
	"Model": true, "Effort": true, "MaxTurns": true, "MaxTokens": true,
	"AllowedTools": true, "CostBudget": true, "WorktreeDir": true,
	"ResumeSessionID": true, "Prompt": true,
}

// flagContractFixedFields are the RunOptions fields flagContractOptions sets
// once to a fixed non-zero value rather than varying: they identify the
// dispatch (skill, context files, issue, repo, stage, run id), not a choice a
// BuildCommand branches its argv on.
var flagContractFixedFields = map[string]bool{
	"SkillPath": true, "ContextFile": true, "OutputFile": true, "IssueNumber": true,
	"Repo": true, "Stage": true, "TargetRepo": true, "RunID": true,
}

// flagContractUnrepresentableFields are RunOptions fields this contract
// cannot set through the public struct literal flagContractOptions builds:
// named here, explicitly, rather than silently passing the exhaustiveness
// check below.
var flagContractUnrepresentableFields = map[string]bool{
	// RunRoot is the per-run root the manager prepares through an adapter's
	// PrepareRunRoot hook, before BuildCommand ever sees the options — never a
	// value this contract's own struct literal constructs.
	"RunRoot": true,
}

// TestFlagContractOptionsCoversEveryRunOptionsField pins #1721's
// exhaustiveness finding: without this test, a RunOptions field could be
// added and wired into an adapter's BuildCommand (gating a new flag) without
// flagContractOptions ever varying it — the field defaults to its zero value
// in every generated option, the gated flag never appears in any argv this
// suite builds, and TestFlagContract can never catch a real CLI dropping or
// renaming it. Every RunOptions field must be accounted for: varied, held
// fixed on purpose, or named as unrepresentable, each with its own list above.
func TestFlagContractOptionsCoversEveryRunOptionsField(t *testing.T) {
	rt := reflect.TypeOf(RunOptions{})
	var uncovered []string
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		switch {
		case flagContractVariedFields[name], flagContractFixedFields[name], flagContractUnrepresentableFields[name]:
			continue
		default:
			uncovered = append(uncovered, name)
		}
	}
	if len(uncovered) > 0 {
		t.Fatalf("RunOptions field(s) %q are in none of flagContractVariedFields, "+
			"flagContractFixedFields or flagContractUnrepresentableFields: a flag an adapter's "+
			"BuildCommand gates on one of them could be added without this contract's option "+
			"product ever exercising it. Add each field to the list that describes it, and if it "+
			"gates a flag, vary it in flagContractOptions", uncovered)
	}
}

// argvFlags returns the flags in one argv, "=value" stripped. With a
// subcommand, argv[0] must be it. No option value in flagContractOptions
// starts with "-", so every token that does is a flag, except the bare "-"
// codex takes as "read the prompt from stdin".
func argvFlags(sub string, args []string) ([]string, error) {
	if sub != "" {
		if len(args) == 0 || args[0] != sub {
			return nil, fmt.Errorf("argv does not start with the %q subcommand the help was captured for: %q", sub, args)
		}
		args = args[1:]
	}
	var flags []string
	for _, a := range args {
		if a == "-" || !strings.HasPrefix(a, "-") {
			continue
		}
		name, _, _ := strings.Cut(a, "=")
		flags = append(flags, name)
	}
	return flags, nil
}

// emittedFlagsByAdapter builds every adapter's command over
// flagContractOptions and returns, per adapter, each flag emitted and the
// first options that emitted it. The product runs twice: with a writable
// TMPDIR, and with one that does not exist, the only way grok falls back from
// --prompt-file to -p. It fails the test when an argv does not start with the
// adapter's subcommand.
func emittedFlagsByAdapter(t *testing.T) map[string]map[string]string {
	t.Helper()
	// The operator overrides grok reads would change its argv.
	t.Setenv("NIGHTGAUGE_GROK_CLI_COMMAND", "")
	t.Setenv("NIGHTGAUGE_GROK_EFFORT", "")
	writable := t.TempDir()
	missing := filepath.Join(t.TempDir(), "missing")

	registry := NewRegistry()
	out := map[string]map[string]string{}
	for _, adapter := range flagContractAdapters {
		runner, err := registry.Get(adapter)
		if err != nil {
			t.Fatalf("registry has no %s adapter: %v", adapter, err)
		}
		flags := map[string]string{}
		// The writable TMPDIR goes last so it stays set after this returns:
		// t.TempDir in the caller creates its directories under TMPDIR.
		for _, tmp := range []string{missing, writable} {
			t.Setenv("TMPDIR", tmp)
			for _, o := range flagContractOptions([]string{"", flagContractModel(adapter)}, []string{"", "implement the issue"}) {
				_, args, _ := runner.BuildCommand(o)
				got, err := argvFlags(helpSubcommand[adapter], args)
				if err != nil {
					t.Errorf("%s with %s: %v", adapter, describeOptions(o), err)
					continue
				}
				for _, f := range got {
					if _, seen := flags[f]; !seen {
						flags[f] = describeOptions(o)
					}
				}
			}
		}
		out[adapter] = flags
	}
	return out
}

// A captured help file starts with this header, which capture-cli-help.sh
// writes; the help text follows it verbatim, redacted.
var helpHeaderRE = regexp.MustCompile(`^# adapter=(\S+) version=([0-9]+\.[0-9]+\.[0-9]+) command=(.+)$`)

const (
	helpFlagPattern        = `--?[A-Za-z0-9][A-Za-z0-9-]*`
	helpPlaceholderPattern = `(?:<[^>]*>|\[[^\]]*\])(?:\.\.\.)?`
	helpOptionPattern      = helpFlagPattern + `(?:=` + helpPlaceholderPattern + `)?`
)

var (
	// helpOptionLineRE matches a line that defines options: at most eight
	// spaces of indentation, one or more flags separated by ", ", the value
	// placeholders, then the end of the line or two spaces and a description.
	// That is the shape commander (claude), clap (codex, grok) and yargs
	// (opencode) print. Wrapped description text is indented further or does
	// not start with a flag, so a flag named in prose is not an option.
	helpOptionLineRE = regexp.MustCompile(`^ {1,8}(` + helpOptionPattern + `(?:, ` + helpOptionPattern + `)*)(?: ` + helpPlaceholderPattern + `)*(?: {2,}\S.*)?$`)
	// helpAliasesRE is clap's alias annotation, as in "[aliases: --effort]".
	helpAliasesRE = regexp.MustCompile(`\[aliases: ([^\]]+)\]`)
	helpFlagRE    = regexp.MustCompile(`^` + helpFlagPattern + `$`)
)

// parseHelpOptions returns every option a help text defines, long and short
// forms and aliases alike.
func parseHelpOptions(help string) map[string]bool {
	options := map[string]bool{}
	for _, line := range strings.Split(help, "\n") {
		if m := helpOptionLineRE.FindStringSubmatch(line); m != nil {
			for _, item := range strings.Split(m[1], ", ") {
				name, _, _ := strings.Cut(item, "=")
				options[name] = true
			}
		}
		for _, m := range helpAliasesRE.FindAllStringSubmatch(line, -1) {
			for _, alias := range strings.Split(m[1], ",") {
				if alias = strings.TrimSpace(alias); helpFlagRE.MatchString(alias) {
					options[alias] = true
				}
			}
		}
	}
	return options
}

// helpCapture is one captured help file.
type helpCapture struct {
	path, adapter, version, command, body string
	options                               map[string]bool
}

// findHelpCapture returns the one capture in dir named for adapter, or why
// there is none; found is false when no file matches.
func findHelpCapture(dir, adapter string) (path string, found bool, problem string) {
	stem := adapter
	if sub := helpSubcommand[adapter]; sub != "" {
		stem += "-" + sub
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false, fmt.Sprintf("cannot read the help directory %s (%s): %v", dir, flagContractHelpDirEnv, err)
	}
	name := regexp.MustCompile(`^` + regexp.QuoteMeta(stem) + `-[0-9]+\.[0-9]+\.[0-9]+\.txt$`)
	var matches []string
	for _, e := range entries {
		if !e.IsDir() && name.MatchString(e.Name()) {
			matches = append(matches, filepath.Join(dir, e.Name()))
		}
	}
	switch len(matches) {
	case 0:
		return "", false, ""
	case 1:
		return matches[0], true, ""
	default:
		return "", true, fmt.Sprintf("%s: %d captures match %s-<version>.txt in %s, want exactly one: %q", adapter, len(matches), stem, dir, matches)
	}
}

func readHelpCapture(path string) (helpCapture, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return helpCapture{}, err
	}
	header, body, _ := strings.Cut(string(raw), "\n")
	m := helpHeaderRE.FindStringSubmatch(header)
	if m == nil {
		return helpCapture{}, fmt.Errorf("%s: the first line %q is not the capture header `# adapter=<id> version=<x.y.z> command=<command>`", path, header)
	}
	return helpCapture{path: path, adapter: m[1], version: m[2], command: m[3], body: body, options: parseHelpOptions(body)}, nil
}

// readHiddenSidecar returns the flags the sidecar of the capture at
// capturePath lists, or none when the capture has no sidecar. A blank line or
// one starting with "#" is a comment; every other line is exactly one flag.
func readHiddenSidecar(capturePath string) ([]string, error) {
	path := capturePath + hiddenSidecarSuffix
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var flags []string
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case !helpFlagRE.MatchString(line):
			return nil, fmt.Errorf("%s:%d: %q is not a flag; each line is one flag, and # starts a comment", path, i+1, line)
		case seen[line]:
			return nil, fmt.Errorf("%s:%d: %s is listed twice", path, i+1, line)
		}
		seen[line] = true
		flags = append(flags, line)
	}
	if len(flags) == 0 {
		return nil, fmt.Errorf("%s lists no flag: remove it", path)
	}
	return flags, nil
}

// flagContractProblems checks every adapter's emitted flags against the
// captures in dir and their sidecars. It returns the violations, and notes on
// what was not checked or was accepted by a knownBroken entry or a sidecar.
// pinned holds the captures to the manifests' max_tested, which only the
// committed captures are.
func flagContractProblems(dir string, pinned bool, emitted map[string]map[string]string) (problems, notes []string) {
	captured := map[string]bool{}
	for _, adapter := range flagContractAdapters {
		m, ok := adaptercompat.Get(adapter)
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: no compat manifest", adapter))
			continue
		}
		if reason, skip := helpNotCaptured[adapter]; skip && m.MaxTested != "" {
			problems = append(problems, fmt.Sprintf("%s: helpNotCaptured says %q, but its manifest pins max_tested %s: capture its help with scripts/capture-cli-help.sh and remove the entry", adapter, reason, m.MaxTested))
		}
		flags := emitted[adapter]
		for flag, issue := range knownBroken[adapter] {
			if _, ok := flags[flag]; !ok {
				problems = append(problems, fmt.Sprintf("%s: knownBroken lists %s (#%d), but BuildCommand no longer emits it: remove the entry", adapter, flag, issue))
			}
		}

		path, found, problem := findHelpCapture(dir, adapter)
		if problem != "" {
			problems = append(problems, problem)
			continue
		}
		if !found {
			if reason, skip := helpNotCaptured[adapter]; skip {
				notes = append(notes, fmt.Sprintf("%s: flags not checked against help: %s", adapter, reason))
			} else {
				problems = append(problems, fmt.Sprintf("%s: no captured help in %s; scripts/capture-cli-help.sh writes it", adapter, dir))
			}
			continue
		}
		captured[filepath.Base(path)] = true
		capture, err := readHelpCapture(path)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		wantCommand := m.Binary + " --help"
		if sub := helpSubcommand[adapter]; sub != "" {
			wantCommand = m.Binary + " " + sub + " --help"
		}
		switch {
		case capture.adapter != adapter:
			problems = append(problems, fmt.Sprintf("%s: its header names adapter %q, not %q", path, capture.adapter, adapter))
			continue
		case capture.command != wantCommand:
			problems = append(problems, fmt.Sprintf("%s: its header names command %q, not %q", path, capture.command, wantCommand))
			continue
		case !strings.HasSuffix(path, "-"+capture.version+".txt"):
			problems = append(problems, fmt.Sprintf("%s: its header names version %s, which is not the version in its name", path, capture.version))
			continue
		case pinned && capture.version != m.MaxTested:
			problems = append(problems, fmt.Sprintf("%s: captured version %s is not the manifest's max_tested %q; re-run scripts/capture-cli-help.sh", path, capture.version, m.MaxTested))
			continue
		case len(capture.options) < 10:
			problems = append(problems, fmt.Sprintf("%s: parsed only %d options; the help parser no longer matches its format", path, len(capture.options)))
			continue
		}

		sidecar := path + hiddenSidecarSuffix
		hiddenFlags, err := readHiddenSidecar(path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", adapter, err))
			continue
		}
		hidden := map[string]bool{}
		for _, f := range hiddenFlags {
			hidden[f] = true
			if issue, broken := knownBroken[adapter][f]; broken {
				problems = append(problems, fmt.Sprintf("%s: %s lists %s as accepted, but knownBroken has it refused (#%d): probe it again and correct one of them", adapter, sidecar, f, issue))
			}
			if _, ok := flags[f]; !ok {
				problems = append(problems, fmt.Sprintf("%s: %s lists %s, but BuildCommand no longer emits it: remove the line", adapter, sidecar, f))
			}
		}
		for _, flag := range sortedKeys(flags) {
			issue, broken := knownBroken[adapter][flag]
			defined := capture.options[flag]
			switch {
			case defined && broken:
				problems = append(problems, fmt.Sprintf("%s: %s defines %s now, so knownBroken's entry for #%d is not needed: remove it", adapter, path, flag, issue))
			case defined && hidden[flag]:
				problems = append(problems, fmt.Sprintf("%s: %s lists %s now, so %s need not: remove the line", adapter, path, flag, sidecar))
			case defined:
			case broken:
				notes = append(notes, fmt.Sprintf("%s: %s is known broken (#%d); %s does not define it", adapter, flag, issue, path))
			case hidden[flag]:
				notes = append(notes, fmt.Sprintf("%s: %s is hidden in %s; %s records it probed as accepted on %s", adapter, flag, path, sidecar, capture.version))
			default:
				problems = append(problems, fmt.Sprintf("%s: BuildCommand emits %s (with %s), which %s (`%s`, version %s) does not define", adapter, flag, flags[flag], path, capture.command, capture.version))
			}
		}
	}
	return append(problems, orphanSidecars(dir, captured)...), notes
}

// orphanSidecars reports each sidecar in dir with no capture of its name
// beside it. A sidecar records probes of one version, so after a re-capture
// its flags are probed again on the new version and the sidecar renamed.
func orphanSidecars(dir string, captured map[string]bool) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // findHelpCapture has reported it.
	}
	var problems []string
	for _, e := range entries {
		capture, isSidecar := strings.CutSuffix(e.Name(), hiddenSidecarSuffix)
		if isSidecar && !captured[capture] {
			problems = append(problems, fmt.Sprintf("%s: there is no capture %s beside it, and its probes hold only for that version: probe its flags on the version captured and name the sidecar after that capture, or remove it", filepath.Join(dir, e.Name()), capture))
		}
	}
	return problems
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestFlagContract checks every flag each CLI adapter's BuildCommand emits,
// across the option product, against the adapter's captured help.
func TestFlagContract(t *testing.T) {
	dir, overridden := flagContractHelpDir()
	problems, notes := flagContractProblems(dir, !overridden, emittedFlagsByAdapter(t))
	for _, n := range notes {
		t.Log(n)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// ignoreHelpDirOverride points NIGHTGAUGE_FLAG_CONTRACT_HELP_DIR at an empty
// directory for the rest of t. The canary (#1639) runs `-run TestFlagContract`
// with the override set, which also runs the contract's self-tests; they read
// committedHelpDir, so one that read the override instead fails here too.
func ignoreHelpDirOverride(t *testing.T) {
	t.Helper()
	t.Setenv(flagContractHelpDirEnv, t.TempDir())
}

// copyHelpDir copies every file in committedHelpDir, captures and sidecars
// alike, into a new temporary directory.
func copyHelpDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	entries, err := os.ReadDir(committedHelpDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(committedHelpDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// recaptureAs turns adapter's capture in dir into one of version, header and
// name alike, with the help text unchanged. With probed, its sidecar moves
// with it, as recording that version's probes would; without, the sidecar
// keeps the old version's name. It returns the sidecar's path, or "" when the
// capture has none.
func recaptureAs(t *testing.T, dir, adapter, version string, probed bool) (sidecar string) {
	t.Helper()
	old, found, problem := findHelpCapture(dir, adapter)
	if !found || problem != "" {
		t.Fatalf("no capture for %s in %s: %s", adapter, dir, problem)
	}
	c, err := readHelpCapture(old)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(old)
	if err != nil {
		t.Fatal(err)
	}
	renamed := strings.Replace(string(data), "version="+c.version+" ", "version="+version+" ", 1)
	path := strings.TrimSuffix(old, c.version+".txt") + version + ".txt"
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(renamed), 0o644); err != nil {
		t.Fatal(err)
	}
	sidecar = old + hiddenSidecarSuffix
	if _, err := os.Stat(sidecar); errors.Is(err, fs.ErrNotExist) {
		return ""
	} else if err != nil {
		t.Fatal(err)
	}
	if !probed {
		return sidecar
	}
	if err := os.Rename(sidecar, path+hiddenSidecarSuffix); err != nil {
		t.Fatal(err)
	}
	return path + hiddenSidecarSuffix
}

// TestFlagContractReadsTheHelpDirOverride runs the contract against altered
// copies of the captures, in a directory named by
// NIGHTGAUGE_FLAG_CONTRACT_HELP_DIR. An unaltered copy passes. So does a copy
// whose every capture is of a newer version, when the probes of that version
// are recorded in its sidecars: other help text needs no code edit. Without
// those probes, each hidden flag is reported, and so is each sidecar left
// under the old version's name. A copy missing one option's definition fails
// with the adapter, the flag and the file.
func TestFlagContractReadsTheHelpDirOverride(t *testing.T) {
	ignoreHelpDirOverride(t)
	emitted := emittedFlagsByAdapter(t)
	captured := func() []string {
		var adapters []string
		for _, adapter := range flagContractAdapters {
			if _, skip := helpNotCaptured[adapter]; !skip {
				adapters = append(adapters, adapter)
			}
		}
		return adapters
	}()

	run := func(t *testing.T, dir string) []string {
		t.Helper()
		t.Setenv(flagContractHelpDirEnv, dir)
		got, overridden := flagContractHelpDir()
		if got != dir || !overridden {
			t.Fatalf("flagContractHelpDir() = %q, %v with %s=%s", got, overridden, flagContractHelpDirEnv, dir)
		}
		problems, _ := flagContractProblems(got, !overridden, emitted)
		return problems
	}

	t.Run("unaltered copy", func(t *testing.T) {
		if problems := run(t, copyHelpDir(t)); len(problems) != 0 {
			t.Errorf("an unaltered copy of the captures failed the contract:\n%s", strings.Join(problems, "\n"))
		}
	})

	t.Run("newer versions with their probes recorded", func(t *testing.T) {
		dir := copyHelpDir(t)
		var sidecars []string
		for _, adapter := range captured {
			if sidecar := recaptureAs(t, dir, adapter, "99.0.0", true); sidecar != "" {
				sidecars = append(sidecars, sidecar)
			}
		}
		if problems := run(t, dir); len(problems) != 0 {
			t.Errorf("captures of newer versions, their hidden flags probed and recorded, failed the contract:\n%s", strings.Join(problems, "\n"))
		}
		if len(sidecars) == 0 {
			t.Error("no committed capture has a sidecar, so this case no longer shows a probe record reaching the contract without a code edit")
		}
	})

	t.Run("newer versions without their probes", func(t *testing.T) {
		dir := copyHelpDir(t)
		var want []string
		for _, adapter := range captured {
			committedCapture, _, _ := findHelpCapture(committedHelpDir, adapter)
			hidden, err := readHiddenSidecar(committedCapture)
			if err != nil {
				t.Fatal(err)
			}
			if sidecar := recaptureAs(t, dir, adapter, "99.0.0", false); sidecar != "" {
				want = append(want, sidecar+": there is no capture "+filepath.Base(strings.TrimSuffix(sidecar, hiddenSidecarSuffix)))
			}
			for _, f := range hidden {
				want = append(want, adapter+": BuildCommand emits "+f+" ")
			}
		}
		if len(want) == 0 {
			t.Fatal("no committed capture has a sidecar, so this case shows nothing")
		}
		problems := run(t, dir)
		if len(problems) != len(want) {
			t.Errorf("want %d violations, got %d:\n%s", len(want), len(problems), strings.Join(problems, "\n"))
		}
		for _, w := range want {
			found := false
			for _, p := range problems {
				found = found || strings.Contains(p, w)
			}
			if !found {
				t.Errorf("no violation says %q:\n%s", w, strings.Join(problems, "\n"))
			}
		}
	})

	for _, c := range []struct{ adapter, flag string }{
		{"claude-headless", "--allowedTools"},
		{"opencode", "--dir"},
	} {
		t.Run("drops "+c.adapter+" "+c.flag, func(t *testing.T) {
			dir := copyHelpDir(t)
			path, _, _ := findHelpCapture(dir, c.adapter)
			removeOptionDefinition(t, path, c.flag)
			problems := run(t, dir)
			if len(problems) != 1 {
				t.Fatalf("want exactly one violation, got %d:\n%s", len(problems), strings.Join(problems, "\n"))
			}
			for _, want := range []string{c.adapter + ":", c.flag, path} {
				if !strings.Contains(problems[0], want) {
					t.Errorf("the violation does not name %q: %s", want, problems[0])
				}
			}
		})
	}
}

// removeOptionDefinition deletes the line of a capture that defines flag.
func removeOptionDefinition(t *testing.T, path, flag string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	kept := lines[:0]
	removed := 0
	for _, line := range lines {
		if m := helpOptionLineRE.FindStringSubmatch(line); m != nil && definesFlag(m[1], flag) {
			removed++
			continue
		}
		kept = append(kept, line)
	}
	if removed != 1 {
		t.Fatalf("%s: %d lines define %s, want 1", path, removed, flag)
	}
	if err := os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
}

// definesFlag reports whether an option line's flag list names flag.
func definesFlag(list, flag string) bool {
	for _, item := range strings.Split(list, ", ") {
		if name, _, _ := strings.Cut(item, "="); name == flag {
			return true
		}
	}
	return false
}

// TestFlagContractTablesFailWhenNotNeeded checks that a knownBroken entry or a
// sidecar line fails the contract once it is not needed: when the flag
// appears in the help, and when BuildCommand stops emitting it. A sidecar
// with no capture of its name, a sidecar line that is not a flag, and a flag
// a sidecar accepts while knownBroken has it refused fail it too.
func TestFlagContractTablesFailWhenNotNeeded(t *testing.T) {
	ignoreHelpDirOverride(t)
	emitted := emittedFlagsByAdapter(t)
	committed := committedHelpDir
	captureName := func(adapter string) string {
		t.Helper()
		name, found := findHelpCaptureName(committed, adapter)
		if !found {
			t.Fatalf("no capture for %s in %s", adapter, committed)
		}
		return name
	}
	sidecarName := func(adapter string) string { return captureName(adapter) + hiddenSidecarSuffix }

	// appended copies the committed directory and appends line to the file
	// name in the copy, creating it if need be.
	appended := func(t *testing.T, name, line string) string {
		t.Helper()
		dir := copyHelpDir(t)
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(data, line+"\n"...), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	withDefinition := func(t *testing.T, adapter, line string) string {
		t.Helper()
		return appended(t, captureName(adapter), line)
	}
	without := func(adapter, flag string) map[string]map[string]string {
		out := map[string]map[string]string{}
		for a, flags := range emitted {
			out[a] = map[string]string{}
			for f, o := range flags {
				if a != adapter || f != flag {
					out[a][f] = o
				}
			}
		}
		return out
	}

	cases := []struct {
		name    string
		dir     string
		emitted map[string]map[string]string
		want    []string
	}{
		{"known broken flag now in help", withDefinition(t, "codex", "      --ask-for-approval <APPROVAL_POLICY>"), emitted, []string{"codex:", "--ask-for-approval", "#1715", "remove it"}},
		{"known broken flag no longer emitted", committed, without("claude-headless", "--max-tokens"), []string{"claude-headless:", "--max-tokens", "#1716", "remove the entry"}},
		{"hidden flag now in help", withDefinition(t, "claude-headless", "  --max-turns <turns>                   Maximum agentic turns"), emitted, []string{"claude-headless:", "--max-turns", sidecarName("claude-headless"), "remove the line"}},
		{"hidden flag no longer emitted", committed, without("grok", "--no-auto-update"), []string{"grok:", "--no-auto-update", sidecarName("grok"), "remove the line"}},
		{"sidecar of another version", appended(t, "grok-0.0.1.txt"+hiddenSidecarSuffix, "--no-auto-update"), emitted, []string{"grok-0.0.1.txt" + hiddenSidecarSuffix, "no capture grok-0.0.1.txt", "probe its flags on the version captured"}},
		{"sidecar line that is not a flag", appended(t, sidecarName("claude-headless"), "max-turns"), emitted, []string{"claude-headless:", sidecarName("claude-headless"), `"max-turns" is not a flag`}},
		{"sidecar accepts a known broken flag", appended(t, sidecarName("claude-headless"), "--max-tokens"), emitted, []string{"claude-headless:", sidecarName("claude-headless"), "--max-tokens", "#1716", "knownBroken"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			problems, _ := flagContractProblems(c.dir, true, c.emitted)
			if len(problems) != 1 {
				t.Fatalf("want exactly one violation, got %d:\n%s", len(problems), strings.Join(problems, "\n"))
			}
			for _, want := range c.want {
				if !strings.Contains(problems[0], want) {
					t.Errorf("the violation does not say %q: %s", want, problems[0])
				}
			}
		})
	}
}

// findHelpCaptureName is the file name of adapter's capture in dir.
func findHelpCaptureName(dir, adapter string) (string, bool) {
	path, found, _ := findHelpCapture(dir, adapter)
	return filepath.Base(path), found
}

// TestHelpOptionParser pins what the parser reads from the real captures: long
// and short forms, a commander option whose description starts on the next
// line, clap's [aliases: ...], and never a flag that description prose only
// mentions.
func TestHelpOptionParser(t *testing.T) {
	ignoreHelpDirOverride(t)
	dir := committedHelpDir
	cases := []struct {
		adapter      string
		defined      []string
		proseOnly    []string
		minimumCount int
	}{
		{"claude-headless", []string{"-p", "--print", "--allowedTools", "--allowed-tools", "--exclude-dynamic-system-prompt-sections", "--max-budget-usd", "--no-session-persistence", "--output-format"}, nil, 50},
		{"codex", []string{"-s", "--sandbox", "-m", "--model", "--json", "--dangerously-bypass-approvals-and-sandbox", "-i", "--image"}, []string{"--last"}, 20},
		{"grok", []string{"-p", "--single", "--prompt-file", "--reasoning-effort", "--effort", "--worktree-ref", "--ref", "-r", "--resume"}, []string{"--allowedTools", "--disallowedTools", "--system-prompt"}, 40},
		{"opencode", []string{"-m", "--model", "--format", "--print-logs", "--log-level", "--dir", "--auto", "--share", "--port"}, []string{"--yolo", "--dangerously-skip-permissions"}, 20},
	}
	for _, c := range cases {
		t.Run(c.adapter, func(t *testing.T) {
			path, found, problem := findHelpCapture(dir, c.adapter)
			if !found || problem != "" {
				t.Fatalf("no capture for %s in %s: %s", c.adapter, dir, problem)
			}
			capture, err := readHelpCapture(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(capture.options) < c.minimumCount {
				t.Errorf("parsed %d options from %s, want at least %d", len(capture.options), path, c.minimumCount)
			}
			for _, f := range c.defined {
				if !capture.options[f] {
					t.Errorf("%s defines %s, and the parser missed it", path, f)
				}
			}
			for _, f := range c.proseOnly {
				if capture.options[f] {
					t.Errorf("%s only mentions %s in prose, and the parser counted it as an option", path, f)
				}
			}
		})
	}
}

// TestOpenCodeHelpCaptureMatchesTheOpenCodeEvidence holds the two opencode
// captures together: ADR-022's evidence (testdata/opencode-cli) and the
// flag-contract capture are the same `opencode run --help` of the same version.
func TestOpenCodeHelpCaptureMatchesTheOpenCodeEvidence(t *testing.T) {
	dir := committedHelpDir
	path, found, problem := findHelpCapture(dir, "opencode")
	if !found || problem != "" {
		t.Fatalf("no opencode capture in %s: %s", dir, problem)
	}
	capture, err := readHelpCapture(path)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := os.ReadFile(filepath.Join("testdata", "opencode-cli", "run-help.txt"))
	if err != nil {
		t.Fatal(err)
	}
	version, err := os.ReadFile(filepath.Join("testdata", "opencode-cli", "version.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(version)); capture.version != got {
		t.Errorf("%s is opencode %s; testdata/opencode-cli is opencode %s", path, capture.version, got)
	}
	if capture.body != string(evidence) {
		t.Errorf("%s and testdata/opencode-cli/run-help.txt differ; re-capture both from the same opencode", path)
	}
}

// openCodeForbiddenFlagIn returns the forbidden flag an argv token spells, if
// any. opencode parses `run` with yargs, which takes an option under more
// names than its kebab-case one: 1.18.30 accepts --dangerouslySkipPermissions,
// --yolo=true and --auto.x as it accepts --auto (testdata/cli-help/README.md).
// So the token is reduced to an option name before it is compared: leading
// dashes stripped, cut at "=" and ".", camelCase split into kebab-case, "_"
// read as "-", and lowercased. A name that matches once lowercased without the
// split counts too. 1.18.30 refuses --Yolo and --dangerously_skip_permissions,
// and they are caught all the same, should a later version accept them.
func openCodeForbiddenFlagIn(arg string) (string, bool) {
	name := strings.TrimLeft(arg, "-")
	if name == arg || name == "" {
		return "", false
	}
	name, _, _ = strings.Cut(name, "=")
	name, _, _ = strings.Cut(name, ".")
	name = strings.ReplaceAll(name, "_", "-")
	var kebab strings.Builder
	prev := rune(0)
	for _, r := range name {
		if unicode.IsUpper(r) && (unicode.IsLower(prev) || unicode.IsDigit(prev)) {
			kebab.WriteByte('-')
		}
		kebab.WriteRune(unicode.ToLower(r))
		prev = r
	}
	for _, f := range openCodeForbiddenFlags {
		option := strings.TrimLeft(f, "-")
		if kebab.String() == option || strings.ToLower(name) == option {
			return f, true
		}
	}
	return "", false
}

// TestOpenCodeForbiddenFlagSpellings pins the spellings the forbidden-flag
// guard catches, among them the camelCase, dot-notation and =value forms
// opencode 1.18.30's `run` accepts in place of the kebab-case option, and the
// argv tokens it must leave alone.
func TestOpenCodeForbiddenFlagSpellings(t *testing.T) {
	for arg, want := range map[string]string{
		"--auto":                            "--auto",
		"--auto=true":                       "--auto",
		"--auto.x":                          "--auto",
		"-auto":                             "--auto",
		"--Auto":                            "--auto",
		"--yolo":                            "--yolo",
		"--Yolo=1":                          "--yolo",
		"--YOLO":                            "--yolo",
		"--dangerously-skip-permissions":    "--dangerously-skip-permissions",
		"--dangerouslySkipPermissions":      "--dangerously-skip-permissions",
		"--dangerouslySkipPermissions=true": "--dangerously-skip-permissions",
		"--dangerously-skip-permissions.x":  "--dangerously-skip-permissions",
		"--dangerously_skip_permissions":    "--dangerously-skip-permissions",
		"--DANGEROUSLY-SKIP-PERMISSIONS":    "--dangerously-skip-permissions",
		"--share":                           "--share",
		"--share.enabled=1":                 "--share",
		"--mdns":                            "--mdns",
		"--cors=*":                          "--cors",
		"--Cors":                            "--cors",
	} {
		if got, ok := openCodeForbiddenFlagIn(arg); !ok || got != want {
			t.Errorf("openCodeForbiddenFlagIn(%q) = %q, %v; want %q, true", arg, got, ok, want)
		}
	}
	for _, arg := range []string{
		"run", "--format", "json", "--print-logs", "--log-level", "ERROR", "-m",
		"lmstudio/qwen/qwen3.8-27b", "--dir", "/work/--auto", "-", "--",
		"--no-auto", "--autoupdate", "--sharex", "auto", "yolo",
	} {
		if got, ok := openCodeForbiddenFlagIn(arg); ok {
			t.Errorf("openCodeForbiddenFlagIn(%q) = %q, true; it is not a forbidden flag", arg, got)
		}
	}
}

// TestOpenCodeNeverEmitsBypassFlags drives the opencode BuildCommand through
// the whole option product, including every tool allowed, with hostile models
// and prompts and an auto-approve variable in the environment. No argv token
// may be a flag that approves tools without the permission map, publishes the
// session or exposes a listener, in any spelling openCodeForbiddenFlagIn
// catches. --yolo and --dangerously-skip-permissions are hidden in opencode
// 1.18.30's help, so only this explicit list catches them.
func TestOpenCodeNeverEmitsBypassFlags(t *testing.T) {
	for _, f := range []string{"--auto", "--yolo", "--dangerously-skip-permissions", "--share", "--port", "--mdns", "--cors"} {
		found := false
		for _, g := range openCodeForbiddenFlags {
			found = found || g == f
		}
		if !found {
			t.Errorf("openCodeForbiddenFlags lost %s", f)
		}
	}

	t.Setenv("NIGHTGAUGE_AUTO_APPROVE", "true")
	a := NewOpenCodeAdapter()
	models := []string{"", "lmstudio/qwen/qwen3.8-27b", "anthropic/claude-sonnet-5", "--auto/x", "lmstudio/--share", "--dangerouslySkipPermissions/x"}
	prompts := []string{"", "implement the issue", "--auto --yolo --dangerously-skip-permissions --dangerouslySkipPermissions --share --port --mdns --cors"}
	options := flagContractOptions(models, prompts)
	for _, o := range options {
		_, args, _ := a.BuildCommand(o)
		for _, arg := range args {
			if f, ok := openCodeForbiddenFlagIn(arg); ok {
				t.Errorf("opencode argv carries %s as %q with %s: %q", f, arg, describeOptions(o), args)
			}
		}
	}
	if len(options) < 1000 {
		t.Errorf("the option product has %d combinations; it no longer covers every field", len(options))
	}
}

// TestManifestRequiredFlagsMatch holds each manifest's required_flags to the
// set of flags BuildCommand emits across the option product, in both
// directions, so the manifest cannot drift from the adapter.
func TestManifestRequiredFlagsMatch(t *testing.T) {
	ms, err := adaptercompat.Load()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.Adapter)
	}
	if strings.Join(ids, ",") != strings.Join(flagContractAdapters, ",") {
		t.Errorf("the compat manifests are %v but the flag contract covers %v", ids, flagContractAdapters)
	}
	emitted := emittedFlagsByAdapter(t)
	for _, adapter := range flagContractAdapters {
		m, ok := adaptercompat.Get(adapter)
		if !ok {
			t.Errorf("%s: no compat manifest", adapter)
			continue
		}
		if p := requiredFlagsProblem(adapter, m.RequiredFlags, emitted[adapter]); p != "" {
			t.Error(p)
		}
	}
}

// requiredFlagsProblem reports how required_flags differs from the emitted
// set, or "" when they are the same set.
func requiredFlagsProblem(adapter string, required []string, emitted map[string]string) string {
	have := map[string]bool{}
	var duplicated, notEmitted, missing []string
	for _, f := range required {
		if have[f] {
			duplicated = append(duplicated, f)
		}
		have[f] = true
		if _, ok := emitted[f]; !ok {
			notEmitted = append(notEmitted, f)
		}
	}
	for _, f := range sortedKeys(emitted) {
		if !have[f] {
			missing = append(missing, f)
		}
	}
	if len(duplicated)+len(notEmitted)+len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf("%s: required_flags is not the set BuildCommand emits (emitted but not listed: %q; listed but not emitted: %q; listed twice: %q); update internal/adaptercompat/manifests/%s.json so required_flags is %q",
		adapter, missing, notEmitted, duplicated, adapter, sortedKeys(emitted))
}

// TestRequiredFlagsProblemNamesTheManifest checks both directions of the
// comparison, and that the failure says which manifest to update.
func TestRequiredFlagsProblemNamesTheManifest(t *testing.T) {
	emitted := emittedFlagsByAdapter(t)["grok"]
	listed := sortedKeys(emitted)

	var withoutMaxTurns []string
	for _, f := range listed {
		if f != "--max-turns" {
			withoutMaxTurns = append(withoutMaxTurns, f)
		}
	}
	for _, c := range []struct {
		name     string
		required []string
		flag     string
	}{
		{"one flag missing", withoutMaxTurns, "--max-turns"},
		{"one flag extra", append(append([]string{}, listed...), "--bogus-flag"), "--bogus-flag"},
		{"one flag twice", append(append([]string{}, listed...), "--max-turns"), "--max-turns"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := requiredFlagsProblem("grok", c.required, emitted)
			for _, want := range []string{"update internal/adaptercompat/manifests/grok.json", c.flag} {
				if !strings.Contains(p, want) {
					t.Errorf("the failure does not say %q: %q", want, p)
				}
			}
		})
	}
	if p := requiredFlagsProblem("grok", listed, emitted); p != "" {
		t.Errorf("the emitted set itself was refused: %s", p)
	}
}
