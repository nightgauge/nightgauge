package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	workspace "github.com/nightgauge/nightgauge/internal/knowledge/workspace"
	yaml "gopkg.in/yaml.v3"
)

// RepoProjectSource names WHY a board number was chosen. Every resolution
// carries one, because "which board does repo X use?" has two legitimate
// answers with different authority and different consequences, and a caller
// must be able to tell them apart without re-deriving the lookup.
//
// Before #313 they were two separate lookups that disagreed: this resolver
// refused to answer for an unmapped sibling while the autonomous scheduler
// built its repo set from the bare top-level project number and polled that
// board for every repo. Both behaviors were individually defensible and
// nothing reconciled them, so any diagnostic written against one stated false
// conclusions about the other — #280 shipped a doctor message claiming "the
// scheduler polls no board for this repo", derived from the refusal and simply
// untrue. The reason code is what makes the two policies expressible against
// one lookup instead of two.
type RepoProjectSource string

const (
	// RepoProjectLocalConfig: the target IS this config's own repo, so
	// cfg.ProjectNumber answers for it (or the caller's SharedBoard override,
	// which is what keeps `--project N` meaningful in a single-repo workspace).
	RepoProjectLocalConfig RepoProjectSource = "local-config"

	// RepoProjectExplicitMapping: autonomous.repositories.<repo>.project_number
	// in this config named the board. This is an operator override and outranks
	// what the target repo says about itself.
	RepoProjectExplicitMapping RepoProjectSource = "explicit-mapping"

	// RepoProjectMemberConfig: the target repo's own .nightgauge/config.yaml
	// declared project.number. Reading it is not guessing — the repo said so.
	RepoProjectMemberConfig RepoProjectSource = "member-config"

	// RepoProjectSharedBoardDefault: nothing declared a board for this repo, so
	// the workspace-wide default stands in. Correct for a shared-board
	// workspace and correct for the scheduler to poll; NEVER good enough for a
	// caller that files something (see Declared).
	RepoProjectSharedBoardDefault RepoProjectSource = "shared-board-default"

	// RepoProjectUnmapped: nothing declared a board and there is no
	// workspace-wide default either. Number is 0. No caller can act on this.
	RepoProjectUnmapped RepoProjectSource = "unmapped"
)

// RepoProject is the single answer to "which board does repo X use?".
type RepoProject struct {
	// Repo is the "owner/name" the question was asked about.
	Repo string
	// Number is the resolved board. 0 exactly when Source is
	// RepoProjectUnmapped.
	Number int
	// Source is why Number was chosen. Never empty.
	Source RepoProjectSource
}

// Declared reports whether some config actually named this board for this repo,
// as opposed to the workspace-wide default standing in for a repo that named
// nothing.
//
// This predicate IS the filing policy. #3232 was a cross-repo issue silently
// filed onto the primary board because the lookup defaulted, so every caller
// that WRITES to a board (issue-create, board sync, `nightgauge project
// resolve`) must require Declared() and fail loudly otherwise. Callers that
// only READ a board — the autonomous scheduler scanning for ready work — are
// free to use the default: scanning the wrong board wastes a poll, it does not
// misfile anything.
func (r RepoProject) Declared() bool {
	switch r.Source {
	case RepoProjectLocalConfig, RepoProjectExplicitMapping, RepoProjectMemberConfig:
		return true
	default:
		return false
	}
}

// RepoProjectQuery is everything ResolveRepoProject may consult about a target
// repo. Only Owner and Repo are required.
type RepoProjectQuery struct {
	// Owner and Repo name the target. Repo may be a bare slug or "owner/name";
	// when it carries an owner, that owner wins over the Owner field.
	Owner string
	Repo  string

	// SharedBoard is the workspace-wide board a repo falls back to when nothing
	// declares one for it — the `--project` flag, or cfg.ProjectNumber when the
	// flag was not given. Left at 0, cfg.ProjectNumber is used.
	SharedBoard int

	// MemberRoot is the target repo's checkout root when the caller already
	// knows it (the scheduler walking a workspace manifest always does).
	MemberRoot string

	// StartDir makes the resolver find MemberRoot itself, by walking up to the
	// workspace root and matching the target against the manifest's
	// repositories[] entries. Ignored when MemberRoot is set; empty means no
	// member lookup at all.
	StartDir string
}

