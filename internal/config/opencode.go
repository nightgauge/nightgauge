package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	yaml "gopkg.in/yaml.v3"
)

// OpenCodeConfig is the `opencode:` block of the machine-tier config
// (ADR-022 § 7). Every value in it is a fact about one machine: which
// opencode binary to run, where the operator's model server listens and how
// much context it has loaded. The opencode adapter builds each run's
// OpenCode config from it, and so does `nightgauge opencode config`.
//
// It is read from the machine-tier file only (LoadOpenCodeConfig). The block
// decides where a stage's prompt and repository content are sent, so a
// committed repository config must never be able to set it.
//
// A single model server is written as the flat keys below. Its endpoint id,
// the OpenCode provider key a stage names on -m, follows from Provider:
// `lmstudio` for lm-studio and `ollama` for ollama.
type OpenCodeConfig struct {
	// Binary pins the opencode binary a dispatch spawns and the doctor
	// checks: the absolute path of an executable file. A relative path is
	// refused, never looked up on PATH; unset runs the opencode on PATH
	// (ADR-022 § 7, § 20).
	Binary string `yaml:"binary,omitempty" json:"binary,omitempty"`

	// InheritUserConfig layers the operator's own OpenCode config (their XDG
	// OpenCode config directory, ~/.opencode, and any managed OpenCode config
	// on the machine) back into pipeline runs (ADR-022 § 8).
	//
	// Default OFF. ADR-020 requires every default-off setting to state its
	// reason beside it, and the reason is SECURITY: that config can name
	// plugins, which run as in-process code, MCP servers, providers,
	// permissions and models, and a pipeline run must behave the same on
	// every machine. It is read from the machine tier only, so a committed
	// repository config can never turn it on. Stored logins are never
	// inherited either way: they live in the run's data directory.
	InheritUserConfig bool `yaml:"inherit_user_config,omitempty" json:"inherit_user_config,omitempty"`

	// Model is the <provider>/<model> a caller that names none runs, such as
	// `nightgauge opencode config` without --model.
	Model string `yaml:"model,omitempty" json:"model,omitempty"`

	// Provider is the kind of model server the operator runs: lm-studio or
	// ollama.
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`

	// BaseURL is the server's OpenAI-compatible API root, such as
	// http://127.0.0.1:1234/v1. It never leaves the machine: a run reads it
	// from a private file in its own root, never from the environment.
	BaseURL string `yaml:"base_url,omitempty" json:"base_url,omitempty"`

	// Limit is what the server has loaded, which applies to every model it
	// serves. Keep Context at or below the context the server has loaded,
	// not the model's maximum: OpenCode compacts a session only when it
	// knows the window, and a server that reports 0 never compacts.
	Limit OpenCodeLimit `yaml:"limit,omitempty" json:"limit,omitempty"`

	// Timeouts bound the wait for the server's response headers and between
	// streamed chunks. A cold prefill of a large loaded context can take more
	// than a minute before the first byte.
	Timeouts OpenCodeTimeouts `yaml:"timeouts,omitempty" json:"timeouts,omitempty"`

	// Snapshot, LSP and Formatter override ADR-022 § 12's pipeline defaults
	// (snapshot off, lsp and formatter on). Nil keeps the default.
	Snapshot  *bool `yaml:"snapshot,omitempty" json:"snapshot,omitempty"`
	LSP       *bool `yaml:"lsp,omitempty" json:"lsp,omitempty"`
	Formatter *bool `yaml:"formatter,omitempty" json:"formatter,omitempty"`

	// Endpoints declares any number of additional named model server
	// instances beyond the single flat-key endpoint above (ADR-022
	// § Endpoints, #1678): two LM Studio instances, LM Studio beside Ollama,
	// or any OpenAI-compatible server. Each becomes its own OpenCode
	// provider block, keyed by its id.
	Endpoints []OpenCodeEndpointConfig `yaml:"endpoints,omitempty" json:"endpoints,omitempty"`
}

// OpenCodeLimit is a model server's token limits.
type OpenCodeLimit struct {
	Context int `yaml:"context,omitempty" json:"context,omitempty"`
	Output  int `yaml:"output,omitempty" json:"output,omitempty"`
}

// OpenCodeTimeouts are a model server's request timeouts, as durations such
// as "3m". Zero keeps the adapter's default.
type OpenCodeTimeouts struct {
	Header YAMLDuration `yaml:"header,omitempty" json:"header,omitempty"`
	Chunk  YAMLDuration `yaml:"chunk,omitempty" json:"chunk,omitempty"`
}

// LoadOpenCodeConfig returns the `opencode:` block of the machine-tier config
// (the file MachineConfigPath names, or its Linux legacy location), or a zero
// value when the file or the block is absent.
//
// worktreeDir, when set, is the checkout a stage runs in. Its committed
// project config must not declare `opencode:`: a repository must not choose
// where its stage's code is sent, or grant itself the operator's OpenCode
// config. A declaration there is an error naming the machine-tier file, not
// something to merge or silently drop. The checkout's gitignored local tier
// is not read, because a stage can write its own worktree.
//
// Unknown keys inside the block are an error, so a misspelled key is reported
// rather than read as missing.
func LoadOpenCodeConfig(worktreeDir string) (OpenCodeConfig, error) {
	machinePath, pathErr := machineConfigPathFn()
	if pathErr != nil {
		machinePath = filepath.Join("~", ".nightgauge", "config.yaml")
	}
	if worktreeDir != "" {
		project := filepath.Join(worktreeDir, ".nightgauge", "config.yaml")
		data, err := os.ReadFile(project)
		switch {
		case err == nil:
			if root := parseYAMLRoot(data); root != nil && nodeHasPath(root, []string{"opencode"}) {
				return OpenCodeConfig{}, fmt.Errorf(
					"%s declares opencode:, which is read only from the machine-tier config (%s): it decides where a stage's code is sent, so a committed repository config cannot set it. "+
						"Move the block to %s and remove it from the repository. See docs/SETTINGS_ARCHITECTURE.md",
					project, machinePath, machinePath)
			}
		case !errors.Is(err, fs.ErrNotExist):
			return OpenCodeConfig{}, fmt.Errorf("read project config %q: %w", project, err)
		}
	}

	data, err := readMachineConfigBytes()
	if errors.Is(err, errConfigNotFound) {
		return OpenCodeConfig{}, nil
	}
	if err != nil {
		return OpenCodeConfig{}, err
	}
	return parseOpenCodeConfig(data, machinePath)
}

// parseOpenCodeConfig decodes the `opencode:` block of a machine-tier YAML
// document read from source.
func parseOpenCodeConfig(data []byte, source string) (OpenCodeConfig, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return OpenCodeConfig{}, fmt.Errorf("parse machine config %q: %w", source, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return OpenCodeConfig{}, nil
	}
	root := doc.Content[0]
	if !nodeHasPath(root, []string{"opencode"}) {
		return OpenCodeConfig{}, nil
	}
	block := findChildByKey(root, "opencode")
	if block.Kind != yaml.MappingNode {
		return OpenCodeConfig{}, fmt.Errorf("machine config %q: opencode: must be a mapping", source)
	}
	// Re-encode the block alone so it can be decoded strictly: yaml.v3
	// reports unknown fields only through a Decoder.
	raw, err := yaml.Marshal(block)
	if err != nil {
		return OpenCodeConfig{}, fmt.Errorf("machine config %q: opencode: %w", source, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var cfg OpenCodeConfig
	if err := dec.Decode(&cfg); err != nil {
		return OpenCodeConfig{}, fmt.Errorf("machine config %q: opencode: %w", source, err)
	}
	return cfg, nil
}
