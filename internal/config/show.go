package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrKeyNotFound is returned by Render when the requested --key path does not
// resolve to any node in the merged effective config. Callers can errors.Is
// against this sentinel to map missing keys to a stable exit code.
var ErrKeyNotFound = errors.New("key not found")

// ErrRawNotScalar is returned by Render when --raw is requested against a
// non-scalar (mapping or sequence) node. The raw form is only meaningful for
// scalar leaves; sub-documents must be emitted as YAML or JSON.
var ErrRawNotScalar = errors.New("--raw is only valid on scalar values")

// renderView mirrors the canonical on-disk YAML schema of .nightgauge/
// config.yaml so dotted keys like `project.number` resolve regardless of how
// Config flattens the same fields in memory.
//
// The view is intentionally narrower than yamlConfigNested — it only carries
// fields the consuming verb is expected to surface.
type renderView struct {
	Project        renderProject           `yaml:"project,omitempty"`
	GitHubUser     string                  `yaml:"github_user,omitempty"`
	GitHubAuth     *GitHubAuthConfig       `yaml:"github_auth,omitempty"`
	LogLevel       string                  `yaml:"logLevel,omitempty"`
	APIKey         string                  `yaml:"api_key,omitempty"`
	Sanitization   *SanitizationConfig     `yaml:"sanitization,omitempty"`
	FeedbackLoop   *FeedbackLoopConfig     `yaml:"feedback_loop,omitempty"`
	Platform       *renderPlatform         `yaml:"platform,omitempty"`
	RemoteCommands *RemoteCommandsConfig   `yaml:"remote_commands,omitempty"`
	AgentTeams     *AgentTeamsConfig       `yaml:"agent_teams,omitempty"`
	Autonomous     *AutonomousConfig       `yaml:"autonomous,omitempty"`
	Knowledge      *KnowledgeConfig        `yaml:"knowledge,omitempty"`
	PipelineExec   *PipelineExecutorConfig `yaml:"pipeline_executor,omitempty"`
}

type renderProject struct {
	Owner          string             `yaml:"owner,omitempty"`
	OwnerType      string             `yaml:"owner_type,omitempty"`
	Number         int                `yaml:"number,omitempty"`
	Repo           string             `yaml:"repo,omitempty"`
	SizeToEstimate map[string]float64 `yaml:"size_to_estimate,omitempty"`
}

type renderPlatform struct {
	APIURL     string           `yaml:"api_url,omitempty"`
	LicenseKey string           `yaml:"license_key,omitempty"`
	Telemetry  *TelemetryConfig `yaml:"telemetry,omitempty"`
}

// toRenderView projects an in-memory Config into the canonical disk-shaped
// view used for `nightgauge config show` output.
func toRenderView(cfg *Config) renderView {
	var safeGitHubAuth *GitHubAuthConfig
	if cfg.GitHubAuth != nil {
		safeGitHubAuth = &GitHubAuthConfig{Users: cfg.GitHubAuth.Users, SuppressGHWarning: cfg.GitHubAuth.SuppressGHWarning}
		if cfg.GitHubAuth.Token != "" {
			safeGitHubAuth.Token = redactConfigSecret(cfg.GitHubAuth.Token)
		}
		if len(cfg.GitHubAuth.Tokens) > 0 {
			safeGitHubAuth.Tokens = make(map[string]string, len(cfg.GitHubAuth.Tokens))
			for owner, token := range cfg.GitHubAuth.Tokens {
				safeGitHubAuth.Tokens[owner] = redactConfigSecret(token)
			}
		}
	}
	v := renderView{
		Project: renderProject{
			Owner:          cfg.Owner,
			OwnerType:      cfg.OwnerType,
			Number:         cfg.ProjectNumber,
			Repo:           cfg.DefaultRepo,
			SizeToEstimate: cfg.SizeToEstimate,
		},
		GitHubUser:     cfg.GitHubUser,
		GitHubAuth:     safeGitHubAuth,
		LogLevel:       cfg.LogLevel,
		APIKey:         redactConfigSecret(cfg.APIKey),
		Sanitization:   cfg.Sanitization,
		FeedbackLoop:   cfg.FeedbackLoop,
		RemoteCommands: cfg.RemoteCommands,
		AgentTeams:     cfg.AgentTeams,
		Autonomous:     cfg.Autonomous,
		Knowledge:      cfg.Knowledge,
		PipelineExec:   cfg.PipelineExecutor,
	}
	// api_url / license_key mirror the on-disk platform: block (#333) — render
	// them nested, matching the schema the VSCode extension actually writes,
	// rather than as flat top-level keys.
	if cfg.Telemetry != nil || cfg.PlatformURL != "" || cfg.LicenseKey != "" {
		v.Platform = &renderPlatform{
			APIURL:     cfg.PlatformURL,
			LicenseKey: redactConfigSecret(cfg.LicenseKey),
			Telemetry:  cfg.Telemetry,
		}
	}
	return v
}

