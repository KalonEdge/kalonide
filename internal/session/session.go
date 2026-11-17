// Package session stores conversations on disk so they can be listed,
// resumed and forked. Nothing leaves the machine.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/KalonEdge/kalonide/internal/config"
	"github.com/KalonEdge/kalonide/internal/llm"
)

// Session is one saved conversation. The system prompt is not stored; it
// is rebuilt when the session is resumed.
type Session struct {
	ID        string        `json:"id"`
	Title     string        `json:"title"`
	Workspace string        `json:"workspace"`
	Provider  string        `json:"provider"`
	Model     string        `json:"model"`
	Parent    string        `json:"parent,omitempty"`
	Created   time.Time     `json:"created"`
	Updated   time.Time     `json:"updated"`
	Usage     llm.Usage     `json:"usage"`
	Messages  []llm.Message `json:"messages"`
}

// Store reads and writes sessions in a directory.
type Store struct{ Dir string }

// NewID returns a sortable, unique session id.
func NewID() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

// New starts an unsaved session.
func New(workspace, provider, model string) *Session {
	now := time.Now()
	return &Session{ID: NewID(), Workspace: workspace, Provider: provider, Model: model, Created: now, Updated: now}
}

func (s *Store) path(id string) string { return filepath.Join(s.Dir, id+".json") }

// Save writes the session atomically.
func (s *Store) Save(sess *Session) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	sess.Updated = time.Now()
	if sess.Title == "" {
		sess.Title = TitleFrom(sess.Messages)
	}
	data, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(s.path(sess.ID), data, 0o600)
}

// Load reads one session. id may be a unique prefix, or "" / "latest" for
// the most recent session.
func (s *Store) Load(id string) (*Session, error) {
	if id == "" || id == "latest" {
		list, err := s.List("")
		if err != nil {
			return nil, err
		}
		if len(list) == 0 {
			return nil, errors.New("no saved sessions yet")
		}
		id = list[0].ID
	}
	full, err := s.resolve(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.path(full))
	if err != nil {
		return nil, err
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("session %s is damaged: %w", full, err)
	}
	return &sess, nil
}

func (s *Store) resolve(prefix string) (string, error) {
	if _, err := os.Stat(s.path(prefix)); err == nil {
		return prefix, nil
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return "", fmt.Errorf("no session %q", prefix)
	}
	var found []string
	for _, e := range entries {
		id := strings.TrimSuffix(e.Name(), ".json")
		if strings.HasSuffix(e.Name(), ".json") && (strings.HasPrefix(id, prefix) || strings.HasSuffix(id, prefix)) {
			found = append(found, id)
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("no session %q", prefix)
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("%q matches %d sessions; use more of the id", prefix, len(found))
}

// Summary is a session listing entry.
type Summary struct {
	ID        string
	Title     string
	Workspace string
	Model     string
	Updated   time.Time
	Turns     int
}

// List returns sessions, newest first. A non-empty workspace limits the
// list to sessions from that folder.
func (s *Store) List(workspace string) ([]Summary, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Summary
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.Dir, e.Name()))
		if err != nil {
			continue
		}
		var sess Session
		if json.Unmarshal(data, &sess) != nil {
			continue
		}
		if workspace != "" && sess.Workspace != workspace {
			continue
		}
		turns := 0
		for _, m := range sess.Messages {
			if m.Role == llm.RoleUser {
				turns++
			}
		}
		out = append(out, Summary{ID: sess.ID, Title: sess.Title, Workspace: sess.Workspace, Model: sess.Model, Updated: sess.Updated, Turns: turns})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

// Delete removes a session.
func (s *Store) Delete(id string) error {
	full, err := s.resolve(id)
	if err != nil {
		return err
	}
	return os.Remove(s.path(full))
}

// Fork copies a session under a new id.
func (s *Store) Fork(id string) (*Session, error) {
	src, err := s.Load(id)
	if err != nil {
		return nil, err
	}
	cp := *src
	cp.Messages = append([]llm.Message(nil), src.Messages...)
	cp.Parent = src.ID
	cp.ID = NewID()
	cp.Created = time.Now()
	cp.Title = "fork of " + src.Title
	return &cp, nil
}

// TitleFrom derives a title from the first user message.
func TitleFrom(msgs []llm.Message) string {
	for _, m := range msgs {
		if m.Role == llm.RoleUser {
			t := strings.Join(strings.Fields(m.Content), " ")
			if r := []rune(t); len(r) > 60 {
				t = string(r[:57]) + "…"
			}
			return t
		}
	}
	return "untitled"
}
