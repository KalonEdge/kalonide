package shop

import "fmt"

// Receipt formats an order with its total.
func Receipt(items []Item) string {
	s := ""
	for _, it := range items {
		s += fmt.Sprintf("%s x%d\n", it.Name, it.Qty)
	}
	return s + fmt.Sprintf("total: %.2f\n", Total(items))
}

func discounted(items []Item, pct float64) float64 {
	return Total(items) * (1 - pct/100)
}
