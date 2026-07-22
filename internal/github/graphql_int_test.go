package github

import (
	"math"
	"strconv"
	"testing"
)

func TestCheckedGraphQLInt(t *testing.T) {
	tests := []struct {
		name    string
		value   int
		wantErr bool
	}{
		{name: "zero", value: 0},
		{name: "maximum", value: math.MaxInt32},
		{name: "minimum", value: math.MinInt32},
	}
	if strconv.IntSize == 64 {
		tests = append(tests,
			struct {
				name    string
				value   int
				wantErr bool
			}{name: "above maximum", value: int(int64(math.MaxInt32) + 1), wantErr: true},
			struct {
				name    string
				value   int
				wantErr bool
			}{name: "below minimum", value: int(int64(math.MinInt32) - 1), wantErr: true},
		)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := checkedGraphQLInt("test value", tt.value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("checkedGraphQLInt() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && int(got) != tt.value {
				t.Fatalf("checkedGraphQLInt() = %d, want %d", got, tt.value)
			}
		})
	}
}
