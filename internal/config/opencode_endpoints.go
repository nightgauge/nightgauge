package config

// OpenCodeEndpointConfig is one named model server in the machine-tier
// `opencode.endpoints[]` list (ADR-022 § Endpoints, #1678). It extends the
// single flat-key endpoint (OpenCodeConfig's Provider/BaseURL/Limit fields)
// to any number of named instances: two LM Studio servers, LM Studio beside
// Ollama, or any OpenAI-compatible server (vLLM, llama.cpp server, LocalAI).
//
// It is read from the machine tier only, through the same `opencode:` block
// LoadOpenCodeConfig refuses in a committed project-tier config, so an
// endpoint's address can never be set by a repository.
type OpenCodeEndpointConfig struct {
	// ID is the operator-chosen instance name, and the OpenCode provider key
	// a stage names on -m: lowercase letters, digits and -, at most 32
	// characters, never starting with -, and unique among every endpoint
	// (this list and the flat legacy keys). It must not collide with a
	// provider key OpenCode's bundled catalog already uses, except lmstudio
	// on a lm-studio entry (ADR-022 § Endpoints).
	ID string `yaml:"id" json:"id"`

	// Provider is the endpoint's kind: lm-studio, ollama or
	// openai-compatible. openai-compatible is the primary and only required
	// kind (2026-09-20 scope narrowing); lm-studio and ollama are optional
	// labels with no protocol-specific readiness probe of their own.
	Provider string `yaml:"provider" json:"provider"`

	// BaseURL is the server's OpenAI-compatible API root. It never leaves
	// the machine: a run reads it from a private file in its own root, never
	// from the environment, and it is redacted from every captured output.
	BaseURL string `yaml:"base_url" json:"base_url"`

	// AllowLAN opts an entry into a base_url that is not on this machine. It
	// must also resolve to a private-network address (RFC 1918, or an IPv6
	// unique-local address): a documentation address such as 192.0.2.10 is
	// refused even with AllowLAN set.
	AllowLAN bool `yaml:"allow_lan,omitempty" json:"allow_lan,omitempty"`

	// Limit is the context and output the server has loaded, or is
	// configured to load, for every model on it. Unlike the flat legacy
	// endpoint, an endpoint here is never probed for its loaded context
	// (2026-09-20 scope narrowing drops the LM-Studio-specific probe), so
	// this is the honest, declared source of truth: it must be set.
	Limit OpenCodeLimit `yaml:"limit,omitempty" json:"limit,omitempty"`

	// Timeouts bound the wait for the server's response headers and between
	// streamed chunks. Zero keeps the adapter's default.
	Timeouts OpenCodeTimeouts `yaml:"timeouts,omitempty" json:"timeouts,omitempty"`

	// APIKeyEnv names the environment variable this process holds the
	// endpoint's credential in, when it has one. It is stored as a variable
	// name only: the generated config resolves it to {env:VAR}, never a
	// literal, and this struct never carries the value itself.
	APIKeyEnv string `yaml:"api_key_env,omitempty" json:"api_key_env,omitempty"`

	// SelfHosted marks a model the endpoint runs itself, as opposed to one it
	// forwards to a hosted service through a gateway or proxy (ADR-022
	// § Endpoints, "An endpoint can forward"). Cost stamping's consumption of
	// this field is #1679's job, not this issue's; it is only parsed, stored
	// and passed through here.
	SelfHosted bool `yaml:"self_hosted,omitempty" json:"self_hosted,omitempty"`

	// MaxConcurrency is the declared capacity of the endpoint: how many
	// concurrent dispatches the operator judges it can serve. It is never
	// measured or probed, only declared and surfaced by the doctor as
	// "slots". Zero means not declared.
	MaxConcurrency int `yaml:"max_concurrency,omitempty" json:"max_concurrency,omitempty"`

	// Models are the model ids the operator declares on this endpoint. The
	// adapter and the doctor never inspect Variants beyond passing it
	// through unfiltered into the generated provider block; #1643's
	// --variant mapping is what reads it.
	Models []OpenCodeEndpointModel `yaml:"models,omitempty" json:"models,omitempty"`
}

// OpenCodeEndpointModel is one model an operator declares on an endpoint.
type OpenCodeEndpointModel struct {
	// ID is the model id OpenCode dispatches, after the endpoint's provider
	// key (endpoint-id/model-id on -m).
	ID string `yaml:"id" json:"id"`

	// Variants is an opaque passthrough: this package and the adapter never
	// inspect its contents, including which entries are disabled. #1643
	// reads it for its --variant mapping.
	Variants []string `yaml:"variants,omitempty" json:"variants,omitempty"`
}
