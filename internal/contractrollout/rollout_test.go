package contractrollout

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// fakeForge is an in-memory forge: labels per repository and the pull
// requests a rollout opened. It never reaches a network.
type fakeForge struct {
	labels map[string]*fakeLabels
	prs    map[string]*PR // repo + "@" + head
	bodies map[string]string
	bases  map[string]string
	next   int
}

func newFakeForge() *fakeForge {
	return &fakeForge{labels: map[string]*fakeLabels{}, prs: map[string]*PR{}, bodies: map[string]string{}, bases: map[string]string{}, next: 100}
}

func (f *fakeForge) Labels(repo string) LabelClient {
	if f.labels[repo] == nil {
		f.labels[repo] = &fakeLabels{}
	}
	return f.labels[repo]
}

func (f *fakeForge) FindPR(_ context.Context, repo, head string) (*PR, error) {
	return f.prs[repo+"@"+head], nil
}

func (f *fakeForge) CreatePR(_ context.Context, repo, head, base, title, body string) (*PR, error) {
	f.next++
	pr := &PR{Number: f.next, URL: fmt.Sprintf("https://forge.invalid/%s/pull/%d", repo, f.next), State: "OPEN"}
	f.prs[repo+"@"+head] = pr
	f.bodies[repo] = title + "\n" + body
	f.bases[repo] = base
	return pr, nil
}

// targetRepo is a bare origin and a checkout of it, with one commit on main.
type targetRepo struct{ origin, checkout string }

func newTargetRepo(t *testing.T, files map[string]string) targetRepo {
	t.Helper()
	origin := filepath.Join(t.TempDir(), "origin.git")
	gittest.Run(t, t.TempDir(), "init", "--quiet", "--bare", "-b", "main", origin)
	checkout := filepath.Join(t.TempDir(), "checkout")
	gittest.Run(t, t.TempDir(), "clone", "--quiet", origin, checkout)
	gittest.Run(t, checkout, "switch", "--quiet", "-c", "main")
	for p, content := range files {
		mode := os.FileMode(0o644)
		if strings.HasSuffix(p, ".sh") {
			mode = 0o755
		}
		writeFile(t, filepath.Join(checkout, p), content, mode)
	}
	gittest.Run(t, checkout, "add", "-A")
	gittest.Run(t, checkout, "commit", "--quiet", "--allow-empty", "-m", "base")
	gittest.Run(t, checkout, "push", "--quiet", "-u", "origin", "main")
	return targetRepo{origin: origin, checkout: checkout}
}

// passingGate is a repository's own local gate that checks the contract's
// file arrived.
const passingGate = "#!/bin/sh\nset -e\ntest -x scripts/check.sh\n"

func demoContract(t *testing.T, targets []Target) *Contract {
	t.Helper()
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "scripts", "check.sh"), "#!/bin/sh\necho checked\n", 0o755)
	manifest := `name: demo
branch: chore/demo-contract
commit_message: "ci: adopt the demo contract"
pr:
  body: "Adopts {{.Contract}} in {{.Repo}}."
files:
  - path: scripts/check.sh
labels:
  - name: contract:demo
    color: "0e8a16"
    description: Demo contract
ci_job:
  workflow: .github/workflows/check.yml
  id: check
  job: |
    runs-on: ubuntu-latest
    steps:
      - run: bash scripts/check.sh
changelog:
  section: Changed
  entry: |
    {{.Repo}} runs the demo check on every pull request
    ({{.Contract}} contract).
`
	writeFile(t, filepath.Join(src, "demo.yaml"), manifest, 0o644)
	c, err := Load(filepath.Join(src, "demo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetTargets(targets); err != nil {
		t.Fatal(err)
	}
	return c
}

func pathsFor(repos map[string]targetRepo) func(Target) (string, error) {
	return func(t Target) (string, error) {
		r, ok := repos[t.Repo]
		if !ok {
			return "", fmt.Errorf("no checkout for %s", t.Repo)
		}
		return r.checkout, nil
	}
}

