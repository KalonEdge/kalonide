// Package discover finds model servers on this machine, so Kalonide can
// follow Kalon when it restarts or moves to another port.
package discover

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KalonEdge/kalonide/internal/llm"
)

// Ports are where local model servers usually listen: Kalon and Ollama
// (11434), a second Ollama (11435), LM Studio (1234), llama.cpp and
// friends (8080).
var Ports = []int{11434, 11435, 1234, 8080}

// Server is a model server that answered.
type Server struct {
	URL    string // base URL, e.g. http://localhost:11434/v1
	Models []string
}

// Serves reports whether the server offers model.
func (s Server) Serves(model string) bool {
	for _, m := range s.Models {
		if m == model {
			return true
		}
	}
	return false
}

// IsLocal reports whether a base URL points at this machine.
func IsLocal(base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

// Probe asks one base URL for its models, with a short timeout.
func Probe(ctx context.Context, base string) (Server, bool) {
	c := llm.New(base, "", nil)
	c.HTTP = &http.Client{Timeout: 2 * time.Second}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	models, err := c.ListModels(ctx)
	if err != nil {
		return Server{}, false
	}
	s := Server{URL: base}
	for _, m := range models {
		s.Models = append(s.Models, m.ID)
	}
	return s, true
}

// Scan probes the usual local ports at once and returns those that answer.
func Scan(ctx context.Context) []Server {
	var mu sync.Mutex
	var out []Server
	var wg sync.WaitGroup
	for _, port := range Ports {
		wg.Add(1)
		go func(port int) {
			defer wg.Done()
			if s, ok := Probe(ctx, fmt.Sprintf("http://localhost:%d/v1", port)); ok {
				mu.Lock()
				out = append(out, s)
				mu.Unlock()
			}
		}(port)
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out
}

// Reconnect waits up to wait for a local model server to answer again:
// the same address (Kalon restarting) or another local port that serves
// model (Kalon moved). It returns the base URL to use, or "".
func Reconnect(ctx context.Context, current, model string, wait time.Duration) string {
	if !IsLocal(current) {
		return ""
	}
	deadline := time.Now().Add(wait)
	for {
		if _, ok := Probe(ctx, current); ok {
			return current
		}
		for _, s := range Scan(ctx) {
			if sameServer(s.URL, current) {
				continue
			}
			if model == "" || s.Serves(model) {
				return s.URL
			}
		}
		if time.Now().After(deadline) {
			return ""
		}
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(time.Second):
		}
	}
}

func sameServer(a, b string) bool {
	norm := func(s string) string {
		s = strings.TrimRight(s, "/")
		s = strings.TrimSuffix(s, "/v1")
		return strings.Replace(s, "127.0.0.1", "localhost", 1)
	}
	return norm(a) == norm(b)
}
