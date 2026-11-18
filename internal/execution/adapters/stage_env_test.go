package adapters

import (
	"slices"
	"testing"
)

// parentEnv is a host environment holding the operator's other credentials
// beside the toolchain a stage needs.
var parentEnv = map[string]string{
	// Toolchain, proxy and TLS trust: every stage keeps these.
	"PATH": "/usr/bin", "HOME": "/home/op", "GOPATH": "/home/op/go",
	"JAVA_HOME": "/opt/jdk", "SSH_AUTH_SOCK": "/tmp/agent.sock",
	"HTTPS_PROXY": "http://proxy:3128", "https_proxy": "http://proxy:3128",
	"HTTP_PROXY": "http://proxy:3128", "NO_PROXY": "localhost", "no_proxy": "localhost",
	"SSL_CERT_FILE": "/etc/ca.pem", "SSL_CERT_DIR": "/etc/certs",
	"NODE_EXTRA_CA_CERTS": "/etc/ca.pem", "REQUESTS_CA_BUNDLE": "/etc/ca.pem",
	"MAX_THINKING_TOKENS": "1024", "NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_USD": "5",
	"NIGHTGAUGE_SKILL_DIR": "/skills",
	// Forge and cloud platform credentials: base set.
	"GH_TOKEN": "x", "GITHUB_TOKEN": "x", "GITLAB_TOKEN": "x",
	"AWS_ACCESS_KEY_ID": "x", "AWS_SECRET_ACCESS_KEY": "x", "AWS_SESSION_TOKEN": "x",
	// Never a stage's.
	"NIGHTGAUGE_LICENSE_KEY": "x", "NIGHTGAUGE_JIRA_TOKEN": "x",
	"SLACK_BOT_TOKEN": "x", "DISCORD_WEBHOOK_URL": "x", "NIGHTGAUGE_SHIP_NOTIFY_WEBHOOK": "x",
	"STRIPE_SECRET_KEY": "x", "DB_PASSWORD": "x", "NPM_TOKEN": "x",
	// Provider credentials: only the declaring harness.
	"ANTHROPIC_API_KEY": "x", "CLAUDE_CODE_OAUTH_TOKEN": "x",
	"OPENAI_API_KEY": "x", "GEMINI_API_KEY": "x", "GOOGLE_API_KEY": "x",
	"XAI_API_KEY": "x", "COPILOT_GITHUB_TOKEN": "x", "MISTRAL_API_KEY": "x",
}

var alwaysKept = []string{
	"PATH", "HOME", "GOPATH", "JAVA_HOME", "SSH_AUTH_SOCK",
	"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "NO_PROXY", "no_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS", "REQUESTS_CA_BUNDLE",
	"MAX_THINKING_TOKENS", "NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_USD", "NIGHTGAUGE_SKILL_DIR",
	"GH_TOKEN", "GITHUB_TOKEN", "GITLAB_TOKEN",
	"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
}

var providerKeys = []string{
	"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "OPENAI_API_KEY", "GEMINI_API_KEY",
	"GOOGLE_API_KEY", "XAI_API_KEY", "COPILOT_GITHUB_TOKEN", "MISTRAL_API_KEY",
}

func TestStageWithholdsCredentialEnvPerAdapter(t *testing.T) {
	t.Parallel()
	cases := map[string][]string{
		"claude":     {"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"},
		"claude-sdk": {"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"},
		"codex":      {"OPENAI_API_KEY"},
		"gemini":     {"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		"gemini-sdk": {"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		"copilot":    {"COPILOT_GITHUB_TOKEN"},
		"grok":       {"XAI_API_KEY"},
	}
	for adapter, declared := range cases {
		t.Run(adapter, func(t *testing.T) {
			t.Parallel()
			var got []string
			for k := range parentEnv {
				if !StageWithholdsCredentialEnv(adapter, k, nil) {
					got = append(got, k)
				}
			}
			want := append(slices.Clone(alwaysKept), declared...)
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("%s child env:\n got  %v\n want %v", adapter, got, want)
			}
		})
	}
}

func TestStageEnvAllowAddsOperatorNames(t *testing.T) {
	t.Parallel()
	if !StageWithholdsCredentialEnv("claude", "NPM_TOKEN", nil) {
		t.Fatal("NPM_TOKEN reached a stage the operator did not allow it to")
	}
	if StageWithholdsCredentialEnv("claude", "NPM_TOKEN", []string{"NPM_TOKEN"}) {
		t.Fatal("NPM_TOKEN was withheld although the operator allowed it")
	}
}

func TestStageEnvWithholdComposesTheAdapterHook(t *testing.T) {
	t.Setenv(StageEnvAllowVar, " NPM_TOKEN , ,")
	oc := &OpenCodeAdapter{}
	w := StageEnvWithhold(oc, RunOptions{Model: "anthropic/claude-sonnet-4-5"})
	for key, want := range map[string]bool{
		"ANTHROPIC_API_KEY":      false, // OpenCode's own rule keeps the dispatched provider
		"OPENAI_API_KEY":         true,  // and withholds every other
		"OPENCODE_CONFIG":        true,  // OpenCode's own rule
		"SLACK_BOT_TOKEN":        true,  // the shared credential rule
		"NIGHTGAUGE_LICENSE_KEY": true,
		"NPM_TOKEN":              false, // the operator's allow list
		"PATH":                   false,
	} {
		if got := w(key); got != want {
			t.Errorf("opencode withhold(%s) = %v, want %v", key, got, want)
		}
	}
	cw := StageEnvWithhold(&ClaudeAdapter{}, RunOptions{})
	for _, k := range providerKeys {
		want := k != "ANTHROPIC_API_KEY" && k != "CLAUDE_CODE_OAUTH_TOKEN"
		if got := cw(k); got != want {
			t.Errorf("claude withhold(%s) = %v, want %v", k, got, want)
		}
	}
}

func TestCredentialEnvName(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]bool{
		"SLACK_BOT_TOKEN": true, "STRIPE_SECRET_KEY": true, "DB_PASSWORD": true,
		"DISCORD_WEBHOOK_URL": true, "SENTRY_DSN": true, "GOOGLE_APPLICATION_CREDENTIALS": true,
		"MY_APIKEY": true, "CLIENT_SECRET_PATH": true, "HF_PAT": true,
		"MAX_THINKING_TOKENS": false, "CLAUDE_CODE_MAX_OUTPUT_TOKENS": false,
		"NIGHTGAUGE_PIPELINE_TOKEN_BUDGET_CEILING_USD": false, "SSH_AUTH_SOCK": false,
		"GIT_ASKPASS": false, "KEYCHAIN_PATH": false, "PATH": false,
	} {
		if got := credentialEnvName(name); got != want {
			t.Errorf("credentialEnvName(%s) = %v, want %v", name, got, want)
		}
	}
}