// TestRolloutOpensOnePRPerRepoAfterItsGate is the rollout end to end, from
// one invocation, against three target repositories on local bare origins:
// one that needs the contract and whose gate passes, one already compliant,
// and one whose gate fails. Only the first gets a PR, and the table has a
// row for each.
func TestRolloutOpensOnePRPerRepoAfterItsGate(t *testing.T) {
	ctx := context.Background()
	repos := map[string]targetRepo{
		"o/needs": newTargetRepo(t, map[string]string{
			"scripts/ci-local.sh":        passingGate,
			".github/workflows/lint.yml": "name: Lint\non: [pull_request]\njobs:\n  lint:\n    runs-on: x\n    steps:\n      - run: make lint\n",
		}),
		"o/compliant": newTargetRepo(t, map[string]string{
			"scripts/ci-local.sh": passingGate,
			"scripts/check.sh":    "#!/bin/sh\necho checked\n",
			".github/workflows/check.yml": "name: check\non: [pull_request]\njobs:\n  check:\n    runs-on: ubuntu-latest\n" +
				"    steps:\n      - run: bash scripts/check.sh\n",
		}),
		"o/failing": newTargetRepo(t, map[string]string{"scripts/ci-local.sh": "#!/bin/sh\necho gate says no >&2\nexit 3\n"}),
	}
	c := demoContract(t, []Target{
		{Repo: "o/needs"},
		{Repo: "o/compliant"},
		{Repo: "o/failing"},
	})
	forge := newFakeForge()
	work := t.TempDir()

	rows := Rollout(ctx, c, Options{Apply: true, WorkDir: work, ResolvePath: pathsFor(repos), Forge: forge})
	if len(rows) != 3 {
		t.Fatalf("rows = %+v", rows)
	}
	needs, compliant, failing := rows[0], rows[1], rows[2]

	if needs.Status != StatusPROpen || needs.Gate != "passed" || needs.PR == nil {
		t.Fatalf("o/needs = %+v", needs)
	}
	if !strings.Contains(forge.bodies["o/needs"], "Adopts demo in o/needs.") || forge.bases["o/needs"] != "main" {
		t.Errorf("PR body/base = %q / %q", forge.bodies["o/needs"], forge.bases["o/needs"])
	}
	// The pushed branch holds the byte-identical file and the inserted job.
	pushed := gittest.Run(t, repos["o/needs"].origin, "show", "chore/demo-contract:scripts/check.sh")
	if pushed != "#!/bin/sh\necho checked" {
		t.Errorf("pushed check.sh = %q", pushed)
	}
	if wf := gittest.Run(t, repos["o/needs"].origin, "show", "chore/demo-contract:.github/workflows/check.yml"); !strings.Contains(wf, "bash scripts/check.sh") {
		t.Errorf("pushed workflow = %q", wf)
	}
	if msg := gittest.Run(t, repos["o/needs"].origin, "log", "-1", "--format=%s", "chore/demo-contract"); msg != "ci: adopt the demo contract" {
		t.Errorf("commit message = %q", msg)
	}
	if len(forge.labels["o/needs"].created) != 1 {
		t.Errorf("labels created = %+v", forge.labels["o/needs"].created)
	}
	// The operator's checkout is untouched and the worktree is gone.
	if st := gittest.Run(t, repos["o/needs"].checkout, "status", "--porcelain"); st != "" {
		t.Errorf("the checkout was changed:\n%s", st)
	}
	if _, err := os.Stat(filepath.Join(work, "o__needs")); !os.IsNotExist(err) {
		t.Error("the worktree of a rolled-out target was left behind")
	}

	if compliant.Status != StatusCompliant || compliant.PR != nil {
		t.Errorf("o/compliant = %+v", compliant)
	}
	if _, ok := forge.prs["o/compliant@chore/demo-contract"]; ok {
		t.Error("a compliant repository got a PR")
	}

	if failing.Status != StatusGateFailed || failing.Gate != "failed" || failing.PR != nil || failing.Worktree == "" {
		t.Errorf("o/failing = %+v", failing)
	}
	if !strings.Contains(failing.Detail, "gate says no") {
		t.Errorf("the gate's output is not in the row: %q", failing.Detail)
	}
	if out, err := gittest.Command(repos["o/failing"].origin, "rev-parse", "--verify", "chore/demo-contract").CombinedOutput(); err == nil {
		t.Errorf("a branch whose gate failed was pushed: %s", out)
	}

	var table bytes.Buffer
	WriteTable(&table, rows)
	for _, want := range []string{
		"| Repository | Status | Local gate | PR | CI checks | Detail |",
		"| o/needs | pr-open | passed | [#101](https://forge.invalid/o/needs/pull/101) | - |",
		"| o/compliant | compliant | skipped | - | - |",
		"| o/failing | gate-failed | failed | - | - |",
	} {
		if !strings.Contains(table.String(), want) {
			t.Errorf("table lacks %q:\n%s", want, table.String())
		}
	}

	// A second invocation finds the open PR instead of opening another, and
	// reports it rolled out once its checks are green.
	forge.prs["o/needs@chore/demo-contract"].Checks = "SUCCESS"
	again := Rollout(ctx, c, Options{Apply: true, WorkDir: t.TempDir(), ResolvePath: pathsFor(repos), Forge: forge})
	if again[0].Status != StatusRolledOut || again[0].Gate != "earlier-run" || again[0].PR.Number != 101 || forge.next != 101 {
		t.Errorf("second rollout of o/needs = %+v (PRs opened: %d)", again[0], forge.next-100)
	}

	refreshed := Refresh(ctx, c, forge)
	if refreshed[0].Status != StatusRolledOut || refreshed[1].Status != StatusPlanned {
		t.Errorf("refresh = %+v", refreshed)
	}
	forge.prs["o/needs@chore/demo-contract"].Checks = "FAILURE"
	if r := Refresh(ctx, c, forge); r[0].Status != StatusCIFailed {
		t.Errorf("refresh with red CI = %+v", r[0])
	}
}

