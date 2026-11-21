package users

import (
	"encoding/json"
	"time"
)

// User is an account.
type User struct {
	ID           int       `json:"user_id"`
	FullName     string    `json:"full_name"`
	Email        string    `json:"email,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	PasswordHash string    `json:"-"`
}

// Encode returns the user as JSON.
func Encode(u User) ([]byte, error) { return json.Marshal(u) }
