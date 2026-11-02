package ci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// runNameCarriesCommitMessage reports whether a workflow's run-name reads
// head_commit.message, which puts a whole commit message in the run title (#2846).
func runNameCarriesCommitMessage(content []byte) (bool, error) {
	var wf struct {
		RunName string `yaml:"run-name"`
	}
	if err := yaml.Unmarshal(content, &wf); err != nil {
		return false, err
	}
	return strings.Contains(wf.RunName, "head_commit.message"), nil
}

func TestRunNameCarriesCommitMessage(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want bool
	}{
		{"merge group message", "name: CI\nrun-name: ${{ github.event.merge_group.head_commit.message }}\n", true},
		{"push message", "run-name: \"Run: ${{ github.event.head_commit.message }}\"\n", true},
		{"other expression", "run-name: ${{ github.actor }} deploy\n", false},
		{"no run-name", "name: CI\non: push\n", false},
		{"only a comment", "# run-name: ${{ github.event.head_commit.message }}\nname: CI\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := runNameCarriesCommitMessage([]byte(c.yaml))
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestWorkflows_RunNameNeverUsesCommitMessage(t *testing.T) {
	root := repoRootFromTest(t)
	var scanned int
	for _, pattern := range []string{"*.yml", "*.yaml"} {
		files, err := filepath.Glob(filepath.Join(root, ".github", "workflows", pattern))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			content, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			scanned++
			bad, err := runNameCarriesCommitMessage(content)
			if err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			if bad {
				t.Errorf("%s: run-name uses head_commit.message", filepath.Base(file))
			}
		}
	}
	if scanned == 0 {
		t.Fatal("no workflows scanned: this guard would pass on an empty directory")
	}
}

// repoRootFromTest walks up to the directory holding go.mod.
func repoRootFromTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}
