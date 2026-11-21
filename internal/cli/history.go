package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/KalonEdge/kalonide/internal/agent"
	"github.com/KalonEdge/kalonide/internal/llm"
	"github.com/KalonEdge/kalonide/internal/session"
	"github.com/KalonEdge/kalonide/internal/ui"
)

// Hit is one search match in a saved session.
type Hit struct {
	Session string    `json:"session"`
	Title   string    `json:"title"`
	Updated time.Time `json:"updated"`
	Role    string    `json:"role"`
	Snippet string    `json:"snippet"`
}

// searchSessions finds messages containing every word of query
// (case-insensitive), newest sessions first.
func searchSessions(store *session.Store, query, workspace string, limit int) ([]Hit, error) {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil, fmt.Errorf("give something to search for")
	}
	list, err := store.List(workspace)
	if err != nil {
		return nil, err
	}
	var hits []Hit
	for _, sum := range list {
		s, err := store.Load(sum.ID)
		if err != nil {
			continue
		}
		for _, m := range s.Messages {
			text := m.Content
			for _, c := range m.ToolCalls {
				text += "\n" + c.Function.Name + " " + c.Function.Arguments
			}
			low := strings.ToLower(text)
			all := true
			for _, w := range words {
				if !strings.Contains(low, w) {
					all = false
					break
				}
			}
			if !all {
				continue
			}
			hits = append(hits, Hit{Session: s.ID, Title: s.Title, Updated: s.Updated, Role: m.Role, Snippet: snippet(text, low, words[0], 70)})
			if len(hits) >= limit {
				return hits, nil
			}
		}
	}
	return hits, nil
}

// snippet returns the text around the first match, on one line.
func snippet(text, low, word string, radius int) string {
	i := strings.Index(low, word)
	if i < 0 {
		i = 0
	}
	start, end := max(0, i-radius), min(len(text), i+len(word)+radius)
	for start > 0 && !isRuneStart(text[start]) {
		start--
	}
	for end < len(text) && !isRuneStart(text[end]) {
		end++
	}
	s := strings.Join(strings.Fields(text[start:end]), " ")
	if start > 0 {
		s = "…" + s
	}
	if end < len(text) {
		s += "…"
	}
	return s
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

func cmdSearch(args []string) int {
	var all, asJSON, help bool
	var limit int
	f := newFlags("search")
	f.Bool(&all, "Search sessions from every folder", "all", "a")
	f.Int(&limit, "<n>", "Most matches to show (default 30)", "limit")
	f.Bool(&asJSON, "JSON output", "json")
	f.Bool(&help, "Show this help", "h", "help")
	if err := f.Parse(args); err != nil {
		return usageFail(err)
	}
	if help || len(f.args) == 0 {
		fmt.Print("Search past sessions.\n\nUsage:\n  kalonide search <words> [--all] [--json]\n\n" + f.Usage())
		if help {
			return exitOK
		}
		return exitUsage
	}
	if limit <= 0 {
		limit = 30
	}
	ws := ""
	if !all {
		wd, _ := os.Getwd()
		ws, _ = realDir(wd)
	}
	hits, err := searchSessions(newStore(), strings.Join(f.args, " "), ws, limit)
	if err != nil {
		return usageFail(err)
	}
	if asJSON {
		return printJSON(hits)
	}
	if len(hits) == 0 {
		fmt.Println("No matches" + map[bool]string{true: ".", false: " in this folder's sessions (try --all)."}[all])
		return exitOK
	}
	printHits(hits)
	return exitOK
}

func printHits(hits []Hit) {
	last := ""
	for _, h := range hits {
		if h.Session != last {
			fmt.Printf("%s %s %s\n", ui.Bold(ui.Sanitize(h.Title)), ui.Dim(h.Session), ui.Dim(ago(h.Updated)))
			last = h.Session
		}
		fmt.Printf("  %s %s\n", ui.Dim(fmt.Sprintf("%-9s", h.Role)), ui.Sanitize(h.Snippet))
	}
	fmt.Println(ui.Dim("\nContinue one with: kalonide resume <id>"))
}

// ---- usage ----

type usageRow struct {
	Day      string `json:"day"`
	Model    string `json:"model"`
	Sessions int    `json:"sessions"`
	Requests int    `json:"requests"`
	Tokens   int    `json:"tokens"`
	Exact    bool   `json:"exact"` // false when estimated (the server reported no usage)
}

func cmdUsage(args []string) int {
	var days int
	var asJSON, help bool
	f := newFlags("usage")
	f.Int(&days, "<n>", "Only the last n days (default 30)", "days")
	f.Bool(&asJSON, "JSON output", "json")
	f.Bool(&help, "Show this help", "h", "help")
	if err := f.Parse(args); err != nil {
		return usageFail(err)
	}
	if help {
		fmt.Print("Sessions, requests and tokens per day and model, from saved sessions.\nTokens are estimated (~) when the server reports none, as Kalon does.\n\nUsage:\n  kalonide usage [--days n] [--json]\n\n" + f.Usage())
		return exitOK
	}
	if days <= 0 {
		days = 30
	}
	store := newStore()
	list, err := store.List("")
	if err != nil {
		return fail(err)
	}
	since := time.Now().AddDate(0, 0, -days)
	rows := map[string]*usageRow{}
	for _, sum := range list {
		if sum.Updated.Before(since) {
			continue
		}
		s, err := store.Load(sum.ID)
		if err != nil {
			continue
		}
		day := s.Updated.Format("2006-01-02")
		key := day + "\x00" + s.Model
		r := rows[key]
		if r == nil {
			r = &usageRow{Day: day, Model: s.Model, Exact: true}
			rows[key] = r
		}
		r.Sessions++
		r.Requests += countRequests(s.Messages)
		if s.Usage.TotalTokens > 0 {
			r.Tokens += s.Usage.TotalTokens
		} else {
			r.Tokens += agent.EstimateTokens(s.Messages)
			r.Exact = false
		}
	}
	out := make([]usageRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Day != out[j].Day {
			return out[i].Day > out[j].Day
		}
		return out[i].Model < out[j].Model
	})
	if asJSON {
		return printJSON(out)
	}
	if len(out) == 0 {
		fmt.Printf("No sessions in the last %d days.\n", days)
		return exitOK
	}
	fmt.Printf("%-10s  %-28s %8s %8s %10s\n", "day", "model", "sessions", "requests", "tokens")
	var tot usageRow
	for _, r := range out {
		tok := compactNum(r.Tokens)
		if !r.Exact {
			tok = "~" + tok
		}
		fmt.Printf("%-10s  %-28s %8d %8d %10s\n", r.Day, clipText(r.Model, 28), r.Sessions, r.Requests, tok)
		tot.Sessions += r.Sessions
		tot.Requests += r.Requests
		tot.Tokens += r.Tokens
	}
	fmt.Printf("%-10s  %-28s %8d %8d %10s\n", ui.Bold("total"), "", tot.Sessions, tot.Requests, "~"+compactNum(tot.Tokens))
	return exitOK
}

// countRequests counts the user's own messages (not nudges or summaries).
func countRequests(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == llm.RoleUser && !strings.HasPrefix(m.Content, "[Summary of the earlier conversation]") {
			n++
		}
	}
	return n
}
