package adapters

import (
	"os"
	"slices"
	"strings"
)

// The stage environment's credential allowlist (ADR-023 Decision 7).
//
// An agent stage reads issue text and repository files, so whatever is in its
// environment is reachable by a prompt it did not write. The stage keeps the
// host environment's toolchain variables (PATH, HOME, GOPATH, JAVA_HOME,
// proxy and TLS trust settings and the like), because the builds and tests it
// runs read them and no fixed list covers every toolchain. A variable whose
// NAME marks it as a credential reaches the stage only when it is in the base
// set every stage gets, or the adapter's harness declares it:
//
//   - the base set (stageBaseCredentialEnv) is the forge tokens the stage's
//     gh, git and nightgauge calls use, and the cloud platform families the
//     OpenCode adapter keeps for the same reason (openCodePlatformEnvNames,
//     ADR-022 § 8);
//   - each adapter declares the names and prefixes its harness authenticates
//     with (adapterCredentialEnv);
//   - every model service's key the OpenCode catalog names is withheld from a
//     harness that does not declare it, whatever its name looks like;
//   - the operator can pass more names, from the daemon's own environment,
//     in StageEnvAllowVar (comma-separated). A repository cannot set it.
//
// The decision reads only the name, never a value, so nothing it withholds
// can be logged.

// StageEnvAllowVar names the operator's extra stage credentials, a
// comma-separated list of variable names read from the daemon's environment.
const StageEnvAllowVar = "NIGHTGAUGE_STAGE_ENV_ALLOW"

// stageBaseCredentialEnv are credential-named variables every stage keeps:
// the forge tokens and the AWS session variables the platform family needs
// alongside openCodePlatformEnvNames.
var stageBaseCredentialEnv = map[string]bool{
	"GH_TOKEN":                true,
	"GITHUB_TOKEN":            true,
	"GH_ENTERPRISE_TOKEN":     true,
	"GITHUB_ENTERPRISE_TOKEN": true,
	"GITLAB_TOKEN":            true,
	"AWS_SESSION_TOKEN":       true,
	"AWS_SECURITY_TOKEN":      true,
}

// credentialEnvDecl is what one adapter's harness authenticates with.
type credentialEnvDecl struct {
	names    []string
	prefixes []string
	// catalogOwn reports that the adapter decides OpenCode catalog
	// variables itself (WithholdsEnv), so the shared rule leaves them alone.
	catalogOwn bool
}

// adapterCredentialEnv maps an adapter name to the credentials its harness
// reads. An adapter that is not listed gets the base set only.
var adapterCredentialEnv = map[string]credentialEnvDecl{
	"claude": {
		names:    []string{"CLAUDE_CODE_OAUTH_TOKEN", "AWS_BEARER_TOKEN_BEDROCK"},
		prefixes: []string{"ANTHROPIC_"},
	},
	"claude-sdk": {
		names:    []string{"CLAUDE_CODE_OAUTH_TOKEN", "AWS_BEARER_TOKEN_BEDROCK"},
		prefixes: []string{"ANTHROPIC_"},
	},
	"codex": {
		prefixes: []string{"OPENAI_", "CODEX_"},
	},
	"gemini": {
		names:    []string{"GOOGLE_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY"},
		prefixes: []string{"GEMINI_"},
	},
	"gemini-sdk": {
		names:    []string{"GOOGLE_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY"},
		prefixes: []string{"GEMINI_"},
	},
	"copilot": {
		prefixes: []string{"COPILOT_"},
	},
	"grok": {
		prefixes: []string{"XAI_", "GROK_"},
	},
	"opencode": {catalogOwn: true},
}

// credentialEnvName reports whether a variable's name marks it as a
// credential: a final segment of TOKEN, KEY, SECRET, PASSWORD, PASSWD,
// APIKEY, PAT, CREDENTIAL(S) or DSN, or a SECRET, PASSWORD, PASSWD, APIKEY or
// WEBHOOK segment anywhere. MAX_THINKING_TOKENS and a TOKEN_BUDGET setting are
// not credentials and do not match.
func credentialEnvName(key string) bool {
	segs := strings.Split(strings.ToUpper(key), "_")
	switch segs[len(segs)-1] {
	case "TOKEN", "KEY", "SECRET", "PASSWORD", "PASSWD", "APIKEY", "PAT",
		"CREDENTIAL", "CREDENTIALS", "DSN":
		return true
	}
	for _, s := range segs {
		switch s {
		case "SECRET", "PASSWORD", "PASSWD", "APIKEY", "WEBHOOK":
			return true
		}
	}
	return false
}

// StageWithholdsCredentialEnv reports whether an inherited variable named key
// must not reach a stage run by the adapter named adapter. extraAllow is the
// operator's StageEnvAllowVar list.
func StageWithholdsCredentialEnv(adapter, key string, extraAllow []string) bool {
	if stageBaseCredentialEnv[key] || openCodePlatformEnvNames[key] || slices.Contains(extraAllow, key) {
		return false
	}
	decl := adapterCredentialEnv[adapter]
	if slices.Contains(decl.names, key) {
		return false
	}
	for _, p := range decl.prefixes {
		if strings.HasPrefix(key, p) {
			return false
		}
	}
	if openCodeCatalogEnvNames[key] {
		// A model service's key: the OpenCode adapter decides these per
		// dispatch; every other adapter has declared the ones it reads above.
		return !decl.catalogOwn
	}
	return credentialEnvName(key)
}

// StageEnvAllow is the operator's StageEnvAllowVar list from this process's
// environment, trimmed, with empty entries dropped.
func StageEnvAllow() []string {
	var out []string
	for _, n := range strings.Split(os.Getenv(StageEnvAllowVar), ",") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// StageEnvWithhold is the complete withhold decision for a stage spawned by
// runner: the adapter's own WithholdsEnv hook (OpenCode's, ADR-022 § 8), then
// the shared credential allowlist. The manager and every other spawn of an
// agent harness compose the child environment through it.
func StageEnvWithhold(runner SkillRunner, opts RunOptions) func(key string) bool {
	own, _ := runner.(interface {
		WithholdsEnv(RunOptions, string) bool
	})
	name := runner.Name()
	extra := StageEnvAllow()
	return func(key string) bool {
		if own != nil && own.WithholdsEnv(opts, key) {
			return true
		}
		return StageWithholdsCredentialEnv(name, key, extra)
	}
}
