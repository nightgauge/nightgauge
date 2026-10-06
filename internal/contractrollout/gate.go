package contractrollout

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// DefaultGateScript is a target's gate when its configuration declares
// none: the repository's own local gate, run as `bash scripts/ci-local.sh`.
const DefaultGateScript = "scripts/ci-local.sh"

// GateConfigPath is the target's own committed configuration. Its
// `local_gate` block declares the repository's gate (#2434).
const GateConfigPath = ".nightgauge/config.yaml"

// Gate is a target repository's local gate: steps run in order in the
// worktree, each an argv executed directly (never through a shell), stopping
// at the first failure. A zero Gate means the repository has none.
type Gate struct {
	Steps [][]string
	// Declared is true when the repository's own configuration declared the
	// gate; false for the DefaultGateScript fallback.
	Declared bool
}

// Missing reports a repository with no gate to run.
func (g Gate) Missing() bool { return len(g.Steps) == 0 }

// String renders the gate for a status row and a log line. It is never
// executed.
func (g Gate) String() string {
	steps := make([]string, 0, len(g.Steps))
	for _, s := range g.Steps {
		steps = append(steps, strings.Join(s, " "))
	}
	return strings.Join(steps, " && ")
}

// Scripts are the repository paths the gate runs as scripts: the
// interpreter steps' script arguments.
func (g Gate) Scripts() []string {
	var out []string
	for _, s := range g.Steps {
		if gatePrograms[s[0]].script {
			out = append(out, s[1])
		}
	}
	return out
}

// programRule is what a gate step may pass to one allowlisted program.
type programRule struct {
	// script requires the first argument to be a regular file in the
	// repository: the interpreter then runs the repository's own script
	// rather than a string from the declaration.
	script bool
	// refuse are flags that make the program run a command string, or a
	// makefile the step names (exact, or as flag=value).
	refuse []string
}

// gatePrograms is the allowlist of a declared gate's programs. A gate is a
// list of plain commands: what needs a shell, a pipe or an environment
// variable goes in a script the gate runs with bash.
var gatePrograms = map[string]programRule{
	"bash":    {script: true},
	"sh":      {script: true},
	"node":    {script: true},
	"python3": {script: true},
	"npm":     {refuse: []string{"-c", "--call"}},
	"npx":     {refuse: []string{"-c", "--call"}},
	"pnpm":    {refuse: []string{"-c", "--shell-mode"}},
	"yarn":    {},
	"go":      {},
	"make":    {refuse: []string{"-E", "--eval", "-f", "--file", "--makefile"}},
	"flutter": {},
	"dart":    {},
}

const (
	maxGateSteps   = 32
	maxGateArgs    = 64
	maxGateArgSize = 512
)

// gateDecl is the `local_gate` block of GateConfigPath.
type gateDecl struct {
	Steps [][]string `yaml:"steps"`
}

// ResolveGate returns the gate of the repository at root: the `local_gate`
// its own GateConfigPath declares, else `bash scripts/ci-local.sh` when that
// script is a regular file, else a zero Gate. Applying, root is the fresh
// worktree at origin/<base>, read before any contract file is written, so
// the gate is the one the repository's base branch declares; a contract can
// name no gate. A declaration that fails validation is an error, never a
// silent fallback to the default.
func ResolveGate(root string) (Gate, error) {
	decl, err := readGateDecl(root)
	if err != nil {
		return Gate{}, err
	}
	if decl != nil {
		if err := validateGateSteps(root, decl.Steps); err != nil {
			return Gate{}, fmt.Errorf("%s local_gate: %w", GateConfigPath, err)
		}
		return Gate{Steps: decl.Steps, Declared: true}, nil
	}
	if regularFileIn(root, DefaultGateScript) {
		return Gate{Steps: [][]string{{"bash", DefaultGateScript}}}, nil
	}
	return Gate{}, nil
}

// readGateDecl reads the `local_gate` block, or nil when the file or the
// block is absent. Other keys of the configuration are not this package's to
// judge; unknown keys inside the block are refused.
func readGateDecl(root string) (*gateDecl, error) {
	if err := refuseSymlinkPath(root, GateConfigPath); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(GateConfigPath)))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var top struct {
		LocalGate yaml.Node `yaml:"local_gate"`
	}
	if err := yaml.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("%s: %w", GateConfigPath, err)
	}
	if top.LocalGate.Kind == 0 {
		return nil, nil
	}
	raw, err := yaml.Marshal(&top.LocalGate)
	if err != nil {
		return nil, err
	}
	var decl gateDecl
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&decl); err != nil {
		return nil, fmt.Errorf("%s local_gate: %w", GateConfigPath, err)
	}
	return &decl, nil
}