// ResolveRepoProject answers "which board does repo X use?" — the one
// implementation, shared by the CLI (`nightgauge project resolve --repo`,
// `nightgauge project add`), the autonomous scheduler's repo-set construction,
// internal/doctor, and the stranded-ready sweep (#271, #313). It always
// answers; the reason code, not an error, is how a caller learns the answer was
// a default rather than a declaration.
//
// Precedence, strongest first:
//
//  1. The target is this config's own repo — cfg answers for itself.
//  2. autonomous.repositories.<repo>.project_number — an operator override.
//  3. The target repo's own .nightgauge/config.yaml project.number.
//  4. The workspace-wide SharedBoard.
//  5. Nothing — RepoProjectUnmapped, Number 0.
//
// The workspace manifest's repositories[].project_number is deliberately NOT a
// step. It is Source A in the doctor cross-check below, validated AGAINST this
// resolver's answer; feeding it back in would make that check compare a value
// to itself.
func ResolveRepoProject(cfg *Config, q RepoProjectQuery) RepoProject {
	ownerPart, repoPart := q.Owner, q.Repo
	if idx := strings.Index(q.Repo, "/"); idx >= 0 {
		ownerPart, repoPart = q.Repo[:idx], q.Repo[idx+1:]
	}
	target := ownerPart + "/" + repoPart

	// A board number of 0 is not an answer whatever produced it, so every
	// return funnels through here and degrades to unmapped rather than handing
	// back a zero the caller would spend a network call discovering.
	answer := func(n int, src RepoProjectSource) RepoProject {
		if n <= 0 {
			return RepoProject{Repo: target, Source: RepoProjectUnmapped}
		}
		return RepoProject{Repo: target, Number: n, Source: src}
	}

	shared := q.SharedBoard
	if shared <= 0 && cfg != nil {
		shared = cfg.ProjectNumber
	}

	// cfg is nil for a manifest-only workspace root — no local repo and no
	// explicit mappings, but the member configs below are still authoritative.
	if cfg != nil {
		if cfg.DefaultRepo != "" && target == cfg.Owner+"/"+cfg.DefaultRepo {
			return answer(shared, RepoProjectLocalConfig)
		}
		if cfg.Autonomous != nil {
			if rc := cfg.Autonomous.Repositories[target]; rc != nil && rc.ProjectNumber > 0 {
				return answer(rc.ProjectNumber, RepoProjectExplicitMapping)
			}
			if rc := cfg.Autonomous.Repositories[repoPart]; rc != nil && rc.ProjectNumber > 0 {
				return answer(rc.ProjectNumber, RepoProjectExplicitMapping)
			}
		}
	}

	if root := memberRootFor(q, repoPart, target); root != "" {
		if memberCfg, err := Load(root); err == nil && memberCfg != nil && memberCfg.ProjectNumber > 0 {
			return answer(memberCfg.ProjectNumber, RepoProjectMemberConfig)
		}
	}

	// A config with no repo of its own cannot tell the target apart from
	// itself, so its board is the best declaration on offer. #262 established
	// this as a pass rather than a refusal, and a filing caller relies on it —
	// but it is a guess about identity, so it is tried only after every real
	// declaration above has come up empty.
	if cfg != nil && cfg.DefaultRepo == "" {
		return answer(shared, RepoProjectLocalConfig)
	}

	return answer(shared, RepoProjectSharedBoardDefault)
}

