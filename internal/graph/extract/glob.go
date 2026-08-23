package extract

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// trackedFiles returns every path git tracks under root, slash-separated.
//
// Each extractor calls this for itself rather than sharing one cached list.
// That is deliberate: ADR-005's extractors are meant to be independently
// verifiable, and a shared mutable list is the kind of coupling that makes one
// extractor's golden test depend on another's having run. The cost is a second
// `git ls-files`, which is milliseconds.
func trackedFiles(root string) ([]string, error) {
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, raw := range strings.Split(string(out), "\x00") {
		p := strings.TrimSpace(raw)
		if p != "" {
			files = append(files, filepath.ToSlash(p))
		}
	}
	return files, nil
}

// matchGlob returns the tracked files a capability `owns` pattern covers.
//
// Matching happens against git's TRACKED list rather than the filesystem, which
// is where this differs from capabilities.countMatches. countMatches answers
// "does this glob own anything on disk", and walking the filesystem is right
// for that. The graph needs "which file NODES does this glob own", and the file
// nodes come from git ls-files — so matching the filesystem would produce edges
// pointing at build output and gitignored artifacts that have no node.
//
// Semantics, matching the registry's documented intent:
//
//	**  crosses directory separators
//	*   does not
//	?   one non-separator character
func matchGlob(files []string, pattern string) []string {
	re, err := globToRegexp(pattern)
	if err != nil {
		return nil
	}
	var out []string
	for _, f := range files {
		if re.MatchString(f) {
			out = append(out, f)
		}
	}
	return out
}

func globToRegexp(pattern string) (*regexp.Regexp, error) {
	p := filepath.ToSlash(pattern)
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case '*':
			if i+1 < len(p) && p[i+1] == '*' {
				i++
				// "a/**" must also match "a" itself having no deeper entries?
				// No: an owns glob names files, so "a/**" means "everything
				// beneath a". Trailing-slash handling falls out of the prefix.
				b.WriteString(".*")
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(p[i])))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}
