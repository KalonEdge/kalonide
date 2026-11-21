// Package checkpoint records files before the agent changes them, so a
// turn's edits can be undone. It works without git and never touches git
// history.
package checkpoint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// maxFileSize is the largest file kept for undo; bigger files are changed
// without a copy and reported.
const maxFileSize = 8 << 20

type snapshot struct {
	existed bool
	data    []byte
	mode    os.FileMode
}

// Turn is one user request and the files the agent changed while
// answering it.
type Turn struct {
	Prompt   string
	MsgIndex int // conversation length before the request was added
	Epoch    int // the conversation's compaction count when the turn began
	files    map[string]*snapshot
	order    []string
	skipped  []string // too large to keep
}

// Files returns the changed paths in the order they were first touched.
func (t *Turn) Files() []string { return append([]string(nil), t.order...) }

// Store holds the checkpoints of one session.
type Store struct {
	mu    sync.Mutex
	turns []*Turn
}

// Begin starts a new turn. msgIndex is the conversation length before the
// user's message is added, so a rewind can cut the conversation there;
// epoch tells whether the conversation was summarized since.
func (s *Store) Begin(prompt string, msgIndex, epoch int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turns = append(s.turns, &Turn{Prompt: prompt, MsgIndex: msgIndex, Epoch: epoch, files: map[string]*snapshot{}})
}

// Record saves path as it is now, once per turn, before it is changed.
// Without a current turn it does nothing.
func (s *Store) Record(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.turns) == 0 {
		return nil
	}
	t := s.turns[len(s.turns)-1]
	if _, done := t.files[path]; done {
		return nil
	}
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		t.files[path] = &snapshot{existed: false}
	case err != nil:
		return err
	case info.Size() > maxFileSize:
		t.skipped = append(t.skipped, path)
		t.files[path] = nil
		return nil
	default:
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		t.files[path] = &snapshot{existed: true, data: data, mode: info.Mode().Perm()}
	}
	t.order = append(t.order, path)
	return nil
}

// Turns returns the recorded turns, oldest first.
func (s *Store) Turns() []*Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Turn(nil), s.turns...)
}

// Undo restores the files of the most recent turn that changed any, and
// removes that turn and every later one. It returns the turn undone.
func (s *Store) Undo() (*Turn, []string, error) {
	s.mu.Lock()
	last := -1
	for i := len(s.turns) - 1; i >= 0; i-- {
		if len(s.turns[i].order) > 0 {
			last = i
			break
		}
	}
	s.mu.Unlock()
	if last < 0 {
		return nil, nil, errors.New("nothing to undo: no files were changed by the agent in this session")
	}
	return s.RewindTo(last)
}

// RewindTo restores every file changed in turn i and later, newest turn
// first, drops those turns, and returns turn i with the restored paths.
func (s *Store) RewindTo(i int) (*Turn, []string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < 0 || i >= len(s.turns) {
		return nil, nil, fmt.Errorf("no turn %d", i+1)
	}
	var restored []string
	var errs []error
	seen := map[string]bool{}
	for k := len(s.turns) - 1; k >= i; k-- {
		t := s.turns[k]
		for j := len(t.order) - 1; j >= 0; j-- {
			p := t.order[j]
			snap := t.files[p]
			if snap == nil {
				continue
			}
			if err := restore(p, snap); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", p, err))
				continue
			}
			if !seen[p] {
				seen[p] = true
				restored = append(restored, p)
			}
		}
	}
	target := s.turns[i]
	s.turns = s.turns[:i]
	return target, restored, errors.Join(errs...)
}

// Skipped lists files in turn t that were too large to keep.
func (t *Turn) Skipped() []string { return append([]string(nil), t.skipped...) }

func restore(path string, snap *snapshot) error {
	if !snap.existed {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".undo-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(snap.data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, snap.mode); err != nil {
		return err
	}
	return os.Rename(name, path)
}
