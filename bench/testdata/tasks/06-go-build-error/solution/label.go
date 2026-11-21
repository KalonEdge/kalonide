package labels

import "strconv"

// Label returns a display label such as "item-3".
func Label(n int) string {
	return "item-" + strconv.Itoa(n)
}

// Labels returns labels for 1..n.
func Labels(n int) []string {
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, Label(i))
	}
	return out
}
