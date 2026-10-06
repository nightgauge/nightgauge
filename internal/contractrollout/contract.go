// Package contractrollout rolls one cross-repository contract out to every
// repository a contract manifest names (#1480): files copied byte for byte,
// labels provisioned, a CI job inserted, one pull request per repository
// after that repository's own local gate passes, and one status table for
// the whole rollout. Each pull request carries the contract's changelog
// entry, and is opened only after the target's own declared gate passed.
//
// Each primitive (CopyFiles, ProvisionLabels, InsertCIJob) works on a single
// repository and is tested on its own; Rollout composes them per target.
// The command is `nightgauge workspace contract`; the procedure and the
// manifest schema are in docs/MULTI_REPO_WORKSPACE.md § Contract rollout.
package contractrollout

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	yaml "gopkg.in/yaml.v3"
)

// Contract is a parsed contract manifest.
type Contract struct {
	// Name identifies the contract in branch names, commit messages and the
	// status table.
	Name string `yaml:"name"`
	// SourceRoot is where Files are read from, relative to the manifest's
	// own directory. Default: the manifest's directory.
	SourceRoot string `yaml:"source_root"`
	// Branch is the head branch each target's pull request is opened from.
	// Default: contract/<name>.
	Branch string `yaml:"branch"`
	// Base is the branch each target's pull request merges into. Default:
	// main. A target may override it.
	Base string `yaml:"base"`
	// CommitMessage is the one commit's message. Default:
	// "chore: adopt the <name> contract".
	CommitMessage string `yaml:"commit_message"`
	// PR is the pull request's title and per-repository body template.
	PR PRTemplate `yaml:"pr"`
	// Files are copied byte for byte from SourceRoot into every target.
	Files []File `yaml:"files"`
	// Labels are provisioned in every target repository.
	Labels []Label `yaml:"labels"`
	// CIJob, when set, is inserted into a workflow of every target.
	CIJob *CIJob `yaml:"ci_job"`
	// Changelog is the entry every target's CHANGELOG.md gets under
	// `## [Unreleased]`. Required when the contract has files or a CI job:
	// every target's changelog contract wants an entry for the change.
	Changelog *ChangelogEntry `yaml:"changelog"`
	// Targets are the repositories the contract rolls out to. A manifest may
	// leave them out and let the command take them from the workspace
	// manifest, so one contract serves any workspace.
	Targets []Target `yaml:"targets"`

	// dir is the manifest's own directory.
	dir string
}

// PRTemplate is the pull request each target gets. Body is a text/template
// executed with BodyData.
type PRTemplate struct {
	Title string `yaml:"title"`
	Body  string `yaml:"body"`
}

// BodyData is what a PR body template sees.
type BodyData struct {
	// Contract is the contract's name.
	Contract string
	// Repo is the target's owner/name.
	Repo string
}

// File is one file to copy. Target defaults to Path.
type File struct {
	Path   string `yaml:"path"`
	Target string `yaml:"target"`
}

// TargetPath is where the file lands in a target repository.
func (f File) TargetPath() string {
	if f.Target != "" {
		return f.Target
	}
	return f.Path
}

// Label is one label to provision.
type Label struct {
	Name        string `yaml:"name"`
	Color       string `yaml:"color"`
	Description string `yaml:"description"`
}

// CIJob is one job to insert into a GitHub Actions workflow.
type CIJob struct {
	// Workflow is the workflow file, relative to the repository root.
	Workflow string `yaml:"workflow"`
	// WorkflowName names a workflow file this job creates. Default: ID.
	WorkflowName string `yaml:"workflow_name"`
	// On is the `on:` block of a workflow file this job creates, as YAML.
	// Default: pull_request, and push to main.
	On string `yaml:"on"`
	// ID is the job's key under `jobs:`.
	ID string `yaml:"id"`
	// Job is the job's body as YAML: runs-on, steps and so on.
	Job string `yaml:"job"`
}

// Target is one repository the contract rolls out to. A manifest names the
// repository only: where its checkout is comes from the workspace manifest,
// and its gate is the one the repository itself declares (ResolveGate), so a
// contract can choose neither a directory to write in nor a command to run.
type Target struct {
	// Repo is owner/name on the forge.
	Repo string `yaml:"repo"`
	// Base overrides the contract's Base for this target.
	Base string `yaml:"base"`
}

