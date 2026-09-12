package codexprovision

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

const userAgents = "# Contract\n\nUser rules.\n"

func block(inner string) string {
	return steeringManagedBegin + "\n" + inner + "\n" + steeringManagedEnd + "\n"
}

func guardRepo(t *testing.T) string {
	t.Helper()
	dir := gittest.InitRepo(t, t.TempDir(), "-b", "main")
	gittest.Run(t, dir, "config", "user.email", "test@test")
	gittest.Run(t, dir, "config", "user.name", "test")
	writeFile(t, filepath.Join(dir, "README"), "hi\n")
	gittest.Run(t, dir, "add", ".")
	gittest.Run(t, dir, "commit", "-qm", "initial")
	return dir
}

func gitShow(t *testing.T, dir, ref string) (string, bool) {
	t.Helper()
	out, err := gittest.Command(dir, "show", ref).Output()
	return string(out), err == nil
}

func TestSanitizeStagedAgentsMd_StripsBlockFromIndexOnly(t *testing.T) {
	dir := guardRepo(t)
	writeFile(t, filepath.Join(dir, "AGENTS.md"), userAgents)
	gittest.Run(t, dir, "add", ".")
	gittest.Run(t, dir, "commit", "-qm", "contract")

	edited := userAgents + "Another rule.\n"
	live := upsertManagedSteeringBlock(edited, true, "steering")
	writeFile(t, filepath.Join(dir, "AGENTS.md"), live)
	gittest.Run(t, dir, "add", "-A")

	changed, err := SanitizeStagedAgentsMd(context.Background(), dir)
	if err != nil || !changed {
		t.Fatalf("SanitizeStagedAgentsMd = %v, %v; want true, nil", changed, err)
	}
	staged, _ := gitShow(t, dir, ":AGENTS.md")
	if ContainsManagedSteering(staged) || staged != edited {
		t.Fatalf("staged AGENTS.md = %q, want the user's edit without the block", staged)
	}
	disk, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if string(disk) != live {
		t.Errorf("the working tree belongs to the live stage and must keep its steering")
	}
}

