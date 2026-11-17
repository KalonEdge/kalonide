package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func sse(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", c)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func noSleep(c *Client) *Client {
	c.sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

func TestStreamAssemblesTextAndFragmentedToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		if !req.Stream || req.Model != "m" {
			t.Errorf("bad request: %+v", req)
		}
		sse(w,
			`{"choices":[{"delta":{"content":"Hel"}}]}`,
			`{"choices":[{"delta":{"content":"lo"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"read_file","arguments":"{\"pa"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a.go\"}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c2","function":{"name":"grep","arguments":"{}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		)
	}))
	defer srv.Close()
	var streamed strings.Builder
	resp, err := New(srv.URL, "", nil).Chat(context.Background(), Request{Model: "m"}, func(d Delta) { streamed.WriteString(d.Content) })
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "Hello" || streamed.String() != "Hello" {
		t.Fatalf("content %q streamed %q", resp.Content, streamed.String())
	}
	if len(resp.ToolCalls) != 2 || resp.ToolCalls[0].Function.Arguments != `{"path":"a.go"}` || resp.ToolCalls[1].Function.Name != "grep" {
		t.Fatalf("tool calls: %+v", resp.ToolCalls)
	}
	if resp.FinishReason != "tool_calls" || resp.Usage.TotalTokens != 15 {
		t.Fatalf("finish %q usage %+v", resp.FinishReason, resp.Usage)
	}
}

func TestObjectArgumentsAndMissingIDs(t *testing.T) {
	// Ollama-style: arguments as an object, no index, no id.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"choices":[{"delta":{"tool_calls":[{"function":{"name":"bash","arguments":{"command":"ls"}}}]}}]}`)
	}))
	defer srv.Close()
	resp, err := New(srv.URL, "", nil).Chat(context.Background(), Request{Model: "m"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Function.Arguments != `{"command":"ls"}` || resp.ToolCalls[0].ID == "" {
		t.Fatalf("%+v", resp.ToolCalls)
	}
}

func TestThinkTagsAndReasoningFieldAreSeparated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`{"choices":[{"delta":{"reasoning":"plan "}}]}`,
			`{"choices":[{"delta":{"content":"<thi"}}]}`,
			`{"choices":[{"delta":{"content":"nk>hmm</th"}}]}`,
			`{"choices":[{"delta":{"content":"ink>\nAnswer <b>x</b>"}}]}`,
		)
	}))
	defer srv.Close()
	resp, err := New(srv.URL, "", nil).Chat(context.Background(), Request{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "Answer <b>x</b>" || resp.Reasoning != "plan hmm" {
		t.Fatalf("content %q reasoning %q", resp.Content, resp.Reasoning)
	}
}

func TestRetriesOverloadThenSucceeds(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) <= 2 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":{"message":"busy"}}`)
			return
		}
		sse(w, `{"choices":[{"delta":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	c := noSleep(New(srv.URL, "", nil))
	var waits []time.Duration
	c.OnRetry = func(_ int, d time.Duration, _ error) { waits = append(waits, d) }
	resp, err := c.Chat(context.Background(), Request{}, nil)
	if err != nil || resp.Content != "ok" {
		t.Fatalf("resp %+v err %v", resp, err)
	}
	if len(waits) != 2 || waits[0] != time.Second {
		t.Fatalf("waits %v (Retry-After should be honoured)", waits)
	}
}

func TestNoRetryOnAuthOrAfterStreaming(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":"bad key"}`)
	}))
	defer srv.Close()
	_, err := noSleep(New(srv.URL, "", nil)).Chat(context.Background(), Request{}, nil)
	if KindOf(err) != KindAuth || n.Load() != 1 || !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("err %v kind %v calls %d", err, KindOf(err), n.Load())
	}

	var m atomic.Int32
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"part\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"error\":{\"message\":\"model crashed\"}}\n\n")
	}))
	defer srv2.Close()
	resp, err := noSleep(New(srv2.URL, "", nil)).Chat(context.Background(), Request{}, nil)
	if err == nil || m.Load() != 1 || resp == nil || resp.Content != "part" {
		t.Fatalf("resp %+v err %v calls %d", resp, err, m.Load())
	}
}

func TestClassifiesToolsRejectedAndContextTooLong(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   Kind
	}{
		{400, `{"error":{"message":"registry.ollama.ai/library/gemma:2b does not support tools"}}`, KindToolsRejected},
		{400, `{"error":"This model's maximum context length is 8192 tokens"}`, KindContextTooLong},
		{404, `{"error":"model 'nope' not found"}`, KindNotFound},
		{429, `rate limited`, KindOverloaded},
		{500, `{"error":{"message":"boom","type":"kalon_error"}}`, KindServer},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			fmt.Fprint(w, tc.body)
		}))
		c := noSleep(New(srv.URL, "", nil))
		c.MaxAttempts = 1
		_, err := c.Chat(context.Background(), Request{}, nil)
		if KindOf(err) != tc.want {
			t.Errorf("%d %s: kind %v want %v (%v)", tc.status, tc.body, KindOf(err), tc.want, err)
		}
		srv.Close()
	}
}

