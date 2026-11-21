package shop

// Total returns the sum of price times quantity.
func Total(items []Item) float64 {
	sum := 0.0
	for _, it := range items {
		sum += it.Price * float64(it.Qty)
	}
	return sum
}
