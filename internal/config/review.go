package config

import (
	"fmt"
	"strings"
)

// ReviewConfig is the review: block, the independent review of
// feature-validate (#2839, #3683).
//
//	review:
//	  max_rounds: 3
//	  model: opus
type ReviewConfig struct {
	// MaxRounds caps the catches one pipeline run may spend on an issue.
	// Pointer so an explicit value is distinguishable from unset.
	MaxRounds *int `yaml:"max_rounds,omitempty" json:"max_rounds,omitempty"`
	// Model is the reviewer's model. Empty follows feature-validate's model.
	Model string `yaml:"model,omitempty" json:"model,omitempty"`
}

const (
	// DefaultReviewMaxRounds is the default for review.max_rounds.
	DefaultReviewMaxRounds = 3
	// MinReviewMaxRounds and MaxReviewMaxRounds bound review.max_rounds.
	MinReviewMaxRounds = 1
	MaxReviewMaxRounds = 6
)

// ValidateReview rejects a review.max_rounds outside its bounds. A value out of
// range is an error, never clamped: a cap nobody chose is not a cap.
func ValidateReview(r *ReviewConfig) error {
	if r == nil || r.MaxRounds == nil {
		return nil
	}
	if n := *r.MaxRounds; n < MinReviewMaxRounds || n > MaxReviewMaxRounds {
		return fmt.Errorf("review.max_rounds must be between %d and %d, got %d",
			MinReviewMaxRounds, MaxReviewMaxRounds, n)
	}
	return nil
}

// ResolveReviewMaxRounds is the effective review.max_rounds. It is nil-safe and
// returns the default for an unset or out-of-range value (Load rejects the
// latter, so only a config built in code reaches it).
func (c *Config) ResolveReviewMaxRounds() int {
	if c == nil || c.Review == nil || ValidateReview(c.Review) != nil || c.Review.MaxRounds == nil {
		return DefaultReviewMaxRounds
	}
	return *c.Review.MaxRounds
}

// ResolveReviewModel is the reviewer's model, in order:
//
//  1. review.model;
//  2. NIGHTGAUGE_DISPATCH_MODEL while NIGHTGAUGE_STAGE is feature-validate, the
//     model the stage the review runs inside was dispatched with;
//  3. pipeline.stage_models.feature-validate;
//  4. ui.core.default_model (ResolveDefaultModel);
//  5. "", the adapter's default.
//
// getenv is injectable for tests.
func ResolveReviewModel(cfg *Config, getenv func(string) string) string {
	if cfg != nil && cfg.Review != nil {
		if v := strings.TrimSpace(cfg.Review.Model); v != "" {
			return v
		}
	}
	if strings.TrimSpace(getenv("NIGHTGAUGE_STAGE")) == "feature-validate" {
		if v := strings.TrimSpace(getenv("NIGHTGAUGE_DISPATCH_MODEL")); v != "" {
			return v
		}
	}
	if cfg != nil && cfg.Pipeline != nil {
		if v := strings.TrimSpace(cfg.Pipeline.StageModels["feature-validate"]); v != "" {
			return v
		}
	}
	return ResolveDefaultModel(cfg, getenv)
}
