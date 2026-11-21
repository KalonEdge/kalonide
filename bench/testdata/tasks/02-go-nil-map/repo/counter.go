package wc

// Counter counts how often each word was added.
type Counter struct {
	counts map[string]int
}

// New returns an empty Counter.
func New() *Counter { return &Counter{} }

// Add records one occurrence of word.
func (c *Counter) Add(word string) { c.counts[word]++ }

// Count returns how often word was added.
func (c *Counter) Count(word string) int { return c.counts[word] }