// memberRootFor returns the target repo's checkout root: the caller's explicit
// MemberRoot, else the workspace manifest entry matching the target by name.
// Returns "" when neither is available — the caller then falls back to the
// shared board rather than failing.
func memberRootFor(q RepoProjectQuery, repoPart, target string) string {
	if q.MemberRoot != "" {
		return q.MemberRoot
	}
	if q.StartDir == "" {
		return ""
	}
	wsRoot, err := workspace.DetectWorkspaceRoot(q.StartDir)
	if err != nil {
		return ""
	}
	manifest, err := readWorkspaceManifest(wsRoot)
	if err != nil {
		return ""
	}
	for _, r := range manifest {
		if r.Path == "" {
			continue
		}
		if r.Name == repoPart || r.Name == target {
			return filepath.Join(wsRoot, r.Path)
		}
	}
	return ""
}

// ResolveRepoProjectNumber is ResolveRepoProject under the FILING policy: only
// a board some config declared for this repo counts as an answer, and anything
// else is an error naming the exact config path to fix. Callers that write to a
// board use this; callers that merely poll one use ResolveRepoProject directly
// and read the reason code.
func ResolveRepoProjectNumber(cfg *Config, q RepoProjectQuery) (int, error) {
	if cfg == nil {
		return 0, fmt.Errorf("no config loaded")
	}
	res := ResolveRepoProject(cfg, q)
	if res.Declared() {
		return res.Number, nil
	}
	repoPart := res.Repo
	if idx := strings.LastIndex(res.Repo, "/"); idx >= 0 {
		repoPart = res.Repo[idx+1:]
	}
	switch {
	case res.Source == RepoProjectSharedBoardDefault:
		return 0, fmt.Errorf("no project board mapping for --repo %s (nothing declares a board for it, so filing against the workspace default board %d would be a guess — set autonomous.repositories.%s.project_number in .nightgauge/config.yaml, or project.number in that repo's own .nightgauge/config.yaml)", res.Repo, res.Number, repoPart)
	case cfg.DefaultRepo == "" || res.Repo == cfg.Owner+"/"+cfg.DefaultRepo:
		// The local repo resolved to nothing: its own config has no board.
		// Pointing at autonomous.repositories.<self> would be the wrong fix.
		return 0, fmt.Errorf("no project board configured for %s (set project.number in .nightgauge/config.yaml)", res.Repo)
	default:
		return 0, fmt.Errorf("no project board mapping for --repo %s (configure autonomous.repositories.%s.project_number in .nightgauge/config.yaml)", res.Repo, repoPart)
	}
}

// workspaceManifestRepo is the subset of a .vscode/nightgauge-workspace.yaml
// repositories[] entry this package reads: its declared project_number
// (Source A of the doctor cross-check) and the path used to find that repo's
// own config (an input to ResolveRepoProject).
type workspaceManifestRepo struct {
	Name          string `yaml:"name"`
	Path          string `yaml:"path"`
	ProjectNumber int    `yaml:"project_number"`
}

// readWorkspaceManifest reads the repositories[] entries of the workspace
// manifest at wsRoot. Returns an error when the manifest is absent or
// unparseable — single-repo mode is the common cause and is not a failure.
func readWorkspaceManifest(wsRoot string) ([]workspaceManifestRepo, error) {
	data, err := os.ReadFile(filepath.Join(wsRoot, ".vscode", "nightgauge-workspace.yaml"))
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Repositories []workspaceManifestRepo `yaml:"repositories"`
	}
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	return manifest.Repositories, nil
}

// ProjectMappingMismatch is one repo whose workspace-manifest project_number
// (Source A) disagrees with the runtime-resolved project_number (Source B).
type ProjectMappingMismatch struct {
	// Repo is the manifest entry's repositories[].name, verbatim.
	Repo string
	// ManifestProject is the workspace-manifest project_number (Source A).
	ManifestProject int
	// ResolvedProject is the runtime-resolved project_number (Source B).
	ResolvedProject int
	// ResolvedSource is where Source B's answer came from, so the message can
	// name the file to edit instead of leaving the operator to guess which of
	// two configs is the one that disagrees.
	ResolvedSource RepoProjectSource
}

