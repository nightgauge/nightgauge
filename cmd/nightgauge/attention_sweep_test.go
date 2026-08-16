package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/attention/sweep"
	"github.com/nightgauge/nightgauge/internal/forge"
)

// nilForge satisfies forge.ForgeClient without any transport. The CLI tests
// drive fake producers, so no service is ever reached — and nothing here can
// make a live API call.
type nilForge struct{}

func (nilForge) Issues() forge.IssueService      { return nil }
func (nilForge) PRs() forge.PRService            { return nil }
func (nilForge) Project() forge.ProjectService   { return nil }
func (nilForge) Board() forge.BoardService       { return nil }
func (nilForge) CI() forge.CIService             { return nil }
func (nilForge) Labels() forge.LabelService      { return nil }
func (nilForge) Rulesets() forge.RulesetService  { return nil }
func (nilForge) Security() forge.SecurityService { return nil }
func (nilForge) Auth() forge.AuthService         { return nil }
func (nilForge) Repo() forge.RepoService         { return nil }

type cliProducer struct {
	name string
	reqs []attention.DecisionRequest
}

func (p *cliProducer) Name() string { return p.name }
func (p *cliProducer) Evaluate(context.Context, sweep.Input) ([]attention.DecisionRequest, error) {
	return append([]attention.DecisionRequest(nil), p.reqs...), nil
}

// withRegisteredProducer installs a producer in the process-wide registry the
// CLI sweeps, and stubs the forge resolver so no adapter is constructed.
func withRegisteredProducer(t *testing.T, reqs ...attention.DecisionRequest) {
	t.Helper()
	prevRegistry := sweep.Default
	prevClient := sweepForgeClient
	t.Cleanup(func() {
		sweep.Default = prevRegistry
		sweepForgeClient = prevClient
	})
	sweep.Default = sweep.NewRegistry()
	sweep.Default.Register(&cliProducer{name: "test-producer", reqs: reqs})
	sweepForgeClient = func(string) (forge.ForgeClient, error) { return nilForge{}, nil }
}

func cliObservation(key, fingerprint, title string) attention.DecisionRequest {
	return attention.DecisionRequest{
		IdempotencyKey: key,
		Kind:           attention.KindUnblock,
		Severity:       attention.SeverityBlockingFleet,
		Title:          title,
		Fingerprint:    fingerprint,
		Options:        []attention.Option{{ID: "wait", Label: "Wait", Verb: attention.VerbNoop}},
		DefaultAction:  attention.ExpireNoop,
	}
}

func runSweep(t *testing.T, dir string, extra ...string) (string, error) {
	t.Helper()
	cmd := attentionSweepCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(append([]string{"--repo", "octocat/acme", "--workdir", dir}, extra...))
	err := cmd.Execute()
	return buf.String(), err
}

func TestAttentionSweepRaisesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	withRegisteredProducer(t, cliObservation("k:branch", "check:build=failure", "default branch is red"))

	out, err := runSweep(t, dir)
	if err != nil {
		t.Fatalf("first sweep: %v\n%s", err, out)
	}
	reqs, err := attention.New(dir).List(attention.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(reqs) != 1 || reqs[0].Title != "default branch is red" {
		t.Fatalf("want the blocker raised once, got %d requests", len(reqs))
	}

	// Second sweep, same condition: no new card.
	if _, err := runSweep(t, dir); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	reqs, _ = attention.New(dir).List(attention.ListFilter{})
	if len(reqs) != 1 {
		t.Errorf("a repeated sweep must not duplicate the card, got %d", len(reqs))
	}
}

func TestAttentionSweepJSONReportsTheReconciliation(t *testing.T) {
	dir := t.TempDir()
	withRegisteredProducer(t, cliObservation("k:branch", "f1", "default branch is red"))

	out, err := runSweep(t, dir, "--json")
	if err != nil {
		t.Fatalf("sweep: %v\n%s", err, out)
	}
	var res sweep.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("parse --json output %q: %v", out, err)
	}
	if res.Repo != "octocat/acme" || res.Reconciled.Created != 1 || !res.OK() {
		t.Errorf("unexpected sweep result: %+v", res)
	}
}

