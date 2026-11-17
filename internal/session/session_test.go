package session

import (
	"strings"
	"testing"
	"time"

	"github.com/KalonEdge/kalonide/internal/llm"
)

func TestSaveLoadListForkDelete(t *testing.T) {
	st := &Store{Dir: t.TempDir()}
	a := New("/w", "kalon", "m")
	a.Messages = []llm.Message{{Role: llm.RoleUser, Content: "  fix   the\nlogin bug  "}, {Role: llm.RoleAssistant, Content: "ok"}}
	if err := st.Save(a); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	b := New("/other", "kalon", "m")
	b.Messages = []llm.Message{{Role: llm.RoleUser, Content: strings.Repeat("long ", 40)}}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	got, err := st.Load(a.ID[:20])
	if err != nil || got.Title != "fix the login bug" || len(got.Messages) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	latest, _ := st.Load("")
	if latest.ID != b.ID || !strings.HasSuffix(latest.Title, "…") {
		t.Fatalf("latest %+v", latest)
	}
	list, _ := st.List("/w")
	if len(list) != 1 || list[0].Turns != 1 {
		t.Fatalf("%+v", list)
	}
	f, err := st.Fork(a.ID)
	if err != nil || f.Parent != a.ID || f.ID == a.ID || len(f.Messages) != 2 {
		t.Fatalf("%+v %v", f, err)
	}
	if err := st.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load(a.ID); err == nil {
		t.Fatal("deleted session still loads")
	}
}

func TestEmptyStore(t *testing.T) {
	st := &Store{Dir: t.TempDir() + "/none"}
	if l, err := st.List(""); err != nil || len(l) != 0 {
		t.Fatal(l, err)
	}
	if _, err := st.Load(""); err == nil {
		t.Fatal("expected no sessions error")
	}
}
