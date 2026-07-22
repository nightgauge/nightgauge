package queue

import "testing"

func TestIsEpic(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   bool
	}{
		{"epic label", []string{"type:epic"}, true},
		{"feature label only", []string{"type:feature", "size:S"}, false},
		{"epic with other labels", []string{"type:epic", "priority:high"}, true},
		{"empty", []string{}, false},
		{"case insensitive", []string{"TYPE:EPIC"}, true},
		{"mixed case", []string{"Type:Epic"}, true},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsEpic(tt.labels); got != tt.want {
				t.Errorf("IsEpic(%v) = %v, want %v", tt.labels, got, tt.want)
			}
		})
	}
}
