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

// RepoFile is one path of a repository as its forge serves it at a commit.
type RepoFile struct {
	// Regular is true when the path is a regular file. A symbolic link, a
	// directory or a submodule at the path is not, and has no Content.
	Regular bool `json:"regular"`
	// Content is the file's bytes when Regular.
	Content []byte `json:"content,omitempty"`
}

// DefaultBranchFiles is the head of a repository's default branch as its
// forge reports it, and files read at that commit in the same answer.
type DefaultBranchFiles struct {
	// Branch is the default branch's name.
	Branch string `json:"branch"`
	// Commit is the object id of the commit at the branch's head.
	Commit string `json:"commit"`
	// Files holds each path asked for that the commit has; a path the commit
	// does not have is absent.
	Files map[string]RepoFile `json:"files"`
}
