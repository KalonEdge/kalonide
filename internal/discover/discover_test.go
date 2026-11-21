package discover

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestIsLocalAndSameServer(t *testing.T) {
	for u, want := range map[string]bool{
		"http://localhost:11434/v1": true, "http://127.0.0.1:1234/v1": true, "http://[::1]:8080/v1": true,
		"https://openrouter.ai/api/v1": false, "http://10.0.0.5:11434/v1": false,
	} {
		if IsLocal(u) != want {
			t.Errorf("IsLocal(%s) != %v", u, want)
		}
	}
	if !sameServer("http://127.0.0.1:11434/v1", "http://localhost:11434") || sameServer("http://localhost:1/v1", "http://localhost:2/v1") {
		t.Error("sameServer")
	}
}

// serveModels serves a model list on addr.
func serveModels(t *testing.T, ln net.Listener) *http.Server {
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"bench-only-model"}]}`)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return srv
}

func TestReconnectWaitsForTheServerToComeBack(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // the server is "restarting"
	base := "http://" + addr + "/v1"
	go func() {
		time.Sleep(1500 * time.Millisecond)
		ln2, err := net.Listen("tcp", addr)
		if err == nil {
			serveModels(t, ln2)
		}
	}()
	start := time.Now()
	got := Reconnect(context.Background(), base, "model-nobody-serves", 10*time.Second)
	if got != base || time.Since(start) > 8*time.Second {
		t.Fatalf("got %q after %s", got, time.Since(start))
	}
}

func TestReconnectGivesUp(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	start := time.Now()
	if got := Reconnect(context.Background(), "http://"+addr+"/v1", "model-nobody-serves", 1500*time.Millisecond); got != "" {
		t.Fatalf("got %q", got)
	}
	if time.Since(start) > 12*time.Second {
		t.Fatal("waited far too long")
	}
	if Reconnect(context.Background(), "https://example.com/v1", "m", time.Second) != "" {
		t.Fatal("remote servers are never rediscovered")
	}
}
