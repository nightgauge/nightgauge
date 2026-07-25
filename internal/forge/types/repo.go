package forgetypes

// Repo is the forge-agnostic repository metadata shape returned by
// RepoService.RepoMetadata. Field names mirror `gh repo view --json
// nameWithOwner,owner,name` so jq pipelines parsing the gh output can be
// reused verbatim against `nightgauge forge repo view --json`.
type Repo struct {
	NameWithOwner string `json:"nameWithOwner"`
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	// DefaultBranch is the branch every PR targets by default ("main",
	// "master", "trunk"). Empty when the repository has no commits yet, or
	// when the adapter cannot report it — callers must treat empty as "I do
	// not know" and skip, never as a licence to guess "main".
	DefaultBranch string `json:"defaultBranch,omitempty"`
}

// Actor is a lightweight representation of a forge user / actor — used
// by AuthService.Whoami and (in the future) by `forge auth status`'s
// caller-display fields.
type Actor struct {
	Login string `json:"login"`
}
