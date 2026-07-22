package forgecmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/nightgauge/nightgauge/internal/forge"
	"github.com/spf13/cobra"
)

// graphqlCmd is `forge graphql` — a thin pass-through to the underlying
// forge's GraphQL transport. Mirrors `gh api graphql -f key=val
// -F key=val` so skill files migrate by replacing the binary name.
//
// The adapter must satisfy forge.GraphQLService; adapters that do not
// expose a GraphQL endpoint (e.g. GitLab today) return ErrUnsupported
// at execution time. Carve-outs documented in ADR-008.
func graphqlCmd() *cobra.Command {
	var (
		stringPairs []string
		rawPairs    []string
		queryFile   string
	)
	cmd := &cobra.Command{
		Use:          "graphql",
		Short:        "Execute a raw GraphQL query / mutation against the active forge",
		Long:         longGraphQL,
		SilenceUsage: true,
		Example: `  nightgauge forge graphql -f query='query { viewer { login } }'
  nightgauge forge graphql -f query=@query.graphql -F number=42`,
		RunE: func(cmd *cobra.Command, args []string) error {
			query, vars, err := buildGraphQLPayload(stringPairs, rawPairs, queryFile, cmd.InOrStdin())
			if err != nil {
				return emitError(cmd, err)
			}
			if strings.TrimSpace(query) == "" {
				return emitError(cmd, fmt.Errorf("missing 'query' (pass with -f query='...' or --query-file <path>)"))
			}
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			gqlClient, ok := client.(forge.GraphQLService)
			if !ok {
				return emitError(cmd, fmt.Errorf("graphql: %w (active forge does not expose a GraphQL transport)", forge.ErrUnsupported))
			}
			raw, err := gqlClient.ExecuteGraphQL(cmd.Context(), query, vars)
			if err != nil {
				return emitError(cmd, fmt.Errorf("graphql: %w", err))
			}
			_, werr := cmd.OutOrStdout().Write(append(raw, '\n'))
			return werr
		},
	}
	cmd.Flags().StringArrayVarP(&stringPairs, "field", "f", nil,
		"key=value pair (string). Special key 'query' carries the GraphQL document; @file reads from file.")
	cmd.Flags().StringArrayVarP(&rawPairs, "raw-field", "F", nil,
		"key=value pair (parsed as JSON: numbers, booleans, null pass-through)")
	cmd.Flags().StringVar(&queryFile, "query-file", "",
		"Read the GraphQL document from <path> (alternative to -f query=@<path>)")
	return cmd
}

// buildGraphQLPayload parses the gh-style -f / -F flag values into a
// (query, variables) pair. The 'query' key is extracted from -f and
// treated specially — its value is the GraphQL document. All other -f
// values are string variables; -F values are JSON-decoded.
//
// The function is exported in spirit (its body is the testable surface)
// but kept package-private — table-driven tests live in graphql_test.go.
func buildGraphQLPayload(stringPairs, rawPairs []string, queryFile string, stdin io.Reader) (string, map[string]interface{}, error) {
	vars := map[string]interface{}{}
	var query string

	for _, pair := range stringPairs {
		k, v, err := splitKeyValue(pair)
		if err != nil {
			return "", nil, fmt.Errorf("-f %s: %w", pair, err)
		}
		if strings.HasPrefix(v, "@") {
			data, err := readFileOrStdin(v[1:], stdin)
			if err != nil {
				return "", nil, fmt.Errorf("-f %s: %w", pair, err)
			}
			v = data
		}
		if k == "query" {
			query = v
			continue
		}
		vars[k] = v
	}

	for _, pair := range rawPairs {
		k, v, err := splitKeyValue(pair)
		if err != nil {
			return "", nil, fmt.Errorf("-F %s: %w", pair, err)
		}
		decoded, err := decodeRawValue(v)
		if err != nil {
			return "", nil, fmt.Errorf("-F %s: %w", pair, err)
		}
		vars[k] = decoded
	}

	if queryFile != "" {
		if query != "" {
			return "", nil, fmt.Errorf("--query-file conflicts with -f query=...")
		}
		data, err := readFileOrStdin(queryFile, stdin)
		if err != nil {
			return "", nil, fmt.Errorf("--query-file %s: %w", queryFile, err)
		}
		query = data
	}

	return query, vars, nil
}

// splitKeyValue splits "key=value" into its parts. The first '=' is the
// delimiter; everything after it is the value verbatim (so values may
// themselves contain '=' or '@').
func splitKeyValue(pair string) (string, string, error) {
	idx := strings.Index(pair, "=")
	if idx <= 0 {
		return "", "", fmt.Errorf("expected key=value, got %q", pair)
	}
	return pair[:idx], pair[idx+1:], nil
}

// readFileOrStdin returns the contents of path; the special value "-"
// reads from stdin. Used to support -f query=@file and @- semantics
// (matches `gh api`'s behaviour).
func readFileOrStdin(path string, stdin io.Reader) (string, error) {
	if path == "-" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// decodeRawValue parses the string form of a -F value as JSON. Numbers,
// booleans, null, and JSON-encoded objects/arrays round-trip through
// the variables map. Bare unquoted strings fall back to a string value
// so callers can pass `-F name=alice` without quoting JSON-style.
func decodeRawValue(v string) (interface{}, error) {
	trim := strings.TrimSpace(v)
	if trim == "" {
		return v, nil
	}
	var out interface{}
	if err := json.Unmarshal([]byte(trim), &out); err == nil {
		return out, nil
	}
	// Fall back to verbatim string — matches gh's permissive -F behaviour
	// for plain strings ("alice", "main", etc.).
	return v, nil
}
