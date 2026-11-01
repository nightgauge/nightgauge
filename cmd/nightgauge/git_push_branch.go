package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// Exit codes of `git push-branch`, a contract feature-validate Step 5.3 and
// feature-dev Step 0.7.1b branch on (#2699).
const (
	pushBranchOK     = 0
	pushBranchFailed = 1
	pushBranchForked = 3
)

// pushBranchResult is the --json payload of `git push-branch`.
type pushBranchResult struct {
	Branch    string   `json:"branch"`
	Outcome   string   `json:"outcome"` // pushed | recovered | forked | unreconciled | failed
	OldSHA    string   `json:"old_sha,omitempty"`
	NewSHA    string   `json:"new_sha,omitempty"`
	Unmatched []string `json:"unmatched,omitempty"`
	Detail    string   `json:"detail,omitempty"`
}

// pushBranchAttempt is the push runPushBranch makes. A test replaces it to move
// origin between the reconcile and the retry.
var pushBranchAttempt = pushPlain

// gitPushBranchCmd pushes the current branch to origin without ever forcing.
// A non-fast-forward rejection is reconciled with ReconcileSelfFork: a
// self-fork (a rebased PR branch whose origin copy carries no change the local
// tree lacks) is recovered with a commit descending from origin's tip and
// pushed plainly; a genuine fork, or a rejection that could not be reconciled,
// prints [branch-forked] and exits 3. Exit 1 is reserved for a failure that was
// not a rejection.
func gitPushBranchCmd() *cobra.Command {
	var outputJSON bool
	cmd := &cobra.Command{
		Use:          "push-branch",
		Short:        "Push the current branch to origin, recovering a self-inflicted fork without force (#2699)",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			if code := runPushBranch(cmd.Context(), dir, outputJSON, cmd.OutOrStdout(), cmd.ErrOrStderr()); code != pushBranchOK {
				return verdictExit{code: code}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}

// runPushBranch is the testable body of `git push-branch`.
func runPushBranch(ctx context.Context, dir string, outputJSON bool, stdout, stderr io.Writer) int {
	if ctx == nil {
		ctx = context.Background()
	}
	emit := func(r pushBranchResult, code int) int {
		if outputJSON {
			if data, err := json.MarshalIndent(r, "", "  "); err == nil {
				fmt.Fprintln(stdout, string(data))
			}
		}
		return code
	}

	branchOut, err := gitOutput(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		fmt.Fprintln(stderr, "push-branch: HEAD is not on a branch")
		return emit(pushBranchResult{Outcome: "failed", Detail: "detached HEAD"}, pushBranchFailed)
	}
	branch := strings.TrimSpace(branchOut)
	res := pushBranchResult{Branch: branch}

	pushErr, rejected := pushBranchAttempt(ctx, dir, branch)
	if pushErr == "" {
		res.Outcome = "pushed"
		return emit(res, pushBranchOK)
	}
	if !rejected {
		fmt.Fprint(stderr, pushErr)
		res.Outcome, res.Detail = "failed", strings.TrimSpace(pushErr)
		return emit(res, pushBranchFailed)
	}

	rec := orchestrator.ReconcileSelfFork(ctx, dir, branch)
	res.OldSHA, res.Unmatched, res.Detail = rec.RemoteSHA, rec.Unmatched, rec.Detail
	switch rec.Outcome {
	case orchestrator.SelfForkRecovered:
		if again, againRejected := pushBranchAttempt(ctx, dir, branch); again != "" {
			fmt.Fprint(stderr, again)
			if err := orchestrator.RollbackSelfFork(ctx, dir, rec); err != nil {
				fmt.Fprintf(stderr, "push-branch: could not roll back recovery commit: %v\n", err)
			}
			res.Detail = strings.TrimSpace(again)
			if againRejected {
				fmt.Fprintln(stderr, "[branch-forked] PUSH REJECTED: origin moved during recovery")
				res.Outcome = "forked"
				return emit(res, pushBranchForked)
			}
			res.Outcome = "failed"
			return emit(res, pushBranchFailed)
		}
		res.Outcome, res.NewSHA = "recovered", rec.NewSHA
		// The marker always prints; under --json it goes to stderr so stdout
		// stays pure JSON.
		marker := stdout
		if outputJSON {
			marker = stderr
		}
		fmt.Fprintf(marker, "[self-fork-recovered] origin/%s %s..%s\n",
			branch, shortCommit(rec.RemoteSHA), shortCommit(rec.NewSHA))
		return emit(res, pushBranchOK)
	case orchestrator.SelfForkForked:
		fmt.Fprintf(stderr, "[branch-forked] PUSH REJECTED: %s\n", rec.Detail)
		for _, sha := range rec.Unmatched {
			fmt.Fprintf(stderr, "  origin-only commit: %s\n", sha)
		}
		res.Outcome = "forked"
		return emit(res, pushBranchForked)
	default:
		// The push was rejected and the reconcile produced no recovery. Whether
		// origin carries work is unknown, but the branch is unpushed, so the run
		// must stop here, never defer past it.
		fmt.Fprint(stderr, pushErr)
		fmt.Fprintf(stderr, "[branch-forked] PUSH REJECTED: could not reconcile: %s\n", rec.Detail)
		res.Outcome = "unreconciled"
		return emit(res, pushBranchForked)
	}
}

// pushPlain runs `git push origin HEAD:refs/heads/<branch>`. It returns git's
// stderr on failure ("" on success) and whether the failure was a
// non-fast-forward rejection.
func pushPlain(ctx context.Context, dir, branch string) (string, bool) {
	cmd := exec.CommandContext(ctx, "git", "push", "origin", "HEAD:refs/heads/"+branch)
	cmd.Dir = dir
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		msg := errBuf.String()
		if msg == "" {
			msg = err.Error() + "\n"
		}
		return msg, strings.Contains(msg, "non-fast-forward") || strings.Contains(msg, "fetch first")
	}
	return "", false
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

func shortCommit(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
