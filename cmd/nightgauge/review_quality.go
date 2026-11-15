package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/qualityreview"
	"github.com/spf13/cobra"
)

// qualityReviewTimeout keeps the whole fan-out under the 10-minute ceiling of
// the Phase 5 shell call that runs it, so the call is never cut off mid-write.
const qualityReviewTimeout = 9 * time.Minute

// qualityReviewSkill is the stage whose orchestration block is read.
const qualityReviewSkill = "feature-dev"

// qualityRun is one Phase 5 review: everything runQualityReview needs.
type qualityRun struct {
	Root     string
	Issue    int
	Base     string
	Runner   adapters.SkillRunner // nil when the adapter did not resolve
	Adapter  string
	Resolve  error // why Runner is nil
	Skill    []byte
	SkillErr error
	Spawn    reviewSpawn
	Stderr   io.Writer
}

// runQualityReview fans the declared units out concurrently through the
// stage's adapter and merges their findings. An adapter without a tool loop,
// or a skill without a usable orchestration block, spawns nothing and ends
// skipped with the reason on stderr.
func runQualityReview(ctx context.Context, q qualityRun) qualityreview.Report {
	skip := func(reason string) qualityreview.Report {
		fmt.Fprintln(q.Stderr, "[quality-review] skipped:", reason)
		return qualityreview.Skipped(q.Adapter, reason)
	}
	if q.Resolve != nil {
		return skip(q.Resolve.Error())
	}
	if !q.Runner.Agentic() {
		return skip("adapter " + q.Runner.Name() + " has no tool loop")
	}
	if q.SkillErr != nil {
		return skip("the skill could not be read: " + q.SkillErr.Error())
	}
	spec, err := qualityreview.ParseSpec(q.Skill)
	if err != nil {
		return skip(err.Error())
	}
	b, err := qualityreview.BuildBundle(ctx, q.Root, q.Issue, q.Base)
	if err != nil {
		return skip("the bundle could not be built: " + err.Error())
	}
	logs, err := layout.CloneLogsDir(q.Root)
	if err == nil {
		err = os.MkdirAll(logs, 0o700)
	}
	if err != nil {
		return skip("the log directory is unavailable: " + err.Error())
	}
	stamp := time.Now().Unix()
	units := make([]qualityreview.UnitResult, len(spec.Units))
	var wg sync.WaitGroup
	for i, u := range spec.Units {
		wg.Add(1)
		go func(i int, u qualityreview.Unit) {
			defer wg.Done()
			logPath := filepath.Join(logs, fmt.Sprintf("quality-review-%d-%s-%d.log", q.Issue, u.ID, stamp))
			err := q.Spawn(ctx, q.Runner, adapters.RunOptions{
				WorktreeDir:  q.Root,
				IssueNumber:  q.Issue,
				Stage:        qualityreview.Stage,
				AllowedTools: reviewTools,
				Prompt:       qualityreview.Prompt(q.Root, b, u),
				MaxTurns:     qualityreview.MaxTurns,
			}, logPath)
			units[i] = qualityreview.ReadUnit(b, u.ID)
			if err != nil && units[i].Status == qualityreview.StatusUnfinished {
				units[i].Reason = "spawn failed, see " + logPath + ": " + err.Error()
			}
		}(i, u)
	}
	wg.Wait()
	return qualityreview.Merge(spec, q.Runner.Name(), units)
}

// qualitySkillPath is the dispatched stage's own SKILL.md when the stage
// exported its directory, else the repository's source copy.
func qualitySkillPath(root string) string {
	if dir := os.Getenv("NIGHTGAUGE_SKILL_DIR"); dir != "" {
		if p := filepath.Join(dir, "SKILL.md"); fileExists(p) {
			return p
		}
	}
	return filepath.Join(root, "skills", "nightgauge-"+qualityReviewSkill, "SKILL.md")
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func reviewQualityCmd() *cobra.Command {
	var (
		issue                int
		workdir, base, skill string
	)
	cmd := &cobra.Command{
		Use:   "quality",
		Short: "Fan feature-dev's Phase 5 reviewer units out through the stage's adapter (#2850)",
		Long: `Reads the units and judge from feature-dev's orchestration frontmatter, spawns
one read-only reviewer per unit concurrently, merges their findings and writes
quality-review-{N}.json. An adapter without a tool loop spawns nothing and the
result is skipped. See docs/WORKFLOW_ORCHESTRATION.md.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), qualityReviewTimeout)
			defer cancel()
			root, err := gateWorkdir(workdir)
			if err != nil {
				return err
			}
			cfg, err := config.Load(root)
			if err != nil {
				return err
			}
			q := qualityRun{Root: root, Issue: issue, Base: base, Spawn: spawnReview, Stderr: os.Stderr}
			q.Adapter = config.ResolveStageAdapter(cfg, qualityReviewSkill, os.Getenv).Adapter
			q.Runner, q.Resolve = adapters.NewRegistry().Resolve("", q.Adapter)
			if skill == "" {
				skill = qualitySkillPath(root)
			}
			q.Skill, q.SkillErr = os.ReadFile(skill)
			report := runQualityReview(ctx, q)
			report.CreatedAt = time.Now().UTC().Format(time.RFC3339)
			data, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				return err
			}
			name := fmt.Sprintf("quality-review-%d.json", issue)
			if _, err := layout.WriteClassFile(root, layout.ClassPipeline, name, bytes.NewReader(data)); err != nil {
				return err
			}
			return printJSON(report)
		},
	}
	cmd.Flags().IntVar(&issue, "issue", 0, "Issue number (required)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Working directory (default: cwd)")
	cmd.Flags().StringVar(&base, "base", "main", "Base branch the diff is taken against (origin/<base>)")
	cmd.Flags().StringVar(&skill, "skill", "", "SKILL.md to read units from (default: the dispatched feature-dev skill)")
	_ = cmd.MarkFlagRequired("issue")
	return cmd
}
