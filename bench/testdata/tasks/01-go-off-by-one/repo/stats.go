package stats

// Average returns the mean of xs, or 0 for an empty slice.
func Average(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sum := 0.0
	for i := 1; i < len(xs); i++ {
		sum += xs[i]
	}
	return sum / float64(len(xs))
}
