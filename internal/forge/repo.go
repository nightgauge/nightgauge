package forge

import (
	"context"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// RepoService is the forge-agnostic surface for read-only repository
// metadata. Mirrors the field set returned by `gh repo view --json
// nameWithOwner,owner,name` so jq pipelines parsing the gh output can be
// reused verbatim against `nightgauge forge repo view --json`.
type RepoService interface {
	// RepoMetadata returns the canonical name/owner pair for the named
	// repository. Adapters that do not separate owner namespaces from
	// repository names return Owner == "" and Name == NameWithOwner.
	RepoMetadata(ctx context.Context, owner, name string) (*forgetypes.Repo, error)
}

// GraphQLService is an optional surface — not part of ForgeClient — that
// the GitHub adapter implements to support the `forge graphql` pass-
// through subcommand. Adapters that do not expose a GraphQL transport
// (or that intentionally restrict ad-hoc queries) return
// ErrUnsupported wrapped via %w from ExecuteGraphQL.
//
// The contract is intentionally minimal: a raw query string plus a
// variables map, returning the raw JSON envelope. The CLI layer in
// cmd/nightgauge/forge/graphql.go does the flag parsing
// (-f key=value, -F key=value) and prints the envelope verbatim.
type GraphQLService interface {
	ExecuteGraphQL(ctx context.Context, query string, variables map[string]interface{}) ([]byte, error)
}

// DefaultBranchFileService is an optional surface, not part of ForgeClient,
// that reads files of a repository at the head of its default branch, as the
// forge serves them. The branch, its head and every file come from one
// answer, so no file is read at another commit. The GitHub adapter's
// RepoService implements it.
type DefaultBranchFileService interface {
	// DefaultBranchFiles returns the head of owner/name's default branch and
	// each of paths at that commit. A path the commit does not have is absent
	// from Files and is not an error; a repository the forge does not show,
	// or one with no default branch, is.
	DefaultBranchFiles(ctx context.Context, owner, name string, paths []string) (*forgetypes.DefaultBranchFiles, error)
}
