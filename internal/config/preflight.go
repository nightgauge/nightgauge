package config

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultEnvironmentCheckTimeout bounds a check that declares no timeout.
const DefaultEnvironmentCheckTimeout = 30 * time.Second

// MaxEnvironmentCheckTimeout is the largest timeout a check may declare. A
// preflight that can hold a stage for longer than this is not a preflight.
const MaxEnvironmentCheckTimeout = 10 * time.Minute

// PreflightConfig is the preflight: section of config.yaml (#101).
type PreflightConfig struct {
	// Environment lists project-declared environment checks. Every stage's
	// Phase 0 runs them via `nightgauge preflight environment`; any failure
	// halts the stage with the declared remedy.
	Environment []EnvironmentCheck `json:"environment,omitempty" yaml:"environment,omitempty"`
}

// EnvironmentCheck is one preflight.environment entry.
type EnvironmentCheck struct {
	Name    string            `json:"name" yaml:"name"`
	Run     ArgvList          `json:"run" yaml:"run"`
	Expect  EnvironmentExpect `json:"expect,omitempty" yaml:"expect,omitempty"`
	Remedy  string            `json:"remedy" yaml:"remedy"`
	Timeout YAMLDuration      `json:"timeout,omitempty" yaml:"timeout,omitempty"`
}

// EffectiveTimeout returns the declared timeout, or the default when unset.
func (c EnvironmentCheck) EffectiveTimeout() time.Duration {
	if c.Timeout <= 0 {
		return DefaultEnvironmentCheckTimeout
	}
	return c.Timeout.Duration()
}

// ArgvList is an argument vector executed without a shell. A YAML scalar is
// rejected so that a shell string can never reach exec.
type ArgvList []string

// UnmarshalYAML accepts only a sequence of scalars.
func (a *ArgvList) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.SequenceNode {
		return fmt.Errorf("preflight.environment run must be a list of arguments, " +
			"e.g. run: [curl, -sf, URL] — a shell string is not accepted")
	}
	var out []string
	if err := value.Decode(&out); err != nil {
		return fmt.Errorf("preflight.environment run: %w", err)
	}
	*a = out
	return nil
}

// EnvironmentExpect is the closed set of typed assertions a check may make.
// There is no expression language: unknown keys are a load error.
type EnvironmentExpect struct {
	// ExitCode is the required exit status (default 0).
	ExitCode *int `json:"exit_code,omitempty" yaml:"exit_code,omitempty"`
	// StdoutMatches is a regular expression stdout must match.
	StdoutMatches string `json:"stdout_matches,omitempty" yaml:"stdout_matches,omitempty"`
	// StdoutNumber extracts a number from stdout and bounds it.
	StdoutNumber *StdoutNumberExpect `json:"stdout_number,omitempty" yaml:"stdout_number,omitempty"`
}

// StdoutNumberExpect captures one number from stdout and checks its bounds.
type StdoutNumberExpect struct {
	// Capture is a regular expression with exactly one capture group.
	Capture string   `json:"capture" yaml:"capture"`
	Min     *float64 `json:"min,omitempty" yaml:"min,omitempty"`
	Max     *float64 `json:"max,omitempty" yaml:"max,omitempty"`
}

// WantExitCode returns the required exit status.
func (e EnvironmentExpect) WantExitCode() int {
	if e.ExitCode == nil {
		return 0
	}
	return *e.ExitCode
}

// UnmarshalYAML decodes expect strictly: a mapping with known keys only.
func (e *EnvironmentExpect) UnmarshalYAML(value *yaml.Node) error {
	if err := requireKnownKeys(value, "expect", "exit_code", "stdout_matches", "stdout_number"); err != nil {
		return err
	}
	type plain EnvironmentExpect
	var p plain
	if err := value.Decode(&p); err != nil {
		return err
	}
	*e = EnvironmentExpect(p)
	return nil
}

// UnmarshalYAML decodes stdout_number strictly.
func (s *StdoutNumberExpect) UnmarshalYAML(value *yaml.Node) error {
	if err := requireKnownKeys(value, "expect.stdout_number", "capture", "min", "max"); err != nil {
		return err
	}
	type plain StdoutNumberExpect
	var p plain
	if err := value.Decode(&p); err != nil {
		return err
	}
	*s = StdoutNumberExpect(p)
	return nil
}

func requireKnownKeys(value *yaml.Node, field string, allowed ...string) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("preflight.environment %s must be a mapping of %s — "+
			"expressions are not accepted", field, strings.Join(allowed, ", "))
	}
	for i := 0; i+1 < len(value.Content); i += 2 {
		key := value.Content[i].Value
		known := false
		for _, a := range allowed {
			if key == a {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("preflight.environment %s: unknown key %q (allowed: %s)",
				field, key, strings.Join(allowed, ", "))
		}
	}
	return nil
}

// ValidateEnvironmentChecks enforces the constraints the YAML shape cannot.
func ValidateEnvironmentChecks(checks []EnvironmentCheck) error {
	seen := make(map[string]struct{}, len(checks))
	for i, c := range checks {
		where := fmt.Sprintf("preflight.environment[%d]", i)
		name := strings.TrimSpace(c.Name)
		if name == "" {
			return fmt.Errorf("%s.name is required", where)
		}
		where = fmt.Sprintf("preflight.environment[%d] (%s)", i, name)
		if _, dup := seen[name]; dup {
			return fmt.Errorf("%s: duplicate check name", where)
		}
		seen[name] = struct{}{}
		if len(c.Run) == 0 || strings.TrimSpace(c.Run[0]) == "" {
			return fmt.Errorf("%s.run must be a non-empty list of arguments", where)
		}
		if strings.TrimSpace(c.Remedy) == "" {
			return fmt.Errorf("%s.remedy is required — say how to fix the environment", where)
		}
		if c.Timeout < 0 || c.Timeout.Duration() > MaxEnvironmentCheckTimeout {
			return fmt.Errorf("%s.timeout must be positive and at most %s", where, MaxEnvironmentCheckTimeout)
		}
		if c.Expect.StdoutMatches != "" {
			if _, err := regexp.Compile(c.Expect.StdoutMatches); err != nil {
				return fmt.Errorf("%s.expect.stdout_matches: %w", where, err)
			}
		}
		if n := c.Expect.StdoutNumber; n != nil {
			re, err := regexp.Compile(n.Capture)
			if err != nil {
				return fmt.Errorf("%s.expect.stdout_number.capture: %w", where, err)
			}
			if re.NumSubexp() != 1 {
				return fmt.Errorf("%s.expect.stdout_number.capture must have exactly one capture group, has %d",
					where, re.NumSubexp())
			}
			if n.Min == nil && n.Max == nil {
				return fmt.Errorf("%s.expect.stdout_number needs min, max, or both", where)
			}
			if n.Min != nil && n.Max != nil && *n.Min > *n.Max {
				return fmt.Errorf("%s.expect.stdout_number: min %v exceeds max %v", where, *n.Min, *n.Max)
			}
		}
	}
	return nil
}
