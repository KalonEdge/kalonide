package text

import "unicode"

// IsPalindrome reports whether s reads the same forwards and backwards,
// ignoring case and every character that is not a letter or digit.
// The empty string is a palindrome.
func IsPalindrome(s string) bool {
	var r []rune
	for _, c := range s {
		if unicode.IsLetter(c) || unicode.IsDigit(c) {
			r = append(r, unicode.ToLower(c))
		}
	}
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		if r[i] != r[j] {
			return false
		}
	}
	return true
}
