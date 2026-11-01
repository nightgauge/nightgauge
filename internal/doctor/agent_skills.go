package doctor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nightgauge/nightgauge/internal/skillrender"
)

// Codex and Grok skills that differ from this build (#2739).
//
// Those agents read SKILL.md straight from their home skills directory, so a
// copy an install skipped keeps running old instructions in every project and
// nothing else says so. This compares metadata.version, not bytes: installed
// copies differ from the build's mirror by `name:`, disable-model-invocation
// and rebased links, so bytes would flag every skill. An edit with no version
// bump is therefore not seen. It is a warning because skills halt on doctor
// exit 2, and a stale skill does not stop a stage from running.

const (
	agentSkillsCheck = "agent_skills_drift"
	agentSkillsCode  = "NGD112"

	agentSkillsMarker = ".nightgauge-installed"
	agentSkillsPrefix = "nightgauge-"
	agentSkillsShared = "_shared"
	agentSkillsEntry  = "nightgauge-issue-pickup"
)

// agentSkillsUserHome is the check's reach into the machine. A variable so a
// test can supply it.
var agentSkillsUserHome = os.UserHomeDir

func init() {
	builtinChecks = append(builtinChecks, Check{
		ID: agentSkillsCheck, Title: "Codex and Grok skills", Group: "adapters", Code: agentSkillsCode,
		Run: func(_ context.Context, env *Env) []Finding {
			fs, detail := agentSkillsFindings(env.Cwd)
			env.SetDetail(agentSkillsCheck, detail)
			return fs
		},
	})
}

// agentSkillsHome is one agent's home skills directory.
type agentSkillsHome struct {
	agent  string
	skills string
}

// agentSkillsHomes honours CODEX_HOME and GROK_HOME, as the extension does.
func agentSkillsHomes() ([]agentSkillsHome, error) {
	var homes []agentSkillsHome
	for _, h := range []struct{ agent, env, dir string }{
		{"Codex", "CODEX_HOME", ".codex"},
		{"Grok", "GROK_HOME", ".grok"},
	} {
		base := os.Getenv(h.env)
		if base == "" {
			user, err := agentSkillsUserHome()
			if err != nil {
				return nil, err
			}
			base = filepath.Join(user, h.dir)
		}
		homes = append(homes, agentSkillsHome{h.agent, filepath.Join(base, "skills")})
	}
	return homes, nil
}

func agentSkillsFindings(startDir string) ([]Finding, string) {
	homes, err := agentSkillsHomes()
	if err != nil {
		return []Finding{unverifiableFinding(agentSkillsCheck, agentSkillsCode, SeverityWarning,
			"Codex and Grok skills", "no home directory: "+err.Error())}, "could not find a home directory"
	}
	return agentSkillsFindingsIn(homes, skillrender.DefaultRoots(repoRootOf(startDir)))
}

func agentSkillsFindingsIn(homes []agentSkillsHome, roots []string) ([]Finding, string) {
	// DefaultRoots ends with <workspace>/skills, which in another project is
	// that project's own tree: comparing with it would call every installed
	// skill retired. The entry skill tells a Nightgauge tree from a stranger.
	var usable []string
	for _, root := range roots {
		if _, err := os.Stat(filepath.Join(root, agentSkillsEntry, "SKILL.md")); err == nil {
			usable = append(usable, root)
		}
	}
	if len(usable) == 0 {
		return nil, "skipped: no skills tree to compare with"
	}

	var findings []Finding
	var details []string
	for _, home := range homes {
		entries, err := os.ReadDir(home.skills)
		if errors.Is(err, os.ErrNotExist) {
			details = append(details, home.agent+": no skills directory")
			continue
		}
		if err != nil {
			findings = append(findings, unverifiableFinding(agentSkillsCheck, agentSkillsCode, SeverityWarning,
				home.agent+" skills", err.Error()))
			details = append(details, home.agent+": unreadable")
			continue
		}
		before := len(findings)
		checked := 0
		for _, entry := range entries {
			dir := filepath.Join(home.skills, entry.Name())
			if !entry.IsDir() || entry.Name() == agentSkillsShared {
				continue
			}
			marked := agentSkillMarked(dir)
			if !marked && !strings.HasPrefix(entry.Name(), agentSkillsPrefix) {
				continue
			}
			checked++
			if f := agentSkillFinding(home.agent, dir, entry.Name(), marked, usable); f != nil {
				findings = append(findings, *f)
			}
		}
		details = append(details, fmt.Sprintf("%s: %d checked, %d differ", home.agent, checked, len(findings)-before))
	}
	return findings, strings.Join(details, "; ")
}

