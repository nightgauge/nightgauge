package doctor

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// `nightgauge doctor --only <code|check>` (#2094): the "check again"
// affordance a manual remedy names. It re-runs only the checks that own the
// given codes or IDs, plus the checks they depend on (a dependency that does
// not pass still skips its dependent, and the operator sees why).

// extraCheckCodes names the codes a check emits beyond its primary Check.Code.
// TestEveryCodeResolvesToACheck keeps it complete: every NGD literal in this
// package must resolve to a check through Code or this table.
var extraCheckCodes = map[string][]string{
	"scopes":                {"NGD034"},
	"github_identity":       {"NGD035", "NGD036", "NGD037", "NGD038", "NGD039"},
	"board_population":      {"NGD040", "NGD041", "NGD042"},
	"orphaned_processes":    {"NGD032"},
	"complexity_model":      {"NGD033"},
	"scheduled_automations": {"NGD030", "NGD031"},
	"adapters": {"NGD100", "NGD101", "NGD102", "NGD103", "NGD104", "NGD105",
		"NGD106", "NGD107", "NGD108", "NGD109", "NGD110", "NGD111"},
}

// checkOwns reports whether c is named by want: its ID, its primary code, or
// one of its extra codes. Codes compare case-insensitively.
func checkOwns(c Check, want string) bool {
	if want == c.ID || strings.EqualFold(want, c.Code) {
		return true
	}
	for _, code := range extraCheckCodes[c.ID] {
		if strings.EqualFold(want, code) {
			return true
		}
	}
	return false
}

// Select returns a registry holding the checks only names (by ID or code) and
// every check they depend on, in r's order. An entry that names no check is
// an error, so a typo never reads as a clean pass.
func (r *Registry) Select(only []string) (*Registry, error) {
	keep := map[string]bool{}
	var unknown []string
	var include func(id string)
	include = func(id string) {
		if keep[id] {
			return
		}
		keep[id] = true
		for _, dep := range r.checks[r.index[id]].DependsOn {
			include(dep)
		}
	}
	for _, want := range only {
		want = strings.TrimSpace(want)
		if want == "" {
			continue
		}
		hit := false
		for _, c := range r.checks {
			if checkOwns(c, want) {
				include(c.ID)
				hit = true
			}
		}
		if !hit {
			unknown = append(unknown, want)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("--only: no check owns %s (use a finding code such as NGD036 or a check ID such as github_identity)",
			strings.Join(unknown, ", "))
	}
	out := NewRegistry()
	for _, c := range r.checks {
		if keep[c.ID] {
			out.MustRegister(c)
		}
	}
	return out, nil
}

// RunDoctorOnly is RunDoctorWithConfigError over only the checks only names
// and their dependencies. An empty only runs every check.
func RunDoctorOnly(ctx context.Context, cfg *config.Config, cfgErr error, client *gh.Client, adapters, only []string) (DoctorResult, error) {
	reg := DefaultRegistry()
	if len(only) > 0 {
		var err error
		if reg, err = reg.Select(only); err != nil {
			return DoctorResult{}, err
		}
	}
	cwd, _ := os.Getwd()
	env := &Env{Cfg: cfg, CfgErr: cfgErr, Client: client, Cwd: cwd, Now: time.Now(), Adapters: adapters}
	return runRegistry(ctx, reg, env), nil
}
