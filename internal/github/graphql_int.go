package github

import (
	"fmt"
	"math"

	"github.com/shurcooL/graphql"
)

// checkedGraphQLInt converts a native int to GraphQL's signed 32-bit Int
// representation without allowing architecture-dependent truncation.
func checkedGraphQLInt(name string, value int) (graphql.Int, error) {
	if value < math.MinInt32 || value > math.MaxInt32 {
		return 0, fmt.Errorf("%s %d is outside the GraphQL Int range", name, value)
	}
	return graphql.Int(value), nil
}
