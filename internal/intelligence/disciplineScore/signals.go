package disciplineScore

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// skipDirs are never scanned for test files — vendored / generated trees would
// otherwise dominate and slow the walk.
var skipDirs = map[string]struct{}{
	".git": {}, "node_modules": {}, "vendor": {}, "dist": {}, "build": {},
	".worktrees": {}, "coverage": {}, ".next": {}, "target": {},
}

// errFound short-circuits the WalkDir once a test file is seen.
var errFound = errors.New("found")

// GatherSignals reads the deterministic discipline signals from a repo root.
func GatherSignals(root string) DisciplineInput {
	return DisciplineInput{
		HasTestFiles:          hasTestFiles(root),
		TestCommandConfigured: hasTestCommand(root),
		CIWorkflowCount:       ciWorkflowCount(root),
		HasIssueTemplates:     hasIssueTemplates(root),
		HasProcessDocs:        anyFileExists(root, "CONTRIBUTING.md", "AGENTS.md", "CLAUDE.md"),
	}
}

func hasTestFiles(root string) bool {
	found := false
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if _, skip := skipDirs[d.Name()]; skip {
				return fs.SkipDir
			}
			return nil
		}
		if isTestFileName(d.Name()) {
			found = true
			return errFound
		}
		return nil
	})
	return found
}

func isTestFileName(name string) bool {
	switch {
	case strings.HasSuffix(name, "_test.go"):
		return true
	case strings.HasSuffix(name, ".test.ts"), strings.HasSuffix(name, ".test.tsx"),
		strings.HasSuffix(name, ".test.js"), strings.HasSuffix(name, ".spec.ts"),
		strings.HasSuffix(name, ".spec.js"):
		return true
	case strings.HasSuffix(name, "_test.py"), strings.HasPrefix(name, "test_") && strings.HasSuffix(name, ".py"):
		return true
	case strings.HasSuffix(name, "Test.java"), strings.HasSuffix(name, "_spec.rb"):
		return true
	}
	return false
}

func hasTestCommand(root string) bool {
	if fileExists(filepath.Join(root, "go.mod")) {
		return true
	}
	// package.json scripts.test
	if data, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		if json.Unmarshal(data, &pkg) == nil {
			if t, ok := pkg.Scripts["test"]; ok && strings.TrimSpace(t) != "" {
				return true
			}
		}
	}
	// Makefile test: target
	if data, err := os.ReadFile(filepath.Join(root, "Makefile")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "test:") {
				return true
			}
		}
	}
	// pyproject / pytest config
	return anyFileExists(root, "pytest.ini", "tox.ini") ||
		fileExists(filepath.Join(root, "pyproject.toml"))
}

func ciWorkflowCount(root string) int {
	n := 0
	wfDir := filepath.Join(root, ".github", "workflows")
	if entries, err := os.ReadDir(wfDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && (strings.HasSuffix(e.Name(), ".yml") || strings.HasSuffix(e.Name(), ".yaml")) {
				n++
			}
		}
	}
	if fileExists(filepath.Join(root, ".gitlab-ci.yml")) {
		n++
	}
	return n
}

func hasIssueTemplates(root string) bool {
	if info, err := os.Stat(filepath.Join(root, ".github", "ISSUE_TEMPLATE")); err == nil && info.IsDir() {
		return true
	}
	return anyFileExists(filepath.Join(root, ".github"), "ISSUE_TEMPLATE.md")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func anyFileExists(dir string, names ...string) bool {
	for _, n := range names {
		if fileExists(filepath.Join(dir, n)) {
			return true
		}
	}
	return false
}