func TestAttentionSweepRequiresARepo(t *testing.T) {
	cmd := attentionSweepCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--workdir", t.TempDir()})
	if err := cmd.Execute(); err == nil {
		t.Error("expected --repo to be required")
	}
}

func TestAttentionSweepWithNoProducersReportsCleanly(t *testing.T) {
	dir := t.TempDir()
	prevRegistry, prevClient := sweep.Default, sweepForgeClient
	t.Cleanup(func() { sweep.Default, sweepForgeClient = prevRegistry, prevClient })
	sweep.Default = sweep.NewRegistry()
	sweepForgeClient = func(string) (forge.ForgeClient, error) { return nilForge{}, nil }

	out, err := runSweep(t, dir)
	if err != nil {
		t.Fatalf("a sweep with nothing registered must not fail: %v", err)
	}
	if !strings.Contains(out, "No repo-scoped producers registered") {
		t.Errorf("expected an explicit no-producers message, got:\n%s", out)
	}
}

func TestAttentionMuteAndUnmuteCLI(t *testing.T) {
	dir := t.TempDir()
	withRegisteredProducer(t, cliObservation("k:branch", "f1", "default branch is red"))
	if _, err := runSweep(t, dir); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	reqs, _ := attention.New(dir).List(attention.ListFilter{})
	id := reqs[0].ID

	mute := attentionMuteCmd()
	var buf bytes.Buffer
	mute.SetOut(&buf)
	mute.SetArgs([]string{id, "--workdir", dir, "--actor", "octocat"})
	if err := mute.Execute(); err != nil {
		t.Fatalf("mute: %v", err)
	}
	got, _, err := attention.New(dir).Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.IsMuted() {
		t.Fatal("mute must record a mute")
	}
	if got.Lifecycle.State.IsTerminal() {
		t.Error("muting must not resolve the card")
	}

	unmute := attentionUnmuteCmd()
	unmute.SetOut(&bytes.Buffer{})
	unmute.SetArgs([]string{id, "--workdir", dir})
	if err := unmute.Execute(); err != nil {
		t.Fatalf("unmute: %v", err)
	}
	got, _, _ = attention.New(dir).Get(id)
	if got.IsMuted() {
		t.Error("unmute must restore alerting")
	}
}

func TestAttentionAckCLIKeepsTheCard(t *testing.T) {
	dir := t.TempDir()
	id := seedRequest(t, dir, "k-ack", "Fleet stopped", attention.SeverityBlockingFleet)

	cmd := attentionAckCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{id, "--workdir", dir, "--actor", "octocat"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("ack: %v", err)
	}
	got, _, err := attention.New(dir).Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Lifecycle.State != attention.StateAcknowledged || got.Lifecycle.Acknowledged == nil {
		t.Errorf("ack must acknowledge without resolving, got %q", got.Lifecycle.State)
	}
}

// TestAttentionSweepRootCommand_BareConfigRepoStillRequiresFlag is a
// regression test for #222: sweep run through the assembled root command
// with a bare (no-slash) config.yaml defaultRepo and no --repo flag must
// still fail with the required-flag message, not a confusing splitRepo
// error deeper in the call chain. Built in isolation (attentionSweepCmd())
// never exercises PersistentPreRunE and would miss a regression here.
func TestAttentionSweepRootCommand_BareConfigRepoStillRequiresFlag(t *testing.T) {
	dir := chdirTemp(t)
	writeBareRepoConfig(t, dir)
	withRegisteredProducer(t)

	cmd := rootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"attention", "sweep", "--workdir", dir})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected --repo to still be required with only a bare config.yaml defaultRepo set")
	}
	if !strings.Contains(err.Error(), "--repo is required") {
		t.Errorf("expected the required-flag message, got: %v", err)
	}
}

func TestAttentionListMarksMutedCards(t *testing.T) {
	dir := t.TempDir()
	id := seedRequest(t, dir, "k-muted", "Fleet stopped", attention.SeverityBlockingFleet)
	if _, err := attention.New(dir).Mute(id, "octocat"); err != nil {
		t.Fatalf("Mute: %v", err)
	}

	cmd := attentionListCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--workdir", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("list: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Fleet stopped") {
		t.Errorf("a muted card must still be listed:\n%s", out)
	}
	if !strings.Contains(out, "muted") {
		t.Errorf("a muted card must be marked as silenced:\n%s", out)
	}
}
