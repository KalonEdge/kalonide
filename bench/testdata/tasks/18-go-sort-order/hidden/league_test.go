package league

import (
	"reflect"
	"testing"
)

func TestLeaderboard(t *testing.T) {
	in := []Player{{"cy", 5}, {"al", 9}, {"bo", 5}, {"di", 1}}
	got := Leaderboard(in)
	want := []Player{{"al", 9}, {"bo", 5}, {"cy", 5}, {"di", 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if in[0].Name != "cy" {
		t.Fatal("input was modified")
	}
}
