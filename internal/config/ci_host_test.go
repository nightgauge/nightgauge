package config

import "testing"

func TestCIHost(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"no CI", map[string]string{}, false},
		{"CI=true", map[string]string{"CI": "true"}, true},
		{"CI=1", map[string]string{"CI": "1"}, true},
		{"CI=TRUE", map[string]string{"CI": "TRUE"}, true},
		{"CI=false", map[string]string{"CI": "false"}, false},
		// A stage on an operator machine: forced CI=true, host value false (#2514).
		{"stage on operator machine", map[string]string{"CI": "true", "NIGHTGAUGE_HOST_CI": "false"}, false},
		{"stage on a CI runner", map[string]string{"CI": "true", "NIGHTGAUGE_HOST_CI": "true"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CIHost(func(k string) string { return tc.env[k] }); got != tc.want {
				t.Errorf("CIHost(%v) = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
	if CIHost(nil) {
		t.Error("CIHost(nil) = true, want false")
	}
}
