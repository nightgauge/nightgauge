package skillrender

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SkillPath is one skill path a rendered prompt names.
type SkillPath struct {
	// Path is the text as it appears in the prompt.
	Path string
	// Resolved is the file the path reaches. It is empty for a residual
	// skills/... path, which resolves against a worktree and not the root.
	Resolved string
	// Exists reports whether Resolved is on disk.
	Exists bool
}

const skillPathTail = "[^\\s`'\"()\\[\\]]*"

var (
	residualSkillPathRE = regexp.MustCompile(`(^|[^/\w.-])(skills/(?:nightgauge-[a-z0-9-]+|_shared)/` + skillPathTail + `)`)
	relativeSkillPathRE = regexp.MustCompile("(^|[^/\\w.-])(\\.\\./_shared/" + skillPathTail + ")")
	includesSkillPathRE = regexp.MustCompile("(`)(_includes/" + skillPathTail + ")")
)

// SkillPaths returns each skill path content names, in order of first
// appearance, and whether it exists: absolute paths under root, residual
// skills/nightgauge-*/ and skills/_shared/ paths, backticked _includes/ paths,
// and relative ../_shared/ paths. The last two resolve against skillDir, as the
// composer resolves them. A residual path never counts as existing, however the
// current directory looks: it would resolve against the stage's worktree, which
// is the failure this reports (#196).
//
// A path holding a placeholder or a glob (`{`, `<`, `$`, `*`) names no file and
// is skipped. Paths outside the skill tree, such as ../../docs/, are not skill
// paths and are not returned.
func SkillPaths(content, skillDir, root string) []SkillPath {
	var out []SkillPath
	seen := map[string]bool{}
	add := func(raw string, resolve func(string) string) {
		path := strings.TrimRight(raw, ".,:;")
		if seen[path] || strings.ContainsAny(path, "{<$*") {
			return
		}
		seen[path] = true
		sp := SkillPath{Path: path}
		if resolve != nil {
			sp.Resolved = resolve(path)
			_, err := os.Stat(sp.Resolved)
			sp.Exists = err == nil
		}
		out = append(out, sp)
	}

	root = strings.TrimRight(root, "/\\")
	absolute := regexp.MustCompile(regexp.QuoteMeta(root+string(filepath.Separator)) + skillPathTail)
	for _, path := range absolute.FindAllString(content, -1) {
		add(path, func(p string) string { return p })
	}
	underSkill := func(p string) string { return filepath.Join(skillDir, p) }
	for _, m := range residualSkillPathRE.FindAllStringSubmatch(content, -1) {
		add(m[2], nil)
	}
	for _, m := range relativeSkillPathRE.FindAllStringSubmatch(content, -1) {
		add(m[2], underSkill)
	}
	for _, m := range includesSkillPathRE.FindAllStringSubmatch(content, -1) {
		add(m[2], underSkill)
	}
	return out
}
