package management

import "testing"

func TestNormalizeRoutingStrategy_StickyQuotaProtectAliases(t *testing.T) {
	t.Parallel()

	tests := []string{
		"sticky-quota-protect",
		"sticky_quota_protect",
		"stickyquotaprotect",
		"sqp",
	}
	for _, input := range tests {
		got, ok := normalizeRoutingStrategy(input)
		if !ok {
			t.Fatalf("normalizeRoutingStrategy(%q) ok = false", input)
		}
		if got != "sticky-quota-protect" {
			t.Fatalf("normalizeRoutingStrategy(%q) = %q, want sticky-quota-protect", input, got)
		}
	}
}

func TestNormalizeRoutingStrategy_ExistingStrategiesUnchanged(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"":           "round-robin",
		"rr":         "round-robin",
		"roundrobin": "round-robin",
		"fillfirst":  "fill-first",
		"fill-first": "fill-first",
		"ff":         "fill-first",
	}
	for input, want := range tests {
		got, ok := normalizeRoutingStrategy(input)
		if !ok {
			t.Fatalf("normalizeRoutingStrategy(%q) ok = false", input)
		}
		if got != want {
			t.Fatalf("normalizeRoutingStrategy(%q) = %q, want %q", input, got, want)
		}
	}
}