// validateGateSteps holds a declared gate to the allowlist: a bounded list
// of steps, each a program from gatePrograms and plain arguments; an
// interpreter's first argument is a regular, repository-relative script file
// in root; no flag that runs a command string.
func validateGateSteps(root string, steps [][]string) error {
	if len(steps) == 0 {
		return fmt.Errorf("declares no steps")
	}
	if len(steps) > maxGateSteps {
		return fmt.Errorf("has %d steps, at most %d", len(steps), maxGateSteps)
	}
	for i, s := range steps {
		if len(s) == 0 {
			return fmt.Errorf("step %d is empty", i+1)
		}
		if len(s) > maxGateArgs {
			return fmt.Errorf("step %d has %d arguments, at most %d", i+1, len(s), maxGateArgs)
		}
		for _, a := range s {
			if a == "" || len(a) > maxGateArgSize || strings.IndexFunc(a, isControl) >= 0 {
				return fmt.Errorf("step %d: argument %q is empty, too long or holds a control character", i+1, a)
			}
		}
		rule, ok := gatePrograms[s[0]]
		if !ok {
			return fmt.Errorf("step %d: program %q is not one of %s", i+1, s[0], strings.Join(gateProgramNames(), ", "))
		}
		if rule.script {
			if len(s) < 2 {
				return fmt.Errorf("step %d: %s needs a script in the repository as its first argument", i+1, s[0])
			}
			if err := checkRelPath(s[1]); err != nil {
				return fmt.Errorf("step %d: %s script: %w", i+1, s[0], err)
			}
			if !regularFileIn(root, s[1]) {
				return fmt.Errorf("step %d: %s script %s is not a regular file in the repository", i+1, s[0], s[1])
			}
		}
		for _, a := range s[1:] {
			for _, flag := range rule.refuse {
				if a == flag || strings.HasPrefix(a, flag+"=") {
					return fmt.Errorf("step %d: %s %s is not allowed in a gate (it runs a command string or a file the step names); put the command in a script", i+1, s[0], flag)
				}
			}
		}
	}
	return nil
}

func gateProgramNames() []string {
	names := make([]string, 0, len(gatePrograms))
	for n := range gatePrograms {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

// regularFileIn reports whether rel is a regular file under root, reached
// through no symlink.
func regularFileIn(root, rel string) bool {
	if checkRelPath(rel) != nil || refuseSymlinkPath(root, rel) != nil {
		return false
	}
	info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil && info.Mode().IsRegular()
}

// checkGateUntouched refuses a contract that writes the gate's own
// declaration, the default gate script, the changelog, or a script the gate
// runs: the gate stays the repository's. Paths are compared as the
// filesystem may resolve them, not as strings: case-insensitively (APFS and
// NTFS resolve `Scripts/Gate.sh` to `scripts/gate.sh`), and, for a target
// that already exists under root, by file identity, which also catches a
// differently normalized spelling of the same file.
func checkGateUntouched(root string, g Gate, files []File) error {
	guarded := append([]string{GateConfigPath, DefaultGateScript, ChangelogPath}, g.Scripts()...)
	for _, f := range files {
		for _, gp := range guarded {
			if samePath(root, f.TargetPath(), gp) {
				return fmt.Errorf("files: %s is %s, part of the repository's own gate or changelog; a contract may not replace it", f.TargetPath(), gp)
			}
		}
	}
	return nil
}

// samePath reports whether repository paths a and b may name one file:
// equal under case folding, or, when both exist under root, the same file.
func samePath(root, a, b string) bool {
	if foldPath(a) == foldPath(b) {
		return true
	}
	if root == "" {
		return false
	}
	ai, err := os.Lstat(filepath.Join(root, filepath.FromSlash(a)))
	if err != nil {
		return false
	}
	bi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(b)))
	return err == nil && os.SameFile(ai, bi)
}

// foldPath is p's case-folded clean form, the key under which a
// case-insensitive filesystem resolves it.
func foldPath(p string) string {
	return strings.ToLower(path.Clean(p))
}

// runGate runs every step in dir and stops at the first failure; the error
// names the step, its exit status and the tail of its output.
func runGate(ctx context.Context, dir string, g Gate) error {
	for _, s := range g.Steps {
		if _, err := run(ctx, dir, s[0], s[1:]...); err != nil {
			return err
		}
	}
	return nil
}
