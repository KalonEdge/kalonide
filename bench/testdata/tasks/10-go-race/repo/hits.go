package hits

import "sync"

// Count runs n workers that each record one hit, and returns the total.
func Count(n int) int {
	total := 0
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			total++
		}()
	}
	wg.Wait()
	return total
}