// agentSkillFinding compares one installed directory with the build.
func agentSkillFinding(agent, dir, name string, marked bool, roots []string) *Finding {
	installed := agentSkillVersion(filepath.Join(dir, "SKILL.md"))
	build, shipped := agentSkillReference(name, roots)
	evidence := map[string]string{"path": dir, "installed": installed, "build": build}
	identity := []string{agent, name}
	finding := func(title, cause string, remedy Remedy) *Finding {
		f := newFinding(agentSkillsCheck, agentSkillsCode, SeverityWarning, title, cause,
			evidence, identity, remedy)
		return &f
	}
	label := fmt.Sprintf("%s skill %s", agent, name)
	switch {
	case !shipped && marked:
		return finding(label+" is retired", "this build has no such skill, so "+dir+" is leftover",
			agentSkillsInstallRemedy("reinstall", "", "Or remove the directory"))
	case !shipped:
		return finding(label+" is retired",
			"this build has no such skill, and "+dir+" has no install marker, so no install removes it",
			manualRemedy("remove", "Remove the directory", agentSkillsCheck, "Remove "+dir))
	case !marked:
		return finding(label+" has no install marker",
			fmt.Sprintf("every install skips %s, so it stays at version %s (build %s)",
				dir, agentSkillVersionText(installed), agentSkillVersionText(build)),
			agentSkillsInstallRemedy("adopt", " --adopt", "Or remove the directory and install again"))
	case installed != build:
		return finding(label+" differs from this build",
			fmt.Sprintf("%s is version %s, this build ships %s",
				dir, agentSkillVersionText(installed), agentSkillVersionText(build)),
			agentSkillsInstallRemedy("reinstall", "", "Or reinstall the skills from the VS Code extension"))
	}
	return nil
}

func agentSkillsInstallRemedy(id, flags, alternative string) Remedy {
	return manualRemedy(id, "Refresh the installed skills", agentSkillsCheck,
		"From the primary checkout, run `bash scripts/install-agent-skills.sh"+flags+"`", alternative)
}

func agentSkillMarked(dir string) bool {
	info, err := os.Lstat(filepath.Join(dir, agentSkillsMarker))
	return err == nil && info.Mode().IsRegular()
}

// agentSkillReference finds name in the first root that holds it, canonically
// or in the plugin mirror beside that root, which stores it without the
// prefix. The extension's dist/skills is a pipeline subset, so the mirror is
// what covers the rest of an installed build.
func agentSkillReference(name string, roots []string) (version string, shipped bool) {
	short := strings.TrimPrefix(name, agentSkillsPrefix)
	for _, root := range roots {
		for _, path := range []string{
			filepath.Join(root, name, "SKILL.md"),
			filepath.Join(filepath.Dir(root), "claude-plugins", "nightgauge", "skills", short, "SKILL.md"),
		} {
			if _, err := os.Stat(path); err == nil {
				return agentSkillVersion(path), true
			}
		}
	}
	return "", false
}

// agentSkillVersion is the frontmatter's metadata.version; empty when the file
// or the value is missing or unreadable.
func agentSkillVersion(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	rest, ok := bytes.CutPrefix(data, []byte("---\n"))
	if !ok {
		return ""
	}
	front, _, ok := bytes.Cut(rest, []byte("\n---"))
	if !ok {
		return ""
	}
	var parsed struct {
		Metadata struct {
			Version string `yaml:"version"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal(front, &parsed); err != nil {
		return ""
	}
	return parsed.Metadata.Version
}

func agentSkillVersionText(version string) string {
	if version == "" {
		return "(none)"
	}
	return version
}
