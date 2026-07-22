package forgetypes

// Repo is the forge-agnostic repository metadata shape returned by
// RepoService.RepoMetadata. Field names mirror `gh repo view --json
// nameWithOwner,owner,name` so jq pipelines parsing the gh output can be
// reused verbatim against `nightgauge forge repo view --json`.
type Repo struct {
	NameWithOwner string `json:"nameWithOwner"`
	Owner         string `json:"owner"`
	Name          string `json:"name"`
}

// Actor is a lightweight representation of a forge user / actor — used
// by AuthService.Whoami and (in the future) by `forge auth status`'s
// caller-display fields.
type Actor struct {
	Login string `json:"login"`
}
