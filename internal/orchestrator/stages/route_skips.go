package stages

import "context"

// Route-skipped stages (#1968 ask b).
//
// Routing may skip feature-validate (docs-only, config, trivial complexity).
// The pr-create deterministic arm used to require validate-{N}.json, so every
// such skip punted `missing-validate-context` and paid the saving back as an
// LLM pr-create. The scheduler now tells the runner which stages the route
// skipped on THIS run, and DecideCreate treats a route-skipped validate stage
// as satisfied — recorded as ReasonValidateSkippedByRoute and stated in the PR
// body. A validate context that is merely missing (not skipped by the route)
// still punts, so nothing reaches a PR without the validation the route meant
// to run.
//
// Carried on the context for the same reason as WithPhaseReporter: the
// scheduler holds one runner for every concurrent run, and the PRCreateRunner
// interface stays unchanged so existing fakes keep working.

type routeSkipsKey struct{}

// WithRouteSkippedStages attaches the stage names the run's route skipped.
func WithRouteSkippedStages(ctx context.Context, skipped []string) context.Context {
	if len(skipped) == 0 {
		return ctx
	}
	cp := append([]string(nil), skipped...)
	return context.WithValue(ctx, routeSkipsKey{}, cp)
}

// RouteSkippedStages returns the stage names attached by
// WithRouteSkippedStages, or nil.
func RouteSkippedStages(ctx context.Context) []string {
	skipped, _ := ctx.Value(routeSkipsKey{}).([]string)
	return append([]string(nil), skipped...)
}

// routeSkippedStage reports whether the route skipped the named stage.
func routeSkippedStage(ctx context.Context, stage string) bool {
	for _, s := range RouteSkippedStages(ctx) {
		if s == stage {
			return true
		}
	}
	return false
}