func TestUnreachableFailsFast(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c := noSleep(New(url, "", nil))
	tries := 0
	c.OnRetry = func(int, time.Duration, error) { tries++ }
	_, err := c.Chat(context.Background(), Request{}, nil)
	if KindOf(err) != KindUnreachable || tries != 1 {
		t.Fatalf("kind %v retries %d err %v", KindOf(err), tries, err)
	}
}

func TestStalledStreamIsRetried(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-time.After(3 * time.Second):
			}
			return
		}
		sse(w, `{"choices":[{"delta":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	c := noSleep(New(srv.URL, "", nil))
	c.IdleTimeout = 200 * time.Millisecond
	resp, err := c.Chat(context.Background(), Request{}, nil)
	if err != nil || resp.Content != "ok" || n.Load() != 2 {
		t.Fatalf("resp %+v err %v calls %d", resp, err, n.Load())
	}
}

func TestNonStreamingJSONReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"whole","tool_calls":[{"id":"x","function":{"name":"glob","arguments":"{\"pattern\":\"*\"}"}}]},"finish_reason":"tool_calls"}]}`)
	}))
	defer srv.Close()
	resp, err := New(srv.URL, "", nil).Chat(context.Background(), Request{}, nil)
	if err != nil || resp.Content != "whole" || len(resp.ToolCalls) != 1 {
		t.Fatalf("%+v %v", resp, err)
	}
}

func TestListModelsFallsBackToOllamaTags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.WriteHeader(404)
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"qwen2.5-coder:7b","details":{"parameter_size":"7.6B","family":"qwen2"}}]}`)
		}
	}))
	defer srv.Close()
	models, err := New(srv.URL+"/v1", "", nil).ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "qwen2.5-coder:7b" || !strings.Contains(models[0].Detail, "7.6B") {
		t.Fatalf("%+v %v", models, err)
	}
}

func TestAuthHeaderAndCustomHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" || r.Header.Get("X-Title") != "kalonide" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"a"}]}`)
	}))
	defer srv.Close()
	models, err := New(srv.URL, "k", map[string]string{"X-Title": "kalonide"}).ListModels(context.Background())
	if err != nil || len(models) != 1 {
		t.Fatalf("%v %v", models, err)
	}
}

func TestThinkSplitterHoldsPartialTags(t *testing.T) {
	var s thinkSplitter
	var c, r strings.Builder
	for _, ch := range []string{"a <", "t", "hink>x", "y</thin", "k>b <", "notatag"} {
		cc, rr := s.feed(ch)
		c.WriteString(cc)
		r.WriteString(rr)
	}
	cc, rr := s.flush()
	c.WriteString(cc)
	r.WriteString(rr)
	if c.String() != "a b <notatag" || r.String() != "xy" {
		t.Fatalf("content %q reasoning %q", c.String(), r.String())
	}
}

func TestBestModelAndCaps(t *testing.T) {
	ms := []ModelInfo{
		{ID: "deepseek-r1:1.5b", Caps: []string{"completion", "tools", "thinking"}},
		{ID: "nomic-embed-text"},
		{ID: "gemma4:4b", Caps: []string{"completion", "tools", "vision"}},
		{ID: "llava:13b", Caps: []string{"completion", "vision"}},
		{ID: "qwen2.5:0.5b", Caps: []string{"completion", "tools"}},
	}
	if got := BestModel(ms); got != "gemma4:4b" {
		t.Fatalf("BestModel = %s", got)
	}
	ms = append(ms, ModelInfo{ID: "qwen2.5-coder:7b", Caps: []string{"completion", "tools"}})
	if got := BestModel(ms); got != "qwen2.5-coder:7b" {
		t.Fatalf("BestModel = %s", got)
	}
	if ParamsB("qwen3.5:4b-q5") != 4 || ParamsB("llama3.1:70b-instruct") != 70 || ParamsB("phi") != 0 || ParamsB("qwen2.5:0.5b") != 0.5 {
		t.Fatal("ParamsB")
	}
	if (ModelInfo{ID: "x"}).Supports("tools") != true || (ModelInfo{ID: "x", Caps: []string{"vision"}}).Supports("tools") {
		t.Fatal("Supports")
	}
}

func TestStallRetriesOnlyOnce(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := noSleep(New(srv.URL, "", nil))
	c.IdleTimeout = 100 * time.Millisecond
	_, err := c.Chat(context.Background(), Request{}, nil)
	if KindOf(err) != KindStalled || n.Load() != 2 {
		t.Fatalf("kind %v attempts %d", KindOf(err), n.Load())
	}
}
