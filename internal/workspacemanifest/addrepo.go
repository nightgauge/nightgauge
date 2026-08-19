package workspacemanifest

// The single guarded add path (#706).
//
// `workspace repo add` grew these guards first, but it is no longer the only
// surface that appends an entry: the coverage-gap card's repair verb (#706) and
// the Settings panel (#705) write the same file. Re-implementing the checks per
// surface is how one of them ends up writing `project_number: 0` — the exact
// value the manifest's own comment block warns about — so the checks live here
// and every surface calls this.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// WorkspaceRoot is the directory the manifest's relative paths resolve against:
// the parent of the .vscode/ directory holding the file.
func (m *Manifest) WorkspaceRoot() string {
	return filepath.Dir(filepath.Dir(m.path))
}

// ResolveEntryPath turns a manifest-relative path into an absolute one, using
// the same base every reader uses.
func (m *Manifest) ResolveEntryPath(rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(m.WorkspaceRoot(), rel)
}

// AddRepo validates e, resolves its project number when unset, appends the
// entry and writes the manifest.
//
// resolveProject is consulted ONLY when e.ProjectNumber is zero, and its result
// must be positive. A zero project number is refused from every path because it
// resolves to project 0 and silently misroutes every issue the repository
// produces — a failure with no error message and no bad state to notice.
//
// The manifest is written only if every guard passes; a rejected add leaves the
// file untouched. The returned Entry is the one actually written, so callers
// can report the project number the resolver chose.
func AddRepo(m *Manifest, e Entry, resolveProject func(name string) (int, error)) (Entry, error) {
	e.Name = strings.TrimSpace(e.Name)
	e.Path = strings.TrimSpace(e.Path)
	e.Role = strings.TrimSpace(e.Role)

	if e.Name == "" {
		return Entry{}, fmt.Errorf("repository name is required")
	}
	if e.Path == "" {
		return Entry{}, fmt.Errorf("repository path is required")
	}
	if e.Role != "" && !slices.Contains(ValidRoles, e.Role) {
		return Entry{}, fmt.Errorf("role must be one of: %s", strings.Join(ValidRoles, ", "))
	}
	if _, exists := m.Find(e.Name); exists {
		return Entry{}, fmt.Errorf("repository %q is already in the manifest — names must be unique", e.Name)
	}

	abs := m.ResolveEntryPath(e.Path)
	st, statErr := os.Stat(abs)
	if statErr != nil || !st.IsDir() {
		return Entry{}, fmt.Errorf("path %q does not resolve to a directory (looked at %s)", e.Path, abs)
	}
	if _, gitErr := os.Stat(filepath.Join(abs, ".git")); gitErr != nil {
		return Entry{}, fmt.Errorf("path %q is not a git repository: no .git found at %s", e.Path, abs)
	}

	if e.ProjectNumber == 0 {
		if resolveProject == nil {
			return Entry{}, fmt.Errorf("no project number given for %q and no resolver available", e.Name)
		}
		n, rerr := resolveProject(e.Name)
		if rerr != nil {
			return Entry{}, fmt.Errorf("no project could be resolved for %q: %w\n"+
				"Provision a board for this repository first, or supply the project number explicitly", e.Name, rerr)
		}
		e.ProjectNumber = n
	}
	if e.ProjectNumber <= 0 {
		return Entry{}, fmt.Errorf("project number must be positive: %d would resolve to project 0 and silently misroute issues", e.ProjectNumber)
	}

	if err := m.AddEntry(e); err != nil {
		return Entry{}, err
	}
	if err := m.Write(); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// DeriveEntry builds the entry for a repository that is PRESENT in the
// workspace but absent from the manifest — the coverage-gap card's exact
// subject.
//
// repoSpec is accepted as "owner/name" or bare "name"; only the name half names
// a directory. The path is "." when the repository IS the workspace root and
// "../<name>" otherwise, matching how every existing entry in a real manifest is
// written. It returns an error when no such directory exists, so a card naming a
// repository that has since been moved or deleted fails loudly instead of
// writing an entry that points nowhere.
func DeriveEntry(m *Manifest, repoSpec, role string) (Entry, error) {
	name := strings.TrimSpace(repoSpec)
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if name == "" {
		return Entry{}, fmt.Errorf("repository spec %q names no repository", repoSpec)
	}

	root := m.WorkspaceRoot()
	// The workspace root itself is a candidate: a manifest can legitimately
	// omit the repo it lives in.
	if filepath.Base(root) == name && isGitDir(root) {
		return Entry{Name: name, Path: ".", Role: role}, nil
	}
	sibling := filepath.Join(filepath.Dir(root), name)
	if isGitDir(sibling) {
		return Entry{Name: name, Path: filepath.Join("..", name), Role: role}, nil
	}
	return Entry{}, fmt.Errorf("no git checkout named %q found in this workspace (looked at %s and %s)", name, root, sibling)
}

func isGitDir(dir string) bool {
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}
