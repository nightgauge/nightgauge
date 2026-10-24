package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// StageIdentities returns every GitHub login a stage subprocess on this
// machine can authenticate as (#2130): each account in the gh keyring (every
// one, not only the active account: a stage can run `gh auth token --user
// X`), the account behind each personal token in the environment, and
// the logins configured in github_auth.users or github_user. An App
// installation token (ghs_) names the App, a bot that can never approve, so it
// adds nothing.
//
// The eligibility gate refuses a `pipeline:approved` applied by any of these
// logins: a stage could have applied it. A read that cannot be completed is an
// error, and the gate then accepts no label approval at all: fail closed.
func StageIdentities(ctx context.Context, configured []string) ([]string, error) {
	seen := map[string]bool{}
	add := func(login string) {
		if l := strings.ToLower(strings.TrimSpace(login)); l != "" {
			seen[l] = true
		}
	}
	for _, c := range configured {
		add(c)
	}

	keyring, err := ghKeyringLogins(ctx)
	if err != nil {
		return nil, err
	}
	for _, l := range keyring {
		add(l)
	}

	// Every personal or OAuth token in the environment, under any name: a
	// config's `env:GITHUB_TOKEN_<ORG>` reference puts tokens in variables a
	// stage inherits. An App installation token (ghs_) names a bot.
	tokens := map[string]bool{}
	for _, kv := range os.Environ() {
		_, v, ok := strings.Cut(kv, "=")
		if v = strings.TrimSpace(v); ok && isPersonalToken(v) {
			tokens[v] = true
		}
	}
	for tok := range tokens {
		login, err := ghTokenLogin(ctx, tok)
		if err != nil {
			return nil, fmt.Errorf("resolve the account behind a GitHub token in the environment: %w", err)
		}
		add(login)
	}

	out := make([]string, 0, len(seen))
	for l := range seen {
		out = append(out, l)
	}
	sort.Strings(out)
	return out, nil
}

// ghKeyringLogins lists every account the gh keyring holds, on every host.
// gh being absent means a stage has no keyring to reach through it. gh present
// but unreadable is an error.
func ghKeyringLogins(ctx context.Context) ([]string, error) {
	if _, err := exec.LookPath(ghBinary); err != nil {
		return nil, nil
	}
	cmd := exec.CommandContext(ctx, ghBinary, "auth", "status", "--json", "hosts")
	// Without the env tokens, so gh reports its keyring, not the env token.
	cmd.Env = envWithout(os.Environ(), "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	var parsed struct {
		Hosts map[string][]struct {
			Login string `json:"login"`
		} `json:"hosts"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil || parsed.Hosts == nil {
		if runErr != nil && strings.Contains(strings.ToLower(stderr.String()+stdout.String()), "not logged in") {
			return nil, nil
		}
		return nil, fmt.Errorf("list the gh keyring's accounts: %v (%s)", errors.Join(runErr, err), strings.TrimSpace(stderr.String()))
	}
	var logins []string
	for _, accounts := range parsed.Hosts {
		for _, a := range accounts {
			if a.Login != "" {
				logins = append(logins, a.Login)
			}
		}
	}
	return logins, nil
}

// ghTokenLogin asks GitHub whose token tok is.
func ghTokenLogin(ctx context.Context, tok string) (string, error) {
	cmd := exec.CommandContext(ctx, ghBinary, "api", "user", "--jq", ".login")
	cmd.Env = append(envWithout(os.Environ(), "GH_TOKEN", "GITHUB_TOKEN"), "GH_TOKEN="+tok)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	login := strings.TrimSpace(string(out))
	if login == "" {
		return "", errors.New("GitHub named no account for the token")
	}
	return login, nil
}

// personalTokenPrefixes are GitHub's prefixes for tokens that act as a user:
// classic and fine-grained personal tokens, OAuth and user-to-server tokens.
var personalTokenPrefixes = []string{"ghp_", "github_pat_", "gho_", "ghu_"}

func isPersonalToken(v string) bool {
	for _, p := range personalTokenPrefixes {
		if strings.HasPrefix(v, p) {
			return true
		}
	}
	return false
}