// String renders the standard human-readable mismatch message shared by
// doctor and the scheduler startup warning.
func (m ProjectMappingMismatch) String() string {
	return fmt.Sprintf(
		"project mapping mismatch for %s: workspace yaml says project %d, runtime config resolves to %d (%s) — see nightgauge doctor",
		m.Repo, m.ManifestProject, m.ResolvedProject, m.ResolvedSource)
}

// ProjectMappingUnresolved is one manifest repo whose declared project_number
// could not be checked against anything, because no config declares a board for
// it. This is NOT a clean bill of health — the comparison never happened — but
// it is also not a total outage, and the distinction matters (#280).
//
// The refusal exists on purpose: defaulting a cross-repo target to the primary
// board silently misrouted new issues (#3232), so every caller that FILES
// something (issue-create, board sync, `nightgauge project resolve`) fails
// loudly instead of guessing.
//
// The autonomous scheduler resolves through the same lookup (#313) but is free
// to act on RepoProjectSharedBoardDefault, because polling the wrong board
// wastes a scan rather than misfiling an issue. Fallback records the board it
// will actually poll, so this message can state both consequences without
// either caller asserting anything about the other — the #280 message claimed
// "the scheduler polls no board for this repo" and was simply false.
type ProjectMappingUnresolved struct {
	// Repo is the manifest entry's repositories[].name, verbatim.
	Repo string
	// ManifestProject is the workspace-manifest project_number (Source A).
	ManifestProject int
	// Fallback is the workspace-wide board the scheduler polls for this repo in
	// the absence of a declaration. 0 when nothing at all is configured.
	Fallback int
	// Err is the resolver's own message, carried rather than discarded.
	Err string
}

// String renders the human-readable message for a repo no config declares a
// board for. It states the consequence for both kinds of caller — see the type
// comment for why saying less was how the previous version said something
// untrue.
func (u ProjectMappingUnresolved) String() string {
	if u.Fallback > 0 {
		return fmt.Sprintf(
			"project mapping unverifiable for %s: workspace yaml says project %d, but no config declares a board for it (%s) — the manifest value is unchecked, the autonomous scheduler falls back to the workspace default board %d, and issue creation or board sync targeting this repo will refuse to guess until autonomous.repositories.%s.project_number (or project.number in that repo's own .nightgauge/config.yaml) is set",
			u.Repo, u.ManifestProject, u.Err, u.Fallback, u.Repo)
	}
	return fmt.Sprintf(
		"project mapping unverifiable for %s: workspace yaml says project %d, but no config declares a board for it and there is no workspace default either (%s) — the manifest value is unchecked, and every board operation targeting this repo will fail until autonomous.repositories.%s.project_number (or project.number in that repo's own .nightgauge/config.yaml) is set",
		u.Repo, u.ManifestProject, u.Err, u.Repo)
}

// ProjectMappingReport is the full result of the manifest cross-check. It has
// two populated fields on purpose: "the two sources disagree" and "one source
// is missing entirely" are different conditions with different fixes, and
// collapsing the second into silence is what let a workspace report
// "manifest and runtime config agree" while three of its four repos had no
// runtime mapping at all (#280).
type ProjectMappingReport struct {
	// Mismatches are repos where both sources answered and disagreed.
	Mismatches []ProjectMappingMismatch
	// Unresolvable are repos where Source B could not answer. Never treat an
	// entry here as agreement.
	Unresolvable []ProjectMappingUnresolved
}

// OK reports whether the cross-check found nothing wrong. An unresolvable repo
// counts as wrong: it means the comparison never happened.
func (r ProjectMappingReport) OK() bool {
	return len(r.Mismatches) == 0 && len(r.Unresolvable) == 0
}

// Problems renders every finding as a flat list of human-readable lines, in a
// stable order (mismatches first, then unresolvable).
func (r ProjectMappingReport) Problems() []string {
	out := make([]string, 0, len(r.Mismatches)+len(r.Unresolvable))
	for _, m := range r.Mismatches {
		out = append(out, m.String())
	}
	for _, u := range r.Unresolvable {
		out = append(out, u.String())
	}
	return out
}