// TestRolloutWithoutAGateOpensNoPR: a repository that declares no gate and
// has no scripts/ci-local.sh gets no PR.
func TestRolloutWithoutAGateOpensNoPR(t *testing.T) {
	repos := map[string]targetRepo{"o/nogate": newTargetRepo(t, map[string]string{"README.md": "x\n"})}
	c := demoContract(t, []Target{{Repo: "o/nogate"}})
	forge := newFakeForge()
	rows := Rollout(context.Background(), c, Options{Apply: true, WorkDir: t.TempDir(), ResolvePath: pathsFor(repos), Forge: forge})
	if rows[0].Status != StatusGateMissing || len(forge.prs) != 0 {
		t.Fatalf("row = %+v, PRs = %v", rows[0], forge.prs)
	}
}

// TestRolloutPlanChangesNothing: without Apply the rollout reads the
// checkout and the forge and writes neither.
func TestRolloutPlanChangesNothing(t *testing.T) {
	repos := map[string]targetRepo{"o/needs": newTargetRepo(t, map[string]string{
		"scripts/ci-local.sh": passingGate,
		"CHANGELOG.md":        "# Changelog\n\n## [Unreleased]\n",
	})}
	c := demoContract(t, []Target{{Repo: "o/needs"}})
	forge := newFakeForge()
	rows := Rollout(context.Background(), c, Options{ResolvePath: pathsFor(repos), Forge: forge})
	if rows[0].Status != StatusPlanned || len(rows[0].Labels.Created) != 1 || rows[0].CIJob.Action != "created" || !rows[0].Files[0].Changed {
		t.Fatalf("plan = %+v", rows[0])
	}
	if rows[0].Changelog == nil || rows[0].Changelog.Action != "added" || rows[0].GateCommand != "bash scripts/ci-local.sh" {
		t.Errorf("plan changelog/gate = %+v / %q", rows[0].Changelog, rows[0].GateCommand)
	}
	if len(forge.prs) != 0 || len(forge.labels["o/needs"].created) != 0 {
		t.Error("planning changed the forge")
	}
	if st := gittest.Run(t, repos["o/needs"].checkout, "status", "--porcelain"); st != "" {
		t.Errorf("planning changed the checkout:\n%s", st)
	}
}

