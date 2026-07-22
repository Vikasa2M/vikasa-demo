package fleetsize

import "testing"

func TestResolve(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	cases := []struct {
		name    string
		vars    map[string]string
		want    Size
		wantErr bool
	}{
		{"size small", map[string]string{"SIZE": "small"}, Small, false},
		{"size medium", map[string]string{"SIZE": "medium"}, Medium, false},
		{"size large", map[string]string{"SIZE": "large"}, Large, false},
		{"scale1 alias", map[string]string{"SCALE": "1"}, Small, false},
		{"default large", map[string]string{}, Large, false},
		{"size wins over scale", map[string]string{"SIZE": "large", "SCALE": "1"}, Large, false},
		{"invalid size", map[string]string{"SIZE": "tiny"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(env(tc.vars))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Resolve(%v) = %q, want error", tc.vars, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve(%v) unexpected error: %v", tc.vars, err)
			}
			if got != tc.want {
				t.Fatalf("Resolve(%v) = %q, want %q", tc.vars, got, tc.want)
			}
		})
	}
}

func TestKeep(t *testing.T) {
	// cabinet kinds: (corridor, route, expose)
	hero := func() (string, string, bool) { return "i85", "i85", false }
	perception := func() (string, string, bool) { return "", "i85", false }
	reversible := func() (string, string, bool) { return "", "i75s", false }
	fault := func() (string, string, bool) { return "", "", true }
	arterial := func() (string, string, bool) { return "", "", false }

	cases := []struct {
		size Size
		kind func() (string, string, bool)
		name string
		want bool
	}{
		{Small, hero, "small-hero", true},
		{Small, perception, "small-perception", false},
		{Small, fault, "small-fault", false},
		{Small, arterial, "small-arterial", false},
		{Medium, hero, "medium-hero", true},
		{Medium, perception, "medium-perception", true},
		{Medium, reversible, "medium-reversible", true},
		{Medium, fault, "medium-fault", true},
		{Medium, arterial, "medium-arterial", false},
		{Large, arterial, "large-arterial", true},
		{Large, hero, "large-hero", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, r, e := tc.kind()
			if got := Keep(tc.size, c, r, e); got != tc.want {
				t.Fatalf("Keep(%q, %q, %q, %v) = %v, want %v", tc.size, c, r, e, got, tc.want)
			}
		})
	}
}
