package stages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nightgauge/nightgauge/internal/ci"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/layout"
)

// Merge-group removal classification (#3198). A merge-queue removal is either
// an infrastructure failure (a job failed while preparing its environment,
// before the repository's own checks ran) or a code failure (a job failed in
// one of the repository's own checks, or in a step the classifier cannot name
// as setup). It fails safe toward code: infrastructure needs a positively
// identified setup step with the repository's checks after it left unrun.
// Only unreadable job data stays unclassified and keeps the plain
// merge-queue-failed refusal.

// MergeGroupFailureClass is the verdict on one merge-queue removal.
type MergeGroupFailureClass string

const (
	// MergeGroupUnclassified: the job and step results do not decide it, so
	// the removal keeps today's merge-queue-failed refusal.
	MergeGroupUnclassified MergeGroupFailureClass = "unclassified"
	// MergeGroupInfra: every failing job failed in a setup step and left
	// the repository's checks after it unrun.
	MergeGroupInfra MergeGroupFailureClass = "infrastructure"
	// MergeGroupCode: at least one failing job failed in a check step, or
	// in a step that is not recognised setup. One such job decides the
	// removal, whatever the others did (#3193).
	MergeGroupCode MergeGroupFailureClass = "code"
)

const (
	// MergeQueueCodeFailureMarker is the token a merge-queue-failed reason
	// carries when a merge-group job failed in the repository's own check
	// (#3198). The merge-queue code-failure recovery matches on it.
	MergeQueueCodeFailureMarker = "merge-group code failure"
	// DefaultQueueInfraRetries bounds the re-enqueues after an
	// infrastructure removal. config.ResolvePRMergeTimeout sizes the
	// pr-merge timeout for them, so the constants live there.
	DefaultQueueInfraRetries = config.MergeQueueInfraRetries
	// DefaultQueueInfraRetryDelay is the wait before each re-enqueue.
	DefaultQueueInfraRetryDelay = config.MergeQueueInfraRetryDelay
)

// MergeGroupJobResult is one failed merge-group job with its steps, in
// execution order. Steps is empty when the job could not be read, or when the
// failed check is a commit status with no Actions job.
type MergeGroupJobResult struct {
	Check string
	JobID int64
	RunID int64
	Steps []github.WorkflowRunStep
}

// MergeGroupVerdict is the classification of a removal and the job and step
// that decided it. Job, Step and JobID are empty for an unclassified removal.
// StepNumber is the step's number in the job, which selects its log section.
type MergeGroupVerdict struct {
	Class      MergeGroupFailureClass
	Job        string
	JobID      int64
	RunID      int64
	Step       string
	StepNumber int
}

// ClassifyMergeGroupFailure classifies a removal from its failed jobs' step
// results. kind names what a step does (StepKinds).
//
// Per job, the first step that failed (failure or timed_out) decides:
//   - a check step, or a step that is neither check nor setup (`go vet`, a
//     custom script, a step the workflows do not name): code. When in doubt
//     it is the PR's own failure, never infrastructure (#3198);
//   - a setup step with a check step after it that never ran (skipped, or
//     no conclusion): infrastructure, since the job stopped while preparing
//     its environment, before the repository's remaining checks. Checks that
//     passed before it (the Playwright job builds before installing its OS
//     dependencies) do not change that;
//   - a setup step with no check after it left unrun, or a failed step with
//     no name: unclassified.
//
// A job with no failed step but a cancelled one is neutral: a fail-fast or
// concurrency cancel says nothing about the code, and must not outvote the
// job that actually failed.
//
// Across jobs, any code job makes the removal code; otherwise it is
// infrastructure only when every non-neutral job is; otherwise it is
// unclassified.
func ClassifyMergeGroupFailure(jobs []MergeGroupJobResult, kind func(step string) ci.StepKind) MergeGroupVerdict {
	if len(jobs) == 0 || kind == nil {
		return MergeGroupVerdict{Class: MergeGroupUnclassified}
	}
	var infra *MergeGroupVerdict
	allInfra := true
	for _, job := range jobs {
		v, neutral := classifyMergeGroupJob(job, kind)
		if neutral {
			continue
		}
		switch v.Class {
		case MergeGroupCode:
			return v
		case MergeGroupInfra:
			if infra == nil {
				infra = &v
			}
		default:
			allInfra = false
		}
	}
	if allInfra && infra != nil {
		return *infra
	}
	return MergeGroupVerdict{Class: MergeGroupUnclassified}
}

// classifyMergeGroupJob classifies one job. neutral is true for a job that
// failed only by cancellation.
func classifyMergeGroupJob(job MergeGroupJobResult, kind func(string) ci.StepKind) (MergeGroupVerdict, bool) {
	unclassified := MergeGroupVerdict{Class: MergeGroupUnclassified}
	failed, cancelled := -1, false
	for i, s := range job.Steps {
		switch strings.ToLower(s.Conclusion) {
		case "failure", "timed_out":
			failed = i
		case "cancelled":
			cancelled = true
		}
		if failed >= 0 {
			break
		}
	}
	if failed < 0 {
		return unclassified, cancelled
	}
	if job.Steps[failed].Name == "" {
		return unclassified, false
	}
	v := MergeGroupVerdict{Job: job.Check, JobID: job.JobID, RunID: job.RunID, Step: job.Steps[failed].Name, StepNumber: job.Steps[failed].Number}
	if kind(job.Steps[failed].Name) != ci.StepSetup {
		v.Class = MergeGroupCode
		return v, false
	}
	checkAfter := false
	for _, s := range job.Steps[failed+1:] {
		if kind(s.Name) != ci.StepCheck {
			continue
		}
		checkAfter = true
		if c := strings.ToLower(s.Conclusion); c != "skipped" && c != "" {
			return unclassified, false
		}
	}
	if !checkAfter {
		return unclassified, false
	}
	v.Class = MergeGroupInfra
	return v, false
}