var (
	contractNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	// Every value that reaches a git argv or a forge call is matched whole,
	// as it is used: no trimming or normalizing happens after validation,
	// and no allowlist admits a leading '-', so no value can read as an
	// option. Nothing here is ever interpolated into a shell string.
	repoRe       = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*/[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
	labelNameRe  = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9 _.:/()+-]{0,49}$`)
	pathSegRe    = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_.@+-]*$`)
	jobIDRe      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	labelColorRe = regexp.MustCompile(`^[0-9a-fA-F]{6}$`)
	branchRe     = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/-]*$`)
)

// Load reads and validates a contract manifest.
func Load(manifestPath string) (*Contract, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read contract %s: %w", manifestPath, err)
	}
	abs, err := filepath.Abs(manifestPath)
	if err != nil {
		return nil, err
	}
	return Parse(filepath.Dir(abs), data)
}

// Parse validates a contract manifest whose own directory is dir. Unknown
// keys are refused, so a misspelled key cannot silently do nothing.
func Parse(dir string, data []byte) (*Contract, error) {
	var c Contract
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse contract: %w", err)
	}
	c.dir = dir
	if c.Branch == "" {
		c.Branch = "contract/" + c.Name
	}
	if c.Base == "" {
		c.Base = "main"
	}
	if c.CommitMessage == "" {
		c.CommitMessage = fmt.Sprintf("chore: adopt the %s contract", c.Name)
	}
	if c.PR.Title == "" {
		c.PR.Title = c.CommitMessage
	}
	if c.CIJob != nil {
		if c.CIJob.WorkflowName == "" {
			c.CIJob.WorkflowName = c.CIJob.ID
		}
		if c.CIJob.On == "" {
			c.CIJob.On = "pull_request:\npush:\n  branches: [main]\n"
		}
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// SetTargets replaces the contract's targets and validates them.
func (c *Contract) SetTargets(ts []Target) error {
	c.Targets = ts
	return c.validate()
}

// SourceDir is the absolute directory Files are read from.
func (c *Contract) SourceDir() string {
	return filepath.Join(c.dir, c.SourceRoot)
}

// Dir is the manifest's own directory.
func (c *Contract) Dir() string { return c.dir }

// BaseFor is the base branch of t's pull request.
func (c *Contract) BaseFor(t Target) string {
	if t.Base != "" {
		return t.Base
	}
	return c.Base
}

// RenderBody executes the PR body template for repo.
func (c *Contract) RenderBody(repo string) (string, error) {
	tmpl, err := template.New("body").Option("missingkey=error").Parse(c.PR.Body)
	if err != nil {
		return "", fmt.Errorf("pr.body: %w", err)
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, BodyData{Contract: c.Name, Repo: repo}); err != nil {
		return "", fmt.Errorf("pr.body: %w", err)
	}
	return b.String(), nil
}

func (c *Contract) validate() error {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }
	if !contractNameRe.MatchString(c.Name) {
		add("name %q must be lower-case letters, digits, '.', '_' or '-'", c.Name)
	}
	for _, b := range []string{c.Branch, c.Base} {
		if !validBranch(b) {
			add("branch %q is not a plain branch name", b)
		}
	}
	if len(c.Files) == 0 && len(c.Labels) == 0 && c.CIJob == nil {
		add("the contract changes nothing: give it files, labels or a ci_job")
	}
	seen := map[string]bool{}
	for _, f := range c.Files {
		for _, p := range []string{f.Path, f.TargetPath()} {
			if err := checkRelPath(p); err != nil {
				add("files: %v", err)
			}
		}
		// Compared case-folded: on a case-insensitive filesystem
		// `Scripts/CI-Local.sh` is scripts/ci-local.sh.
		switch foldPath(f.TargetPath()) {
		case foldPath(DefaultGateScript), foldPath(GateConfigPath):
			add("files: %s is part of the repository's own gate; a contract may not replace it", f.TargetPath())
		case foldPath(ChangelogPath):
			add("files: %s is the repository's own changelog; give the contract a changelog entry instead", f.TargetPath())
		}
		if seen[foldPath(f.TargetPath())] {
			add("files: %s is listed twice", f.TargetPath())
		}
		seen[foldPath(f.TargetPath())] = true
	}
	for _, l := range c.Labels {
		if !labelNameRe.MatchString(l.Name) {
			add("labels: name %q must start with a letter, digit or '_' and hold only letters, digits, spaces and _.:/()+-", l.Name)
		}
		if strings.ContainsAny(l.Description, "\x00\r\n") {
			add("labels: %q description holds a control character", l.Name)
		}
		if !labelColorRe.MatchString(l.Color) {
			add("labels: %q has color %q, want six hex digits", l.Name, l.Color)
		}
	}
	if j := c.CIJob; j != nil {
		if err := checkRelPath(j.Workflow); err != nil {
			add("ci_job: %v", err)
		} else if !strings.HasPrefix(j.Workflow, ".github/workflows/") ||
			!(strings.HasSuffix(j.Workflow, ".yml") || strings.HasSuffix(j.Workflow, ".yaml")) {
			add("ci_job: workflow %q is not a .github/workflows/*.yml file", j.Workflow)
		}
		if !jobIDRe.MatchString(j.ID) {
			add("ci_job: id %q is not a valid job id", j.ID)
		}
		if _, err := parseJob(j.Job); err != nil {
			add("ci_job: %v", err)
		}
		var on any
		if err := yaml.Unmarshal([]byte(j.On), &on); err != nil {
			add("ci_job: on: %v", err)
		}
	}
	if cl := c.Changelog; cl != nil {
		if !validChangelogSection(cl.Section) {
			add("changelog: section %q is not one of %s", cl.Section, strings.Join(changelogSections, ", "))
		}
		if _, err := c.RenderChangelogEntry("owner/name"); err != nil {
			add("%v", err)
		}
	} else if len(c.Files) > 0 || c.CIJob != nil {
		add("changelog: the contract changes files or CI, so every target's changelog needs an entry; give it changelog.section and changelog.entry")
	}
	repos := map[string]bool{}
	for _, t := range c.Targets {
		if !repoRe.MatchString(t.Repo) {
			add("targets: repo %q is not owner/name", t.Repo)
		}
		if repos[strings.ToLower(t.Repo)] {
			add("targets: %s is listed twice", t.Repo)
		}
		repos[strings.ToLower(t.Repo)] = true
		if t.Base != "" && !validBranch(t.Base) {
			add("targets: %s base %q is not a plain branch name", t.Repo, t.Base)
		}
	}
	if _, err := template.New("body").Parse(c.PR.Body); err != nil {
		add("pr.body: %v", err)
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid contract:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// checkRelPath refuses a path that is empty, absolute, or leaves the root it
// is joined to, and one inside .git.
func checkRelPath(p string) error {
	if p == "" {
		return fmt.Errorf("an empty path")
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.Contains(p, `\`) {
		return fmt.Errorf("%q is absolute", p)
	}
	clean := path.Clean(p)
	if clean != p || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("%q is not a clean path inside the repository", p)
	}
	// Case-folded: a case-insensitive filesystem resolves .GIT to .git.
	if folded := strings.ToLower(clean); folded == ".git" || strings.HasPrefix(folded, ".git/") {
		return fmt.Errorf("%q is inside .git", p)
	}
	for _, seg := range strings.Split(clean, "/") {
		if !pathSegRe.MatchString(seg) {
			return fmt.Errorf("%q has a segment %q outside [A-Za-z0-9_.@+-] or starting with '-'", p, seg)
		}
	}
	return nil
}

// validBranch is a plain branch name: the allowlist, and none of the forms
// git refuses or reads specially.
func validBranch(b string) bool {
	return branchRe.MatchString(b) && !strings.Contains(b, "..") && !strings.Contains(b, "//") &&
		!strings.HasSuffix(b, "/") && !strings.HasSuffix(b, ".") && !strings.HasSuffix(b, ".lock") &&
		!strings.Contains(b, "/.") && !strings.Contains(b, "/-")
}
