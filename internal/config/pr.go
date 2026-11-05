package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// PRConfig is the subset of the pr: block the Go scheduler reads (#2504).
//
//	pr:
//	  auto_fix_max_attempts: 2
//	  ci_check_timeout: 600 # seconds
type PRConfig struct {
	// AutoFixMaxAttempts bounds how many times pr-merge hands a CI failure to
	// the LLM fixer before the stage fails. 0/absent → DefaultAutoFixMaxAttempts.
	AutoFixMaxAttempts int `yaml:"auto_fix_max_attempts,omitempty" json:"autoFixMaxAttempts,omitempty"`
	// CICheckTimeout is the CI-wait budget per attempt, in seconds.
	// 0/absent → the deterministic runner's built-in budget.
	CICheckTimeout int `yaml:"ci_check_timeout,omitempty" json:"ciCheckTimeout,omitempty"`
}

// DefaultAutoFixMaxAttempts is pr.auto_fix_max_attempts when unset.
const DefaultAutoFixMaxAttempts = 2

// ResolveAutoFixMaxAttempts returns NIGHTGAUGE_PR_AUTO_FIX_MAX_ATTEMPTS, then
// pr.auto_fix_max_attempts, then DefaultAutoFixMaxAttempts. Non-positive
// values are ignored. Safe on a nil receiver.
func (p *PRConfig) ResolveAutoFixMaxAttempts() int {
	if n := positiveEnvInt("NIGHTGAUGE_PR_AUTO_FIX_MAX_ATTEMPTS"); n > 0 {
		return n
	}
	if p != nil && p.AutoFixMaxAttempts > 0 {
		return p.AutoFixMaxAttempts
	}
	return DefaultAutoFixMaxAttempts
}

// ResolveCICheckTimeout returns NIGHTGAUGE_PR_CI_CHECK_TIMEOUT, then
// pr.ci_check_timeout, as a duration (both in seconds). It returns 0 when
// neither is set, meaning "keep the runner's built-in budget". Safe on a nil
// receiver.
func (p *PRConfig) ResolveCICheckTimeout() time.Duration {
	if n := positiveEnvInt("NIGHTGAUGE_PR_CI_CHECK_TIMEOUT"); n > 0 {
		return time.Duration(n) * time.Second
	}
	if p != nil && p.CICheckTimeout > 0 {
		return time.Duration(p.CICheckTimeout) * time.Second
	}
	return 0
}

// DefaultPRMergeCIWait is the floor of an ordinary pr-merge CI wait.
const DefaultPRMergeCIWait = 20 * time.Minute

// ResolvePRMergeCIWait is the budget for one pr-merge CI wait: the largest of
// the merge-queue wait plus ten minutes, DefaultPRMergeCIWait and
// pr.ci_check_timeout. The pr-merge verb and the branch-out-of-date recovery
// both use it, so a head updated during recovery waits as long as a fresh push
// does (#2980). Safe on a nil receiver.
func (c *Config) ResolvePRMergeCIWait() time.Duration {
	var pipeline *PipelineConfig
	var pr *PRConfig
	if c != nil {
		pipeline, pr = c.Pipeline, c.PR
	}
	wait := pipeline.ResolveMergeQueueWaitTimeout() + 10*time.Minute
	if wait < DefaultPRMergeCIWait {
		wait = DefaultPRMergeCIWait
	}
	if ci := pr.ResolveCICheckTimeout(); ci > wait {
		wait = ci
	}
	return wait
}

func positiveEnvInt(key string) int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}
