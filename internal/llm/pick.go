package llm

import (
	"regexp"
	"strconv"
	"strings"
)

var reParams = regexp.MustCompile(`(?i)(?:^|[^a-z0-9.])(\d+(?:\.\d+)?)b(?:$|[^a-z])`)

// BestModel picks a sensible default for coding from what a server offers:
// tool-calling support first, then coding-tuned models, then size.
// Embedding-only models are skipped. It returns "" for an empty list.
func BestModel(models []ModelInfo) string {
	best, bestScore := "", -1.0
	for _, m := range models {
		id := strings.ToLower(m.ID)
		if strings.Contains(id, "embed") {
			continue
		}
		score := 0.0
		if m.Caps != nil && m.Supports("tools") {
			score += 1000
		}
		if strings.Contains(id, "coder") || strings.Contains(id, "code") {
			score += 500
		}
		if p := ParamsB(m.ID); p > 0 {
			score += min(p, 400)
		}
		if score > bestScore {
			best, bestScore = m.ID, score
		}
	}
	if best == "" && len(models) > 0 {
		best = models[0].ID
	}
	return best
}

// ParamsB reads a parameter count in billions from a model name such as
// "qwen2.5-coder:7b" or "llama3.1:70b-instruct". It returns 0 if absent.
func ParamsB(id string) float64 {
	m := reParams.FindStringSubmatch(id)
	if m == nil {
		return 0
	}
	f, _ := strconv.ParseFloat(m[1], 64)
	return f
}
