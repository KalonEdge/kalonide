package llm

import (
	"regexp"
	"strings"
)

// Some servers leak a model's chat-template control tokens into the reply
// text, for example Gemma's "<channel|>" or "<|im_end|>". They are never
// meant to be shown. <tool_call> is deliberately not matched: models write
// it on purpose and the agent parses it.
var reMarker = regexp.MustCompile(`<\|[a-z_]+\|?>|<[a-z_]+\|>|</?(?:end_of_turn|start_of_turn)>`)

// stripMarkers removes leaked control tokens and reports whether one was
// found.
func stripMarkers(s string) (string, bool) {
	if !strings.Contains(s, "<") || !reMarker.MatchString(s) {
		return s, false
	}
	return reMarker.ReplaceAllString(s, ""), true
}

// noteMarkers strips control tokens from a content chunk and tracks
// whether the reply currently ends at one.
func (a *assembler) noteMarkers(chunk string) string {
	clean, found := stripMarkers(chunk)
	switch {
	case found:
		// The token may sit mid-chunk; only text after it counts.
		after := chunk[strings.LastIndex(chunk, ">")+1:]
		a.markerAtEnd = strings.TrimSpace(after) == ""
	case strings.TrimSpace(clean) != "":
		a.markerAtEnd = false
	}
	return clean
}
