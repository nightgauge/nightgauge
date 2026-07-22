package execution

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeNodeEnv struct {
	home      string
	files     map[string]bool
	pathNodes map[string]string // name -> resolved path; absence = not found
	nvmPath   string
	nvmErr    error
}

func (f fakeNodeEnv) Home() string             { return f.home }
func (f fakeNodeEnv) FileExists(p string) bool { return f.files[p] }

func (f fakeNodeEnv) LookPath(name string) (string, error) {
	if p, ok := f.pathNodes[name]; ok {
		return p, nil
	}
	return "", errors.New("executable not found in $PATH")
}

func (f fakeNodeEnv) NvmWhichDefault() (string, error) { return f.nvmPath, f.nvmErr }

func TestResolveNodeBinDir_NvmPresent_UsesDefaultAlias(t *testing.T) {
	home := "/home/runner"
	e := fakeNodeEnv{
		home:    home,
		files:   map[string]bool{filepath.Join(home, ".nvm", "nvm.sh"): true},
		nvmPath: "/home/runner/.nvm/versions/node/v24.3.0/bin/node\n",
	}
	res := resolveNodeBinDir(e)
	if res.Source != "nvm" {
		t.Fatalf("source = %q, want nvm", res.Source)
	}
	want := "/home/runner/.nvm/versions/node/v24.3.0/bin"
	if res.Dir != want {
		t.Fatalf("dir = %q, want %q", res.Dir, want)
	}
}

func TestResolveNodeBinDir_NoNvm_NodeOnPath(t *testing.T) {
	e := fakeNodeEnv{
		home:      "/home/runner",
		files:     map[string]bool{},
		pathNodes: map[string]string{"node": "/usr/local/bin/node"},
	}
	res := resolveNodeBinDir(e)
	if res.Source != "path" {
		t.Fatalf("source = %q, want path", res.Source)
	}
	if res.Dir != "" {
		t.Fatalf("dir = %q, want empty (PATH node is inherited, nothing to prepend)", res.Dir)
	}
}

func TestResolveNodeBinDir_Neither(t *testing.T) {
	e := fakeNodeEnv{home: "/home/runner", files: map[string]bool{}}
	res := resolveNodeBinDir(e)
	if res.Source != "none" {
		t.Fatalf("source = %q, want none", res.Source)
	}
	if res.Dir != "" {
		t.Fatalf("dir = %q, want empty", res.Dir)
	}
}

func TestResolveNodeBinDir_NvmPresentButDefaultErrors_FallsThroughToPath(t *testing.T) {
	home := "/home/runner"
	e := fakeNodeEnv{
		home:      home,
		files:     map[string]bool{filepath.Join(home, ".nvm", "nvm.sh"): true},
		nvmErr:    errors.New("exit status 3"),
		pathNodes: map[string]string{"node": "/usr/bin/node"},
	}
	res := resolveNodeBinDir(e)
	if res.Source != "path" {
		t.Fatalf("source = %q, want path (nvm default unusable → PATH fallback)", res.Source)
	}
}

func TestResolveNodeBinDir_NvmDefaultNonAbsolute_Ignored(t *testing.T) {
	home := "/home/runner"
	e := fakeNodeEnv{
		home:    home,
		files:   map[string]bool{filepath.Join(home, ".nvm", "nvm.sh"): true},
		nvmPath: `N/A: version "N/A -> N/A" is not yet installed`,
	}
	res := resolveNodeBinDir(e)
	if res.Source != "none" {
		t.Fatalf("source = %q, want none (non-path nvm output ignored, no PATH node)", res.Source)
	}
}

func TestPrependPATH_ExistingPATH(t *testing.T) {
	env := []string{"FOO=bar", "PATH=/usr/bin:/bin"}
	out := prependPATH(env, "/opt/node/bin")

	var gotPath string
	var fooSeen bool
	for _, kv := range out {
		if strings.HasPrefix(kv, "PATH=") {
			gotPath = strings.TrimPrefix(kv, "PATH=")
		}
		if kv == "FOO=bar" {
			fooSeen = true
		}
	}
	want := "/opt/node/bin" + string(os.PathListSeparator) + "/usr/bin:/bin"
	if gotPath != want {
		t.Fatalf("PATH = %q, want %q", gotPath, want)
	}
	if !fooSeen {
		t.Fatalf("unrelated env var FOO=bar was dropped")
	}
}

func TestPrependPATH_NoExistingPATH(t *testing.T) {
	out := prependPATH([]string{"FOO=bar"}, "/opt/node/bin")
	found := false
	for _, kv := range out {
		if kv == "PATH=/opt/node/bin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected PATH=/opt/node/bin to be added, got %v", out)
	}
}
