package users

import (
	"encoding/json"
	"time"
)

// User is an account.
type User struct {
	ID           int       `json:"userId"`
	FullName     string    `json:"fullName"`
	Email        string    `json:"email"`
	CreatedAt    time.Time `json:"createdAt"`
	PasswordHash string
}

// Encode returns the user as JSON.
func Encode(u User) ([]byte, error) { return json.Marshal(u) }
