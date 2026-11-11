package securepipe

import "testing"

func TestValidName(t *testing.T) {
	for _, name := range []string{"fivemesh-abc_123", "A.B"} {
		if !ValidName(name) {
			t.Fatalf("valid pipe name rejected: %q", name)
		}
	}
	for _, name := range []string{"", "../pipe", `pipe\child`, "contains space", string(make([]byte, 129))} {
		if ValidName(name) {
			t.Fatalf("invalid pipe name accepted: %q", name)
		}
	}
}
