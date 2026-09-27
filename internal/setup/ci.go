package setup

import (
	"context"
	"fmt"
	"path"
	"strings"
)

// PinnedAction is a GitHub Action referenced by the CI template, pinned to a
// full commit SHA. Orgs that set sha_pinning_required reject tag refs with a
// startup_failure, so the template never emits `@vN`.
type PinnedAction struct {
	Repo string // e.g. actions/checkout
	SHA  string // 40-char commit SHA the tag points at
	Tag  string // human-readable release tag, written as a trailing comment
}

// Pinned actions used by templates/ci.yml.tmpl. To bump: resolve the tag with
// `gh api repos/<repo>/git/ref/tags/<tag>` (dereferencing annotated tags via
// git/tags/<sha>) and update SHA and Tag together.
var (
	actionCheckout  = PinnedAction{Repo: "actions/checkout", SHA: "3d3c42e5aac5ba805825da76410c181273ba90b1", Tag: "v7.0.1"}
	actionSetupNode = PinnedAction{Repo: "actions/setup-node", SHA: "820762786026740c76f36085b0efc47a31fe5020", Tag: "v7.0.0"}
	ciActions       = []PinnedAction{actionCheckout, actionSetupNode}
)

// gateScripts are the package.json scripts that become CI steps, in step
// order. Each is run as `npm run <script>` with no appended arguments.
var gateScripts = []string{"typecheck", "lint", "test", "build"}

var gateStepNames = map[string]string{
	"typecheck": "Typecheck",
	"lint":      "Lint",
	"test":      "Test",
	"build":     "Build",
}

type ciStep struct {
	Name   string
	Script string
}

type ciTemplateData struct {
	NodeVersion  string
	RunsOn       string
	CheckoutSHA  string
	CheckoutTag  string
	SetupNodeSHA string
	SetupNodeTag string
	Steps        []ciStep
}

func newCITemplateData(det DetectedDeps, runsOn string) ciTemplateData {
	d := ciTemplateData{
		NodeVersion:  det.NodeVersion,
		RunsOn:       runsOn,
		CheckoutSHA:  actionCheckout.SHA,
		CheckoutTag:  actionCheckout.Tag,
		SetupNodeSHA: actionSetupNode.SHA,
		SetupNodeTag: actionSetupNode.Tag,
	}
	for _, s := range det.Scripts {
		d.Steps = append(d.Steps, ciStep{Name: gateStepNames[s], Script: s})
	}
	return d
}

// ActionsPolicy is the subset of GET /repos/{r}/actions/permissions (and,
// when allowed_actions is "selected", .../permissions/selected-actions) that
// decides whether the emitted workflow may run.
type ActionsPolicy struct {
	Enabled            bool
	AllowedActions     string // all | local_only | selected
	SHAPinningRequired bool
	// Selected-actions detail; only meaningful when AllowedActions=selected.
	// SelectedKnown is false when the detail could not be read.
	SelectedKnown      bool
	GithubOwnedAllowed bool
	PatternsAllowed    []string
}

func probePolicy(ctx context.Context, probe func(context.Context, string) (*ActionsPolicy, error), workdir string) []string {
	p, err := probe(ctx, workdir)
	if err != nil {
		return []string{fmt.Sprintf("ci: could not read the repository's Actions policy (%v) — not checked", err)}
	}
	if p == nil {
		return nil
	}
	return PolicyWarnings(p)
}

// PolicyWarnings reports why the repository's Actions policy would reject the
// emitted ci.yml. Emitted refs are always SHA-pinned, so sha_pinning_required
// is satisfied; the remaining risks are disabled Actions and allow-lists.
func PolicyWarnings(p *ActionsPolicy) []string {
	var w []string
	if !p.Enabled {
		w = append(w, "ci: GitHub Actions is disabled for this repository — ci.yml will not run")
		return w
	}
	switch p.AllowedActions {
	case "local_only":
		w = append(w, "ci: Actions policy allowed_actions=local_only rejects "+actionList()+" — the workflow will fail with startup_failure")
	case "selected":
		if !p.SelectedKnown {
			w = append(w, "ci: Actions policy allowed_actions=selected and the allow-list could not be read — confirm "+actionList()+" are allowed")
			break
		}
		for _, a := range ciActions {
			if !actionAllowed(p, a) {
				w = append(w, fmt.Sprintf("ci: Actions policy allowed_actions=selected does not allow %s — the workflow will fail with startup_failure", a.Repo))
			}
		}
	}
	return w
}

func actionList() string {
	names := make([]string, 0, len(ciActions))
	for _, a := range ciActions {
		names = append(names, a.Repo)
	}
	return strings.Join(names, ", ")
}

func actionAllowed(p *ActionsPolicy, a PinnedAction) bool {
	if p.GithubOwnedAllowed && (strings.HasPrefix(a.Repo, "actions/") || strings.HasPrefix(a.Repo, "github/")) {
		return true
	}
	ref := a.Repo + "@" + a.SHA
	for _, pat := range p.PatternsAllowed {
		pat = strings.TrimSpace(pat)
		if ok, _ := path.Match(pat, ref); ok {
			return true
		}
		if ok, _ := path.Match(pat, a.Repo); ok {
			return true
		}
		// "owner/*" style patterns match any action under the owner.
		if strings.HasSuffix(pat, "*") && strings.HasPrefix(ref, strings.TrimSuffix(pat, "*")) {
			return true
		}
	}
	return false
}
