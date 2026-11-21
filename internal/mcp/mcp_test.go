package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as a fake stdio MCP server.
func TestMain(m *testing.M) {
	if os.Getenv("KALONIDE_FAKE_MCP") == "1" {
		fakeServer(os.Stdin, os.Stdout)
		os.Exit(0)
	}
	if os.Getenv("KALONIDE_FAKE_MCP") == "crash" {
		fmt.Fprintln(os.Stderr, "fatal: missing API token")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

type msg struct {
	ID     *int64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
}

// fakeServer answers MCP requests line by line.
func fakeServer(in io.Reader, out io.Writer) {
	enc := json.NewEncoder(out)
	sc := bufio.NewScanner(in)
	reply := func(id *int64, result any) {
		enc.Encode(map[string]any{"jsonrpc": "2.0", "id": *id, "result": result})
	}
	fmt.Fprintln(out, "a stray log line that is not JSON")
	for sc.Scan() {
		var m msg
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.ID == nil {
			continue // notifications, and our ping answer
		}
		switch m.Method {
		case "initialize":
			// Ping the client before answering, as real servers may.
			enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 999, "method": "ping"})
			reply(m.ID, map[string]any{"protocolVersion": ProtocolVersion, "serverInfo": map[string]string{"name": "fake"}, "capabilities": map[string]any{}})
		case "tools/list":
			var p struct{ Cursor string }
			json.Unmarshal(m.Params, &p)
			if p.Cursor == "" {
				reply(m.ID, map[string]any{"tools": []map[string]any{{"name": "echo", "description": "Echo text", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": true}}}, "nextCursor": "page2"})
			} else {
				reply(m.ID, map[string]any{"tools": []map[string]any{{"name": "fail", "description": "Always fails", "inputSchema": map[string]any{"type": "object"}}}})
			}
		case "tools/call":
			var p struct {
				Name      string
				Arguments map[string]any
			}
			json.Unmarshal(m.Params, &p)
			if p.Name == "fail" {
				reply(m.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": "it broke"}}, "isError": true})
			} else {
				reply(m.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": fmt.Sprint("echo: ", p.Arguments["text"])}, {"type": "image", "mimeType": "image/png", "data": "AA=="}}})
			}
		default:
			enc.Encode(map[string]any{"jsonrpc": "2.0", "id": *m.ID, "error": map[string]any{"code": -32601, "message": "unknown"}})
		}
	}
}

func selfConfig(mode string) ServerConfig {
	return ServerConfig{Command: os.Args[0], Env: map[string]string{"KALONIDE_FAKE_MCP": mode}}
}

func TestStdioServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Connect(ctx, "fake", selfConfig("1"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.ServerName != "fake" {
		t.Fatalf("server name %q", c.ServerName)
	}
	tools, err := c.Tools(ctx)
	if err != nil || len(tools) != 2 || tools[0].Name != "echo" || !tools[0].Annotations.ReadOnlyHint || tools[1].Name != "fail" {
		t.Fatalf("tools %+v %v", tools, err)
	}
	out, isErr, err := c.Call(ctx, "echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil || isErr || !strings.Contains(out, "echo: hi") || !strings.Contains(out, "[image omitted") {
		t.Fatalf("%q %v %v", out, isErr, err)
	}
	out, isErr, err = c.Call(ctx, "fail", nil)
	if err != nil || !isErr || out != "it broke" {
		t.Fatalf("%q %v %v", out, isErr, err)
	}
}

func TestStdioServerThatCrashes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := Connect(ctx, "bad", selfConfig("crash"))
	if err == nil || !strings.Contains(err.Error(), "missing API token") {
		t.Fatalf("want the server's own error message, got %v", err)
	}
	if _, err := Connect(ctx, "none", ServerConfig{Command: "/no/such/server"}); err == nil {
		t.Fatal("missing command accepted")
	}
}

func TestHTTPServer(t *testing.T) {
	var sawSession bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			return
		}
		var m msg
		json.NewDecoder(r.Body).Decode(&m)
		if r.Header.Get("Mcp-Session-Id") == "s-1" {
			sawSession = true
		}
		if m.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Mcp-Session-Id", "s-1")
		switch m.Method {
		case "initialize":
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": *m.ID, "result": map[string]any{"serverInfo": map[string]string{"name": "remote"}}})
		case "tools/list":
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": *m.ID, "result": map[string]any{"tools": []map[string]any{{"name": "search", "inputSchema": map[string]any{"type": "object"}}}}})
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
			fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"found it\"}]}}\n\n", *m.ID)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	c, err := Connect(ctx, "remote", ServerConfig{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	tools, err := c.Tools(ctx)
	if err != nil || len(tools) != 1 {
		t.Fatal(tools, err)
	}
	out, _, err := c.Call(ctx, "search", json.RawMessage(`{}`))
	if err != nil || out != "found it" || !sawSession {
		t.Fatalf("%q %v session=%v", out, err, sawSession)
	}
}