func TestSanitizeStagedAgentsMd_UnstagesAGeneratedOnlyFile(t *testing.T) {
	dir := guardRepo(t)
	writeFile(t, filepath.Join(dir, "AGENTS.md"), block("steering"))
	gittest.Run(t, dir, "add", "-A")
	if _, err := SanitizeStagedAgentsMd(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := gitShow(t, dir, ":AGENTS.md"); ok {
		t.Fatal("an AGENTS.md holding only generated steering must not be staged")
	}
}

func TestSanitizeStagedAgentsMd_NoOpOutsideARepoOrWithoutBlock(t *testing.T) {
	if changed, err := SanitizeStagedAgentsMd(context.Background(), t.TempDir()); changed || err != nil {
		t.Fatalf("not a repository: got %v, %v", changed, err)
	}
	dir := guardRepo(t)
	writeFile(t, filepath.Join(dir, "AGENTS.md"), userAgents)
	gittest.Run(t, dir, "add", "-A")
	if changed, err := SanitizeStagedAgentsMd(context.Background(), dir); changed || err != nil {
		t.Fatalf("clean AGENTS.md: got %v, %v", changed, err)
	}
}

func TestRepairCommittedSteering_RemovesExactlyTheBlock(t *testing.T) {
	dir := guardRepo(t)
	writeFile(t, filepath.Join(dir, "AGENTS.md"), userAgents)
	gittest.Run(t, dir, "add", ".")
	gittest.Run(t, dir, "commit", "-qm", "contract")
	// The leak: a stage's agent commits while the block is present.
	writeFile(t, filepath.Join(dir, "AGENTS.md"), upsertManagedSteeringBlock(userAgents, true, "steering"))
	writeFile(t, filepath.Join(dir, "work.txt"), "work\n")
	gittest.Run(t, dir, "add", "-A")
	gittest.Run(t, dir, "commit", "-qm", "feat: work")
	// Post-stage the working tree is stripped first.
	if err := stripWorkingTreeSteering(dir); err != nil {
		t.Fatal(err)
	}

	res, err := RepairCommittedSteering(context.Background(), dir, false)
	if err != nil || !res.Repaired {
		t.Fatalf("RepairCommittedSteering = %+v, %v", res, err)
	}
	if head, _ := gitShow(t, dir, "HEAD:AGENTS.md"); head != userAgents {
		t.Fatalf("HEAD:AGENTS.md = %q, want the user content", head)
	}
	if subj := gittest.Run(t, dir, "log", "-1", "--format=%s"); subj != RepairCommitMessage {
		t.Errorf("repair subject = %q", subj)
	}
	if files := gittest.Run(t, dir, "show", "--name-only", "--format=", "HEAD"); files != "AGENTS.md" {
		t.Errorf("the repair must touch only AGENTS.md, touched %q", files)
	}
	if st := gittest.Run(t, dir, "status", "--porcelain"); st != "" {
		t.Errorf("index and working tree must match the repaired HEAD, status:\n%s", st)
	}
	again, err := RepairCommittedSteering(context.Background(), dir, false)
	if err != nil || again.Repaired {
		t.Errorf("a second repair must be a no-op, got %+v, %v", again, err)
	}
}

func TestRepairCommittedSteering_DeletesAGeneratedOnlyFile(t *testing.T) {
	dir := guardRepo(t)
	writeFile(t, filepath.Join(dir, "AGENTS.md"), block("steering"))
	gittest.Run(t, dir, "add", "-A")
	gittest.Run(t, dir, "commit", "-qm", "feat: leaked")
	if err := stripWorkingTreeSteering(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("a generated-only AGENTS.md must be deleted from the working tree: %v", err)
	}
	if res, err := RepairCommittedSteering(context.Background(), dir, false); err != nil || !res.Repaired {
		t.Fatalf("repair = %+v, %v", res, err)
	}
	if _, ok := gitShow(t, dir, "HEAD:AGENTS.md"); ok {
		t.Fatal("the repaired tip must not carry a generated-only AGENTS.md")
	}
	if st := gittest.Run(t, dir, "status", "--porcelain"); st != "" {
		t.Errorf("status after repair:\n%s", st)
	}
}

func TestRepairCommittedSteering_PushesWhenTheLeakWasPublished(t *testing.T) {
	remote := gittest.InitRepo(t, t.TempDir(), "--bare", "-b", "main")
	dir := guardRepo(t)
	gittest.Run(t, dir, "remote", "add", "origin", remote)
	gittest.Run(t, dir, "checkout", "-qb", "feat/x")
	writeFile(t, filepath.Join(dir, "AGENTS.md"), upsertManagedSteeringBlock(userAgents, true, "steering"))
	gittest.Run(t, dir, "add", "-A")
	gittest.Run(t, dir, "commit", "-qm", "feat: leaked")
	gittest.Run(t, dir, "push", "-qu", "origin", "feat/x")

	res, err := RepairCommittedSteering(context.Background(), dir, true)
	if err != nil || !res.Repaired || !res.Pushed || res.PushErr != nil {
		t.Fatalf("repair = %+v, %v; want repaired and pushed", res, err)
	}
	if tip := gittest.Run(t, remote, "rev-parse", "feat/x"); tip != res.NewHead {
		t.Fatalf("remote tip %s, want the repair %s", tip, res.NewHead)
	}
}

func TestStripManagedSteering_DropsOrphanedMarkers(t *testing.T) {
	in := userAgents + steeringManagedBegin + "\nhalf a block\n"
	got := StripManagedSteering(in)
	if ContainsManagedSteering(got) {
		t.Fatalf("orphaned marker survived: %q", got)
	}
	if !strings.Contains(got, "User rules.") {
		t.Fatalf("user content lost: %q", got)
	}
}

func TestFindCommittedSteering_ReportsAndFixes(t *testing.T) {
	dir := guardRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "AGENTS.md"), userAgents+"\n"+block("steering"))
	writeFile(t, filepath.Join(dir, "pkg", "AGENTS.md"), "# Pkg\n\nclean\n")
	gittest.Run(t, dir, "add", "-A")
	gittest.Run(t, dir, "commit", "-qm", "leaked")

	res, err := FindCommittedSteering(context.Background(), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesChecked != 2 || len(res.Findings) != 1 || res.Findings[0] != "AGENTS.md" || len(res.Fixed) != 0 {
		t.Fatalf("unexpected report %+v", res)
	}
	res, err = FindCommittedSteering(context.Background(), dir, true)
	if err != nil || len(res.Fixed) != 1 {
		t.Fatalf("fix: %+v, %v", res, err)
	}
	disk, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if ContainsManagedSteering(string(disk)) || !strings.Contains(string(disk), "User rules.") {
		t.Fatalf("working tree after --fix: %q", disk)
	}
	if _, ok := gitShow(t, dir, "HEAD:AGENTS.md"); !ok {
		t.Fatal("--fix must not commit")
	}
	if _, err := FindCommittedSteering(context.Background(), t.TempDir(), false); err == nil {
		t.Error("a directory outside git must be a hard error")
	}
}