// FindWorkspaceProjectMappingMismatches cross-validates every
// repositories[].project_number entry in the .vscode/nightgauge-workspace.yaml
// manifest (walked up from startDir) against ResolveRepoProject for that same
// repo, returning a report of every disagreeing repo AND every repo the check
// could not evaluate. Shared by internal/doctor's project_mapping check, the
// orchestrator scheduler's startup warning, and the stranded-ready-items sweep
// producer (#271) so there is exactly one implementation of the cross-check.
// Returns a non-nil error only when no workspace manifest exists (single-repo
// mode — not a failure, just "nothing to check").
//
// Each entry is resolved against its OWN checkout (manifest path → that repo's
// .nightgauge/config.yaml), so Source A and Source B stay genuinely independent
// files. The manifest's own project_number is never fed back into the resolver;
// that would make this check compare a value to itself.
//
// An unresolvable repo is reported, never skipped (#280). The previous version
// dropped it with a comment claiming the resolver's own error surfaced it, but
// that error was discarded on the same line and nothing downstream ever saw
// it — so a workspace whose siblings had NO runtime mapping reported
// "manifest and runtime config agree", which doctor rendered as a green check.
func FindWorkspaceProjectMappingMismatches(cfg *Config, startDir string) (ProjectMappingReport, error) {
	if cfg == nil {
		return ProjectMappingReport{}, fmt.Errorf("no config loaded")
	}
	wsRoot, err := workspace.DetectWorkspaceRoot(startDir)
	if err != nil {
		return ProjectMappingReport{}, err
	}
	repos, err := readWorkspaceManifest(wsRoot)
	if err != nil {
		return ProjectMappingReport{}, err
	}

	var report ProjectMappingReport
	for _, r := range repos {
		if r.ProjectNumber == 0 || r.Name == "" {
			continue
		}
		ownerPart, repoPart := r.Name, r.Name
		if idx := strings.Index(r.Name, "/"); idx >= 0 {
			ownerPart, repoPart = r.Name[:idx], r.Name[idx+1:]
		} else if cfg.Owner != "" {
			ownerPart = cfg.Owner
		}
		q := RepoProjectQuery{Owner: ownerPart, Repo: repoPart}
		if r.Path != "" {
			q.MemberRoot = filepath.Join(wsRoot, r.Path)
		}
		resolved := ResolveRepoProject(cfg, q)
		if !resolved.Declared() {
			// Report it. "I could not compare" is not "they agree" — see the
			// function comment.
			errText := ""
			if _, resolveErr := ResolveRepoProjectNumber(cfg, q); resolveErr != nil {
				errText = resolveErr.Error()
			}
			report.Unresolvable = append(report.Unresolvable, ProjectMappingUnresolved{
				Repo:            r.Name,
				ManifestProject: r.ProjectNumber,
				Fallback:        resolved.Number,
				Err:             errText,
			})
			continue
		}
		if resolved.Number != r.ProjectNumber {
			report.Mismatches = append(report.Mismatches, ProjectMappingMismatch{
				Repo:            r.Name,
				ManifestProject: r.ProjectNumber,
				ResolvedProject: resolved.Number,
				ResolvedSource:  resolved.Source,
			})
		}
	}
	return report, nil
}

// CheckWorkspaceProjectMapping is FindWorkspaceProjectMappingMismatches
// rendered as human-readable strings, for callers (doctor, scheduler) that
// only need the message. Includes unresolvable repos, so a caller that only
// prints these lines still cannot report a false all-clear.
func CheckWorkspaceProjectMapping(cfg *Config, startDir string) ([]string, error) {
	report, err := FindWorkspaceProjectMappingMismatches(cfg, startDir)
	if err != nil {
		return nil, err
	}
	return report.Problems(), nil
}
