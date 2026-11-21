package users

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEncodeMatchesSpec(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	b, err := Encode(User{ID: 7, FullName: "Ada", CreatedAt: ts, PasswordHash: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	if len(m) != 3 || m["user_id"] != 7.0 || m["full_name"] != "Ada" || m["created_at"] != "2026-01-02T03:04:05Z" {
		t.Fatalf("got %s", b)
	}
	b, _ = Encode(User{ID: 1, Email: "a@b.c"})
	json.Unmarshal(b, &m)
	if m["email"] != "a@b.c" {
		t.Fatalf("email missing: %s", b)
	}
}
