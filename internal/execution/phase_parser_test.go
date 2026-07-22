package execution

import "testing"

func TestParsePhaseMarker(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		want   *PhaseMarker
		wantOK bool
	}{
		{
			name:   "valid marker",
			line:   `<!-- phase:start name="validate-environment" index=0 total=14 stage="feature-dev" -->`,
			want:   &PhaseMarker{Name: "validate-environment", Index: 0, Total: 14, Stage: "feature-dev"},
			wantOK: true,
		},
		{
			name:   "marker with extra whitespace",
			line:   `<!--  phase:start  name="read-planning-context"  index=1  total=14  stage="feature-dev"  -->`,
			want:   &PhaseMarker{Name: "read-planning-context", Index: 1, Total: 14, Stage: "feature-dev"},
			wantOK: true,
		},
		{
			name:   "marker embedded in text",
			line:   `some output <!-- phase:start name="implementation" index=6 total=14 stage="feature-dev" --> more text`,
			want:   &PhaseMarker{Name: "implementation", Index: 6, Total: 14, Stage: "feature-dev"},
			wantOK: true,
		},
		{
			name:   "not a phase marker",
			line:   `{"type":"result","usage":{"input_tokens":100}}`,
			want:   nil,
			wantOK: false,
		},
		{
			name:   "empty line",
			line:   ``,
			want:   nil,
			wantOK: false,
		},
		{
			name:   "incomplete marker",
			line:   `<!-- phase:start name="test" index=0 -->`,
			want:   nil,
			wantOK: false,
		},
		{
			name:   "different HTML comment",
			line:   `<!-- this is just a comment -->`,
			want:   nil,
			wantOK: false,
		},
		{
			name:   "different stage",
			line:   `<!-- phase:start name="quality-review" index=8 total=14 stage="feature-validate" -->`,
			want:   &PhaseMarker{Name: "quality-review", Index: 8, Total: 14, Stage: "feature-validate"},
			wantOK: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParsePhaseMarker(tc.line)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if got.Name != tc.want.Name {
				t.Errorf("Name = %q, want %q", got.Name, tc.want.Name)
			}
			if got.Index != tc.want.Index {
				t.Errorf("Index = %d, want %d", got.Index, tc.want.Index)
			}
			if got.Total != tc.want.Total {
				t.Errorf("Total = %d, want %d", got.Total, tc.want.Total)
			}
			if got.Stage != tc.want.Stage {
				t.Errorf("Stage = %q, want %q", got.Stage, tc.want.Stage)
			}
		})
	}
}
