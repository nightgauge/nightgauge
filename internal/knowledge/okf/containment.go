package okf

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ValidateSource checks a `sources[].resource` value and returns the form to
// record. Three shapes are accepted, and nothing else:
//
//   - an https:// URL, recorded verbatim;
//   - a bundle-absolute path beginning with "/", recorded cleaned;
//   - a repository-relative path, which must resolve inside repoRoot after
//     cleaning and symlink evaluation, recorded slash-normalised and relative.
//
// A value that escapes the repository is rejected. Callers exit non-zero on a
// rejection having written nothing — a source is a claim about provenance, and
// a claim pointing outside the tree is either a mistake or an attempt to make
// an entry cite something the reader cannot check.
func ValidateSource(resource, repoRoot string) (string, error) {
	resource = strings.TrimSpace(resource)
	if resource == "" {
		return "", fmt.Errorf("source: empty resource")
	}

	if strings.Contains(resource, "://") {
		u, err := url.Parse(resource)
		if err != nil {
			return "", fmt.Errorf("source %q: %w", resource, err)
		}
		if u.Scheme != "https" || u.Host == "" {
			return "", fmt.Errorf("source %q: only https:// URLs are accepted", resource)
		}
		return resource, nil
	}

	if strings.HasPrefix(resource, "/") {
		// A leading "/" means bundle-absolute — a path within the exported
		// bundle, the form the OKF export emits. A host filesystem path looks
		// identical, so reject one that really exists outside the repository
		// rather than silently reinterpreting it as a bundle path and
		// recording a source that points at nothing.
		if _, err := os.Stat(resource); err == nil {
			if _, cerr := ContainedPath(resource, repoRoot); cerr != nil {
				return "", fmt.Errorf("source %q: absolute filesystem path outside the repository; "+
					"use an https:// URL, a repository-relative path, or a bundle-absolute path that is not also a host path", resource)
			}
		}
		return path.Clean(resource), nil
	}

	rel, err := ContainedPath(resource, repoRoot)
	if err != nil {
		return "", fmt.Errorf("source %q: %w", resource, err)
	}
	return rel, nil
}

// ContainedPath resolves candidate against root and returns the slash-separated
// relative path, or an error when the result escapes root. Symlinks are
// evaluated on both sides — a lexical check alone lets a symlink inside the
// tree point anywhere, and on macOS the root itself is usually a symlink
// (/var -> /private/var), which would make every honest path look like an
// escape.
func ContainedPath(candidate, root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("no root to resolve against")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absRoot = resolveExisting(absRoot)

	target := candidate
	if !filepath.IsAbs(target) {
		target = filepath.Join(absRoot, target)
	}
	target = resolveExisting(filepath.Clean(target))

	rel, err := filepath.Rel(absRoot, target)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("resolves to %s, outside %s", target, absRoot)
	}
	return filepath.ToSlash(rel), nil
}

// resolveExisting evaluates symlinks on the deepest existing ancestor of p and
// re-appends the remaining segments. filepath.EvalSymlinks fails outright on a
// path that does not exist yet, which is the common case for a source naming a
// file another branch adds.
func resolveExisting(p string) string {
	remainder := ""
	current := p
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			if remainder == "" {
				return resolved
			}
			return filepath.Join(resolved, remainder)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return p
		}
		remainder = filepath.Join(filepath.Base(current), remainder)
		current = parent
	}
}

// KnowledgeRoot is the directory every knowledge entry lives under, relative
// to a workspace root.
func KnowledgeRoot(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, ".nightgauge", "knowledge")
}

// ResolveEntryPath resolves a caller-supplied entry path against workspaceRoot
// and refuses anything outside the knowledge root.
//
// Every other knowledge verb derives its path internally from an issue number
// or a slug; the stamp verb is the first that takes one from the command line,
// so it is the first that could be pointed at an arbitrary file. Without this
// check `knowledge stamp ../../../.zshrc --status stable` prepends a YAML
// block to the operator's shell config.
func ResolveEntryPath(entryPath, workspaceRoot string) (string, error) {
	root := KnowledgeRoot(workspaceRoot)
	if _, err := os.Stat(root); err != nil {
		return "", fmt.Errorf("no knowledge base at %s", root)
	}

	candidate := entryPath
	if !filepath.IsAbs(candidate) {
		// A path may be given relative to the workspace root
		// (.nightgauge/knowledge/...) or relative to the knowledge root.
		fromWorkspace := filepath.Join(workspaceRoot, candidate)
		if _, err := os.Stat(fromWorkspace); err == nil {
			candidate = fromWorkspace
		} else {
			candidate = filepath.Join(root, candidate)
		}
	}

	if _, err := ContainedPath(candidate, root); err != nil {
		return "", fmt.Errorf("%s is outside the knowledge base at %s: %w", entryPath, root, err)
	}
	return filepath.Clean(candidate), nil
}