// StepKinds builds the step classifier from the repository's workflow steps
// (ci.ListWorkflowSteps), each classified by ci.ClassifyRunBlock against the
// repository's discovered commands (ci.DiscoverCommands). A step a
// job ran is looked up by its rendered name:
//   - a named step by its name, or by the part before any ${{ }} expression
//     in it, or with a trailing parenthetical dropped (a name GitHub rendered
//     with a suffix the workflow no longer carries);
//   - an unnamed run step as "Run <first line>", an unnamed uses step as
//     "Run <action>";
//   - GitHub's own "Set up job", "Complete job" and "Post ..." steps are
//     setup.
//
// A name two steps share with different kinds is other. A step not found is
// other, which ClassifyMergeGroupFailure treats as code. It returns nil when
// no check step is known, which leaves every removal unclassified.
func StepKinds(steps []ci.WorkflowStep, discovered []string) func(step string) ci.StepKind {
	names := map[string]ci.StepKind{}
	type prefixed struct {
		prefix string
		kind   ci.StepKind
	}
	var prefixes []prefixed
	anyCheck := false
	add := func(name string, k ci.StepKind) {
		if prev, ok := names[name]; ok && prev != k {
			k = ci.StepOther
		}
		names[name] = k
	}
	for _, st := range steps {
		k := ci.ClassifyStep(st.Uses, st.Run, discovered)
		if k == ci.StepCheck {
			anyCheck = true
		}
		switch {
		case strings.Contains(st.Name, "${{"):
			if p := strings.TrimSpace(st.Name[:strings.Index(st.Name, "${{")]); p != "" {
				prefixes = append(prefixes, prefixed{p, k})
			}
		case st.Name != "":
			add(st.Name, k)
		case st.Uses != "":
			add("Run "+st.Uses, k)
		default:
			first, _, _ := strings.Cut(strings.TrimSpace(st.Run), "\n")
			prefixes = append(prefixes, prefixed{"Run " + strings.TrimSpace(first), k})
		}
	}
	if !anyCheck {
		return nil
	}
	lookup := func(step string) (ci.StepKind, bool) {
		if k, ok := names[step]; ok {
			return k, true
		}
		for _, p := range prefixes {
			if strings.HasPrefix(step, p.prefix) {
				return p.kind, true
			}
		}
		return "", false
	}
	return func(step string) ci.StepKind {
		switch {
		case step == "":
			return ci.StepOther
		case step == "Set up job" || step == "Complete job" || strings.HasPrefix(step, "Post "):
			return ci.StepSetup
		}
		if k, ok := lookup(step); ok {
			return k
		}
		if i := strings.LastIndex(step, " ("); i > 0 && strings.HasSuffix(step, ")") {
			if k, ok := lookup(step[:i]); ok {
				return k
			}
		}
		return ci.StepOther
	}
}

// discoverStepKinds is the runner's default step classifier source.
func discoverStepKinds(ctx context.Context, workdir string) (func(string) ci.StepKind, error) {
	if workdir == "" {
		return nil, fmt.Errorf("no workdir to read CI workflows in")
	}
	disc, err := ci.DiscoverCommands(ctx, workdir, "")
	if err != nil {
		return nil, fmt.Errorf("discover CI commands: %w", err)
	}
	return StepKinds(ci.ListWorkflowSteps(workdir), disc.Commands), nil
}

// MergeQueueInfraExhaustedReason is the refusal when an infrastructure
// removal persisted through every re-enqueue. It maps to the non-fault
// merge_queue_infra_failed terminal kind.
func MergeQueueInfraExhaustedReason(v MergeGroupVerdict, reenqueues int) string {
	return fmt.Sprintf("%s: infrastructure failure: job %q step %q failed before the build or test ran; re-enqueued %d time(s); the PR needs no change",
		ReasonMergeQueueInfraExhausted, v.Job, v.Step, reenqueues)
}

// MergeQueueCodeFailureReason is the refusal when a merge-group job failed in
// one of the repository's own checks.
func MergeQueueCodeFailureReason(v MergeGroupVerdict) string {
	return fmt.Sprintf("%s: %s: job %q step %q failed in the repository's own check",
		ReasonMergeQueueFailed, MergeQueueCodeFailureMarker, v.Job, v.Step)
}

// MergeQueueFailureFile is the pipeline-state file pr-merge writes for a
// code-failure removal, read by the recovery that sends the run back to
// feature-dev with the job's log excerpt.
func MergeQueueFailureFile(issueNumber int) string {
	return fmt.Sprintf("merge-queue-failure-%d.json", issueNumber)
}

// MergeQueueFailureRecord is the content of MergeQueueFailureFile.
type MergeQueueFailureRecord struct {
	IssueNumber int    `json:"issue_number"`
	PRNumber    int    `json:"pr_number"`
	Class       string `json:"class"`
	Check       string `json:"check"`
	JobID       int64  `json:"job_id,omitempty"`
	RunID       int64  `json:"run_id,omitempty"`
	Step        string `json:"step"`
	LogExcerpt  string `json:"log_excerpt,omitempty"`
	CreatedAt   string `json:"created_at"`
}

// writeMergeQueueFailureRecord persists rec in workdir's pipeline-state
// directory.
func writeMergeQueueFailureRecord(workdir string, rec MergeQueueFailureRecord) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	_, err = layout.WriteClassFile(workdir, layout.ClassPipeline, MergeQueueFailureFile(rec.IssueNumber), bytes.NewReader(data))
	return err
}
