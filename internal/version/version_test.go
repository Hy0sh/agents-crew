package version

import "testing"

func TestShortRev(t *testing.T) {
	cases := map[string]string{
		"":                             "",
		"abc123":                       "abc123",
		"0123456789abcdef0123456789ab": "0123456789ab",
	}
	for in, want := range cases {
		if got := shortRev(in); got != want {
			t.Errorf("shortRev(%q) = %q, want %q", in, got, want)
		}
	}
}
