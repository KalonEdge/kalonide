package league

import "sort"

// Player has a name and a score.
type Player struct {
	Name  string
	Score int
}

// Leaderboard returns players sorted by score, highest first. Players with
// equal scores are ordered by name, A to Z. The input is not modified.
func Leaderboard(ps []Player) []Player {
	out := append([]Player(nil), ps...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Name < out[j].Name
	})
	return out
}
