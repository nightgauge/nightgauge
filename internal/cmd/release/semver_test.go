package release

import "testing"

func TestParseSemver(t *testing.T) {
	cases := []struct {
		in      string
		want    []int
		wantErr bool
	}{
		{"1.2.3", []int{1, 2, 3}, false},
		{"v1.2.3", []int{1, 2, 3}, false},
		{"V2.0", []int{2, 0}, false},
		{"  v0.1  ", []int{0, 1}, false},
		{"", []int{0}, false},
		{"2.1.75", []int{2, 1, 75}, false},
		{"1.0.0-rc.1", nil, true},
		{"abc", nil, true},
		{"1..2", nil, true},
		// Multi-char non-numeric prefix (openai/codex Rust CLI tags) — #4056.
		{"rust-v0.141.0", []int{0, 141, 0}, false},
		{"rust-v1.2.3", []int{1, 2, 3}, false},
		// Codex pre-release tag stays unparseable → excluded by IsNewer.
		{"rust-v0.142.0-alpha.4", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseSemver(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseSemver(%q) err=%v wantErr=%v", tc.in, err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("parseSemver(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i, v := range got {
				if v != tc.want[i] {
					t.Fatalf("parseSemver(%q)[%d] = %d, want %d", tc.in, i, v, tc.want[i])
				}
			}
		})
	}
}

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0},
		{"v1.2.3", "1.2.3", 0},
		{"1.2.4", "1.2.3", 1},
		{"1.2.3", "1.2.4", -1},
		{"1.2", "1.2.0", 0},    // padding
		{"1.2", "1.2.1", -1},   // padding
		{"1.10.0", "1.2.0", 1}, // numeric, not lex
		{"2.0.0", "1.99.99", 1},
		{"v2.1.75", "v2.1.74", 1},
		{"", "0.0.0", 0},
	}
	for _, tc := range cases {
		t.Run(tc.a+"_vs_"+tc.b, func(t *testing.T) {
			got, err := compareSemver(tc.a, tc.b)
			if err != nil {
				t.Fatalf("compareSemver(%q, %q): %v", tc.a, tc.b, err)
			}
			if got != tc.want {
				t.Fatalf("compareSemver(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestCompareSemverErrors(t *testing.T) {
	if _, err := compareSemver("1.2.3-rc.1", "1.0.0"); err == nil {
		t.Errorf("expected error for prerelease tag, got nil")
	}
	if _, err := compareSemver("1.0.0", "abc"); err == nil {
		t.Errorf("expected error for non-numeric, got nil")
	}
}

func TestIsNewer(t *testing.T) {
	cases := []struct {
		tag, base string
		want      bool
	}{
		{"2.1.75", "2.1.74", true},
		{"2.1.74", "2.1.74", false},
		{"2.1.73", "2.1.74", false},
		{"v2.0", "1.99.99", true},
		{"1.0.0-rc.1", "1.0.0", false}, // unparseable → false (defensive)
		{"abc", "1.0.0", false},
	}
	for _, tc := range cases {
		t.Run(tc.tag+"_>_"+tc.base, func(t *testing.T) {
			if got := IsNewer(tc.tag, tc.base); got != tc.want {
				t.Fatalf("IsNewer(%q, %q) = %v, want %v", tc.tag, tc.base, got, tc.want)
			}
		})
	}
}
