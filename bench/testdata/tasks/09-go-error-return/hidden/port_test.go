package netcfg

import "testing"

func TestParsePort(t *testing.T) {
	if p, err := ParsePort("8080"); err != nil || p != 8080 {
		t.Fatalf("ParsePort(8080) = %d, %v", p, err)
	}
	for _, bad := range []string{"", "abc", "0", "65536", "-1", "80x"} {
		if _, err := ParsePort(bad); err == nil {
			t.Errorf("ParsePort(%q) gave no error", bad)
		}
	}
}