// TestChangelogContractRollsOutEndToEnd is the incident #1480 was filed for:
// the shipped changelog contract (configs/contracts/changelog.yaml), rolled
// out through this capability to two repositories that lack it. Each gets
// the core's checker and self-test byte for byte, the changelog CI job, its
// own gate run, and one PR; the table reports both.
func TestChangelogContractRollsOutEndToEnd(t *testing.T) {
	c, err := Load(filepath.Join("..", "..", "configs", "contracts", "changelog.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// o/app's gate runs the checker it is given against its own changelog,
	// so the entry the rollout writes must satisfy the real contract. o/web
	// has no ci-local.sh: it declares its gate in its own configuration.
	gate := "#!/bin/sh\nset -e\ntest -x scripts/check-changelog.sh\ntest -x scripts/test-check-changelog.sh\n" +
		"bash scripts/check-changelog.sh --extension none\n"
	changelog := "# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- An earlier fix (#1).\n\n## [0.1.0] - 2026-01-01\n\n### Added\n\n- The first release.\n"
	repos := map[string]targetRepo{
		"o/app": newTargetRepo(t, map[string]string{"scripts/ci-local.sh": gate, "CHANGELOG.md": changelog}),
		"o/web": newTargetRepo(t, map[string]string{
			"scripts/verify.sh":        "#!/bin/sh\nset -e\ntest -x scripts/check-changelog.sh\n",
			".nightgauge/config.yaml":  "owner: o\nlocal_gate:\n  steps:\n    - [sh, scripts/verify.sh]\n",
			".github/workflows/ci.yml": "name: CI\non: [pull_request]\njobs:\n  test:\n    runs-on: x\n    steps:\n      - run: make test\n",
		}),
	}
	if err := c.SetTargets([]Target{{Repo: "o/app"}, {Repo: "o/web"}}); err != nil {
		t.Fatal(err)
	}
	forge := newFakeForge()
	rows := Rollout(context.Background(), c, Options{Apply: true, WorkDir: t.TempDir(), ResolvePath: pathsFor(repos), Forge: forge})

	for i, repo := range []string{"o/app", "o/web"} {
		if rows[i].Status != StatusPROpen || rows[i].Gate != "passed" {
			t.Fatalf("%s = %+v", repo, rows[i])
		}
		for _, f := range []string{"scripts/check-changelog.sh", "scripts/test-check-changelog.sh"} {
			want := readFile(t, filepath.Join("..", "..", f))
			got, err := gittest.Command(repos[repo].origin, "show", "chore/changelog-contract:"+f).Output()
			if err != nil || string(got) != want {
				t.Errorf("%s: %s is not byte-identical to the core's (%v)", repo, f, err)
			}
		}
		wf := gittest.Run(t, repos[repo].origin, "show", "chore/changelog-contract:.github/workflows/changelog.yml")
		if !strings.Contains(wf, "bash scripts/check-changelog.sh --extension none") {
			t.Errorf("%s: changelog job missing:\n%s", repo, wf)
		}
	}
	// The entry lands in o/app's [Unreleased] section, in the same commit,
	// as a new Added subsection ahead of Fixed; o/web has no changelog.
	pushed := gittest.Run(t, repos["o/app"].origin, "show", "chore/changelog-contract:CHANGELOG.md")
	want := "## [Unreleased]\n\n### Added\n\n- The workspace changelog contract: every pull request checks that this\n" +
		"  changelog keeps one `## [Unreleased]` section and names every released\n" +
		"  tag, with the core's `scripts/check-changelog.sh` and its self-test.\n\n### Fixed\n\n- An earlier fix (#1).\n\n## [0.1.0]"
	if !strings.Contains(pushed, want) {
		t.Errorf("o/app CHANGELOG.md:\n%s", pushed)
	}
	if n := gittest.Run(t, repos["o/app"].origin, "rev-list", "--count", "main..chore/changelog-contract"); n != "1" {
		t.Errorf("o/app: %s commits on the branch, want 1", n)
	}
	if rows[0].Changelog == nil || rows[0].Changelog.Action != "added" || rows[0].GateCommand != "bash scripts/ci-local.sh" {
		t.Errorf("o/app row = %+v", rows[0])
	}
	if rows[1].Changelog == nil || rows[1].Changelog.Action != "absent" || rows[1].GateCommand != "sh scripts/verify.sh" {
		t.Errorf("o/web row = %+v", rows[1])
	}

	var table bytes.Buffer
	WriteTable(&table, rows)
	if strings.Count(table.String(), "| pr-open | passed |") != 2 {
		t.Errorf("table:\n%s", table.String())
	}
}

// TestRolloutRunsTheDeclaredGate: a repository's own local_gate replaces
// scripts/ci-local.sh. Its steps run in order, without a shell, and the
// first failure stops the gate and opens no PR (#2434).
func TestRolloutRunsTheDeclaredGate(t *testing.T) {
	log := filepath.Join(t.TempDir(), "gate.log")
	logScript := "#!/bin/sh\necho \"$2\" >> \"$1\"\n"
	declare := func(steps ...string) string {
		return "owner: o\nlocal_gate:\n  steps:\n    - " + strings.Join(steps, "\n    - ") + "\n"
	}
	repos := map[string]targetRepo{
		// Its ci-local.sh would fail: the declaration wins.
		"o/declared": newTargetRepo(t, map[string]string{
			"scripts/ci-local.sh":     "#!/bin/sh\nexit 1\n",
			"scripts/log.sh":          logScript,
			".nightgauge/config.yaml": declare("[sh, scripts/log.sh, "+log+", one]", "[sh, scripts/log.sh, "+log+", two]"),
		}),
		"o/stops": newTargetRepo(t, map[string]string{
			"scripts/log.sh":          logScript,
			"scripts/no.sh":           "#!/bin/sh\necho declared gate says no >&2\nexit 4\n",
			".nightgauge/config.yaml": declare("[bash, scripts/no.sh]", "[sh, scripts/log.sh, "+log+", never]"),
		}),
		"o/shell": newTargetRepo(t, map[string]string{
			".nightgauge/config.yaml": declare("[sh, -c, 'touch pwned']"),
		}),
		"o/guarded": newTargetRepo(t, map[string]string{
			"scripts/check.sh":        "#!/bin/sh\n",
			".nightgauge/config.yaml": declare("[sh, scripts/check.sh]"),
		}),
	}
	c := demoContract(t, []Target{{Repo: "o/declared"}, {Repo: "o/stops"}, {Repo: "o/shell"}, {Repo: "o/guarded"}})
	forge := newFakeForge()
	work := t.TempDir()
	rows := Rollout(context.Background(), c, Options{Apply: true, WorkDir: work, ResolvePath: pathsFor(repos), Forge: forge})

	declared, stops, shell, guarded := rows[0], rows[1], rows[2], rows[3]
	if declared.Status != StatusPROpen || declared.Gate != "passed" || !strings.HasPrefix(declared.GateCommand, "sh scripts/log.sh ") {
		t.Errorf("o/declared = %+v", declared)
	}
	if got := readFile(t, log); got != "one\ntwo\n" {
		t.Errorf("gate log = %q: the steps did not run once each, in order, or a later step ran after a failure", got)
	}
	if stops.Status != StatusGateFailed || stops.PR != nil || !strings.Contains(stops.Detail, "bash scripts/no.sh") ||
		!strings.Contains(stops.Detail, "declared gate says no") {
		t.Errorf("o/stops = %+v", stops)
	}
	if shell.Status != StatusError || !strings.Contains(shell.Detail, "local_gate") || shell.PR != nil {
		t.Errorf("o/shell = %+v", shell)
	}
	if guarded.Status != StatusError || !strings.Contains(guarded.Detail, "scripts/check.sh is part of the repository's own gate") {
		t.Errorf("o/guarded = %+v", guarded)
	}
	for _, repo := range []string{"o__shell", "o__guarded"} {
		if _, err := os.Stat(filepath.Join(work, repo)); !os.IsNotExist(err) {
			t.Errorf("%s: the worktree of a refused target was left behind", repo)
		}
	}
	if len(forge.prs) != 1 {
		t.Errorf("PRs = %v, want only o/declared's", forge.prs)
	}
}
