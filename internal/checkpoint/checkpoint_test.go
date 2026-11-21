package checkpoint

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUndoRestoresEditsAndRemovesNewFiles(t *testing.T) {
	dir := t.TempDir()
	edited := filepath.Join(dir, "a.txt")
	created := filepath.Join(dir, "sub", "new.txt")
	os.WriteFile(edited, []byte("v1"), 0o755)

	var s Store
	s.Begin("first", 0, 0)
	s.Record(edited)
	os.WriteFile(edited, []byte("v2"), 0o644)

	s.Begin("second", 2, 0)
	s.Record(edited)
	s.Record(edited) // recorded once per turn
	os.WriteFile(edited, []byte("v3"), 0o644)
	s.Record(created)
	os.MkdirAll(filepath.Dir(created), 0o755)
	os.WriteFile(created, []byte("x"), 0o644)

	s.Begin("question only", 4, 0) // no files changed

	turn, restored, err := s.Undo()
	if err != nil || turn.Prompt != "second" || len(restored) != 2 {
		t.Fatalf("undo: %v %+v %v", err, turn, restored)
	}
	if b, _ := os.ReadFile(edited); string(b) != "v2" {
		t.Fatalf("edited = %q, want v2", b)
	}
	if _, err := os.Stat(created); !os.IsNotExist(err) {
		t.Fatal("created file not removed")
	}
	if len(s.Turns()) != 1 {
		t.Fatalf("turns left: %d", len(s.Turns()))
	}

	turn, _, err = s.Undo()
	if err != nil || turn.Prompt != "first" {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(edited); string(b) != "v1" {
		t.Fatalf("edited = %q, want v1", b)
	}
	if st, _ := os.Stat(edited); st.Mode().Perm() != 0o755 {
		t.Fatalf("mode not restored: %v", st.Mode())
	}
	if _, _, err := s.Undo(); err == nil {
		t.Fatal("expected nothing to undo")
	}
}

func TestRewindToEarlierTurnRestoresEverythingAfter(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	os.WriteFile(f, []byte("0"), 0o644)
	var s Store
	for i, v := range []string{"1", "2", "3"} {
		s.Begin("turn", i*2, 0)
		s.Record(f)
		os.WriteFile(f, []byte(v), 0o644)
	}
	turn, _, err := s.RewindTo(1)
	if err != nil || turn.MsgIndex != 2 {
		t.Fatal(err, turn)
	}
	if b, _ := os.ReadFile(f); string(b) != "1" {
		t.Fatalf("got %q, want the state before turn 2", b)
	}
	if len(s.Turns()) != 1 {
		t.Fatal("later turns not dropped")
	}
}

func TestRecordWithoutTurnIsNoop(t *testing.T) {
	var s Store
	if err := s.Record(filepath.Join(t.TempDir(), "x")); err != nil {
		t.Fatal(err)
	}
}
