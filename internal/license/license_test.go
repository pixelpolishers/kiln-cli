package license

import "testing"

func TestCheck(t *testing.T) {
	deny := Policy{Deny: []string{"GPL-3.0", "AGPL-*"}}
	allow := Policy{Allow: []string{"MIT", "Apache-2.0", "BSD-*"}}
	tests := []struct {
		name   string
		policy Policy
		expr   string
		want   Verdict
	}{
		{"plain ok", deny, "MIT", OK},
		{"denied", deny, "GPL-3.0-only", Denied},
		{"or-later suffix", deny, "GPL-3.0-or-later", Denied},
		{"plus suffix", deny, "GPL-3.0+", Denied},
		{"case", deny, "gpl-3.0", Denied},
		{"prefix", deny, "AGPL-3.0-only", Denied},
		{"other gpl version is not denied", deny, "GPL-2.0-only", OK},
		{"or picks the allowed side", deny, "GPL-3.0-only OR MIT", OK},
		{"and needs both", deny, "MIT AND GPL-3.0-only", Denied},
		{"parentheses", deny, "(MIT OR GPL-3.0) AND Zlib", OK},
		{"parentheses fail", deny, "(GPL-3.0 OR AGPL-3.0) AND MIT", Denied},
		{"with exception uses the license", deny, "GPL-2.0-only WITH Classpath-exception-2.0", OK},
		{"allow list ok", allow, "BSD-3-Clause", OK},
		{"allow list refuses", allow, "Zlib", NotAllowed},
		{"allow or", allow, "Zlib OR MIT", OK},
		{"allow and", allow, "MIT AND Zlib", NotAllowed},
		{"empty", deny, "", Unknown},
		{"garbage", deny, "MIT OR", Unknown},
		{"bad char", deny, "MIT; rm", Unknown},
		{"unbalanced", deny, "(MIT", Unknown},
		{"no policy", Policy{}, "Whatever-1.0", OK},
	}
	for _, tc := range tests {
		if got := tc.policy.Check(tc.expr); got != tc.want {
			t.Errorf("%s: Check(%q) = %q, want %q", tc.name, tc.expr, got, tc.want)
		}
	}
}