func redactConfigSecret(value string) string {
	if value == "" || strings.HasPrefix(value, "env:") {
		return value
	}
	return "<redacted>"
}

// Render returns a printable view of cfg in the canonical on-disk YAML schema.
//
// When key is empty, the entire effective config is rendered as YAML (or JSON
// when asJSON is true). When key is a dotted path (e.g. "project.number" or
// "autonomous.scan_interval"), only that branch of the YAML tree is returned;
// scalars print as their string value, mapping nodes print as a sub-document.
//
// The raw flag strips YAML quoting and trailing whitespace from a scalar leaf
// so callers can do `VAL=$(nightgauge config show --key X --raw)`.
// Combining raw with a non-scalar node returns ErrRawNotScalar.
func Render(cfg *Config, key string, asJSON bool, raw bool) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("nil config")
	}

	view := toRenderView(cfg)
	yamlBytes, err := yaml.Marshal(view)
	if err != nil {
		return "", fmt.Errorf("marshal config to yaml: %w", err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(yamlBytes, &doc); err != nil {
		return "", fmt.Errorf("parse re-rendered yaml: %w", err)
	}
	RedactYAML(&doc)

	// Render full output from the same redacted tree used by key/subtree/raw
	// requests so no output mode can bypass the central secret boundary.
	if key == "" {
		if raw {
			return "", ErrRawNotScalar
		}
		return renderNode(docRoot(&doc), asJSON)
	}
	root := docRoot(&doc)
	if root == nil {
		return "", fmt.Errorf("%w: %s", ErrKeyNotFound, key)
	}

	segments := strings.Split(key, ".")
	node := lookupYAMLPath(root, segments)
	if node == nil {
		return "", fmt.Errorf("%w: %s", ErrKeyNotFound, key)
	}

	if raw {
		if node.Kind != yaml.ScalarNode {
			return "", ErrRawNotScalar
		}
		return node.Value, nil
	}

	return renderNode(node, asJSON)
}

// docRoot returns the first content node of a YAML document, or nil for an
// empty document.
func docRoot(doc *yaml.Node) *yaml.Node {
	if doc == nil || doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil
	}
	return doc.Content[0]
}

// lookupYAMLPath walks segments through a mapping-keyed YAML tree. It returns
// the matched node or nil when any segment is missing or the path traverses
// through a non-mapping node. Sequence indexing is intentionally unsupported —
// the verb's contract is "mapping leaves only".
func lookupYAMLPath(node *yaml.Node, segments []string) *yaml.Node {
	current := node
	for _, seg := range segments {
		if current == nil || current.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for i := 0; i+1 < len(current.Content); i += 2 {
			k := current.Content[i]
			v := current.Content[i+1]
			if k.Value == seg {
				next = v
				break
			}
		}
		if next == nil {
			return nil
		}
		current = next
	}
	return current
}

// renderNode serializes a single yaml.Node back to YAML or JSON. Scalar nodes
// emit their bare value; mapping and sequence nodes emit their full
// sub-document.
func renderNode(node *yaml.Node, asJSON bool) (string, error) {
	if node.Kind == yaml.ScalarNode && !asJSON {
		return node.Value + "\n", nil
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(node); err != nil {
		_ = enc.Close()
		return "", fmt.Errorf("encode node to yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("close yaml encoder: %w", err)
	}

	if !asJSON {
		return buf.String(), nil
	}
	return yamlToJSON(buf.Bytes())
}

// yamlToJSON converts a YAML byte slice into pretty-printed JSON. It decodes
// into a generic structure (using string-keyed maps so the output is valid
// JSON) and re-encodes with two-space indentation.
func yamlToJSON(data []byte) (string, error) {
	var raw any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return "", fmt.Errorf("decode yaml for json conversion: %w", err)
	}
	normalized := normalizeForJSON(raw)
	out, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode json: %w", err)
	}
	return string(out) + "\n", nil
}

// normalizeForJSON walks the decoded YAML structure and converts
// map[interface{}]interface{} into map[string]interface{} so encoding/json can
// marshal it.
func normalizeForJSON(in any) any {
	switch v := in.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, val := range v {
			out[k] = normalizeForJSON(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(v))
		for k, val := range v {
			out[fmt.Sprintf("%v", k)] = normalizeForJSON(val)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, val := range v {
			out[i] = normalizeForJSON(val)
		}
		return out
	default:
		return v
	}
}
