package text

import "testing"

func TestIsPalindrome(t *testing.T) {
	for s, want := range map[string]bool{
		"": true, "a": true, "Racecar": true, "A man, a plan, a canal: Panama!": true,
		"hello": false, "ab": false, "No 'x' in Nixon": true, "12321": true, "123": false,
	} {
		if got := IsPalindrome(s); got != want {
			t.Errorf("IsPalindrome(%q) = %v, want %v", s, got, want)
		}
	}
}
