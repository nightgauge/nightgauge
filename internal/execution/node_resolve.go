package execution

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// nodeResolution is the outcome of resolving which Node bin directory (if any)
// should be prepended to a spawned stage's PATH.
type nodeResolution struct {
	// Dir is the directory to prepend to PATH, or "" when nothing should be
	// prepended (node already reachable on PATH, or unresolvable).
	Dir string
	// Source records how Node was resolved: "nvm", "path", or "none".
	Source string
}

// nodeEnv abstracts the host interactions resolveNodeBinDir needs so the
// cascade can be unit-tested without a real nvm install or PATH.
type nodeEnv interface {
	// Home returns the user's home directory.
	Home() string
	// FileExists reports whether path exists.
	FileExists(path string) bool
	// LookPath reports whether name resolves on PATH (wraps exec.LookPath).
	LookPath(name string) (string, error)
	// NvmWhichDefault returns the absolute path to the Node binary for nvm's
	// `default` alias by sourcing the host's nvm and running `nvm which default`.
	NvmWhichDefault() (string, error)
}

// resolveNodeBinDir resolves Node for a spawned stage subprocess as a cascade
// (issue #3863):
//
//  1. nvm present ($HOME/.nvm/nvm.sh exists) → use the `default` alias. A
//     non-interactive spawn does NOT inherit the login shell's nvm PATH, so we
//     source nvm and ask it for `default` explicitly. The `default` alias is the
//     single source of truth — `nvm alias default <v>` is the only knob, so the
//     runner and local compilation never drift.
//  2. else node already on PATH → nothing to prepend (the normal hosted-runner
//     case where setup-node / the image already put node on PATH). Not an error.
//  3. else unresolvable → nothing to prepend; the stage's own command surfaces a
//     clear "node not found".
//
// It deliberately does NOT read a per-repo .nvmrc, hardcode a version, or assume
// Homebrew. nvm is preferred when present, never required.
func resolveNodeBinDir(e nodeEnv) nodeResolution {
	if e.FileExists(filepath.Join(e.Home(), ".nvm", "nvm.sh")) {
		if out, err := e.NvmWhichDefault(); err == nil {
			p := strings.TrimSpace(out)
			// `nvm which default` prints an "N/A: version ... is not yet
			// installed" notice (not an absolute path) when the alias is
			// unusable; only accept a real absolute path.
			if p != "" && filepath.IsAbs(p) {
				return nodeResolution{Dir: filepath.Dir(p), Source: "nvm"}
			}
		}
		// nvm present but `default` unresolved — fall through to PATH.
	}
	if _, err := e.LookPath("node"); err == nil {
		return nodeResolution{Dir: "", Source: "path"}
	}
	return nodeResolution{Dir: "", Source: "none"}
}

// osNodeEnv is the production nodeEnv backed by the real OS.
type osNodeEnv struct{}

func (osNodeEnv) Home() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return os.Getenv("HOME")
}

func (osNodeEnv) FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (osNodeEnv) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

func (osNodeEnv) NvmWhichDefault() (string, error) {
	// Source the host's nvm in a login shell and resolve the default alias. The
	// spawn that calls this does not inherit the interactive shell's nvm PATH,
	// which is exactly why this explicit resolution is needed.
	out, err := exec.Command("bash", "-lc",
		`export NVM_DIR="$HOME/.nvm"; . "$NVM_DIR/nvm.sh" >/dev/null 2>&1; nvm which default 2>/dev/null`).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// prependPATH returns a copy of env (a slice of KEY=VALUE strings) with dir
// prepended to the PATH entry. If env has no PATH entry, one is added. dir is
// assumed non-empty.
func prependPATH(env []string, dir string) []string {
	out := make([]string, 0, len(env)+1)
	found := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			found = true
			existing := strings.TrimPrefix(kv, "PATH=")
			if existing == "" {
				out = append(out, "PATH="+dir)
			} else {
				out = append(out, "PATH="+dir+string(os.PathListSeparator)+existing)
			}
			continue
		}
		out = append(out, kv)
	}
	if !found {
		out = append(out, "PATH="+dir)
	}
	return out
}

// applyNodeResolution prepends the resolved Node bin dir to env's PATH when a
// directory was resolved (the nvm path). It returns env unchanged otherwise,
// along with the resolution source ("nvm", "path", or "none") for logging.
func applyNodeResolution(env []string) ([]string, string) {
	res := resolveNodeBinDir(osNodeEnv{})
	if res.Dir != "" {
		env = prependPATH(env, res.Dir)
	}
	return env, res.Source
}
