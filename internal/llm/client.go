package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"

	"time"
)

// Client talks to one OpenAI-compatible endpoint.
type Client struct {
	BaseURL string // e.g. http://localhost:11434/v1
	APIKey  string
	Headers map[string]string
	HTTP    *http.Client

	// MaxAttempts bounds retries of one request (default 5).
	MaxAttempts int
	// UnreachableAttempts bounds retries when nothing is listening
	// (default 2), so a stopped local server fails fast.
	UnreachableAttempts int
	// IdleTimeout aborts a stream that sends nothing for this long
	// (default 120s; local models can be slow to start).
	IdleTimeout time.Duration
	// OnRetry, if set, is told about each retry.
	OnRetry func(attempt int, wait time.Duration, err error)

	sleep func(context.Context, time.Duration) error // replaced in tests
}

// New returns a client with sensible defaults.
func New(baseURL, apiKey string, headers map[string]string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		Headers: headers,
		HTTP:    &http.Client{Transport: http.DefaultTransport},
	}
}

func (c *Client) maxAttempts() int {
	if c.MaxAttempts > 0 {
		return c.MaxAttempts
	}
	return 5
}

func (c *Client) unreachableAttempts() int {
	if c.UnreachableAttempts > 0 {
		return c.UnreachableAttempts
	}
	return 2
}

func (c *Client) idleTimeout() time.Duration {
	if c.IdleTimeout > 0 {
		return c.IdleTimeout
	}
	return 120 * time.Second
}

func (c *Client) wait(ctx context.Context, d time.Duration) error {
	if c.sleep != nil {
		return c.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) newRequest(ctx context.Context, method, url string, body []byte) (*http.Request, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

// Chat sends a streaming chat request and returns the assembled reply.
// onDelta, if non-nil, receives text as it arrives. Requests are retried
// with exponential backoff on overload, server errors, dropped connections
// and stalled streams, but only while nothing has been streamed yet: once
// text reached the caller, a failure is returned with the partial reply.
func (c *Client) Chat(ctx context.Context, req Request, onDelta func(Delta)) (*Response, error) {
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var lastErr error
	limit := c.maxAttempts()
	for attempt := 1; attempt <= limit; attempt++ {
		streamed := false
		emit := func(d Delta) {
			if d.Content != "" || d.Reasoning != "" {
				streamed = true
			}
			if onDelta != nil {
				onDelta(d)
			}
		}
		resp, retryAfter, err := c.chatOnce(ctx, body, emit)
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			return resp, ctx.Err()
		}
		lastErr = err
		kind := KindOf(err)
		if streamed || !retryable(kind) {
			return resp, err
		}
		if kind == KindUnreachable || kind == KindStalled {
			// A stopped or hung server rarely recovers within seconds:
			// fail fast instead of waiting minutes in silence.
			limit = min(limit, c.unreachableAttempts())
		}
		if attempt == limit {
			break
		}
		d := backoff(attempt, retryAfter)
		if c.OnRetry != nil {
			c.OnRetry(attempt, d, err)
		}
		if werr := c.wait(ctx, d); werr != nil {
			return nil, werr
		}
	}
	return nil, lastErr
}

// backoff returns the wait before retry number attempt: the server's
// Retry-After when given (capped at 60s), else 1s, 2s, 4s… up to 20s with
// ±20% jitter.
func backoff(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return min(retryAfter, 60*time.Second)
	}
	d := time.Second << (attempt - 1)
	if d > 20*time.Second || d <= 0 {
		d = 20 * time.Second
	}
	jitter := 0.8 + rand.Float64()*0.4
	return time.Duration(float64(d) * jitter)
}

func parseRetryAfter(h string) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(h, 64); err == nil && secs >= 0 {
		return time.Duration(secs * float64(time.Second))
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// chatOnce performs one HTTP round trip.
func (c *Client) chatOnce(ctx context.Context, body []byte, emit func(Delta)) (*Response, time.Duration, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	req, err := c.newRequest(ctx, http.MethodPost, c.BaseURL+"/chat/completions", body)
	if err != nil {
		return nil, 0, err
	}
	// The idle watchdog also covers the wait for response headers, since a
	// local model may take a while to load before it answers at all.
	idle := c.idleTimeout()
	watchdog := time.AfterFunc(idle, func() { cancel(errStalled) })
	defer watchdog.Stop()

	httpResp, err := c.HTTP.Do(req)
	if err != nil {
		if errors.Is(context.Cause(ctx), errStalled) {
			return nil, 0, &Error{Kind: KindStalled, Msg: fmt.Sprintf("no response for %s", idle)}
		}
		return nil, 0, &Error{Kind: classifyTransport(err), Msg: transportMessage(err, c.BaseURL), Err: err}
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(httpResp.Body, 64<<10))
		msg := errorMessage(data)
		return nil, parseRetryAfter(httpResp.Header.Get("Retry-After")), &Error{
			Kind: classifyStatus(httpResp.StatusCode, msg), Status: httpResp.StatusCode, Msg: msg,
		}
	}

	body2 := &idleReader{r: httpResp.Body, reset: func() { watchdog.Reset(idle) }}
	var resp *Response
	if strings.Contains(httpResp.Header.Get("Content-Type"), "text/event-stream") {
		resp, err = readStream(body2, emit)
	} else {
		// Some servers ignore stream:true and answer with one JSON body.
		resp, err = readWhole(body2, emit)
	}
	if err != nil {
		if errors.Is(context.Cause(ctx), errStalled) {
			return resp, 0, &Error{Kind: KindStalled, Msg: fmt.Sprintf("stream stalled: nothing received for %s", idle)}
		}
		if ctx.Err() != nil && context.Cause(ctx) != nil && !errors.Is(context.Cause(ctx), context.Canceled) {
			return resp, 0, context.Cause(ctx)
		}
		var ce *Error
		if errors.As(err, &ce) {
			return resp, 0, err
		}
		return resp, 0, &Error{Kind: KindOther, Msg: "stream interrupted: " + err.Error(), Err: err}
	}
	return resp, 0, nil
}

var errStalled = errors.New("stream stalled")

type idleReader struct {
	r     io.Reader
	reset func()
}

func (i *idleReader) Read(p []byte) (int, error) {
	n, err := i.r.Read(p)
	if n > 0 {
		i.reset()
	}
	return n, err
}

func transportMessage(err error, base string) string {
	if classifyTransport(err) == KindUnreachable {
		return fmt.Sprintf("cannot reach %s (is the model server running?)", base)
	}
	return err.Error()
}

// errorMessage extracts a readable message from an error body. It handles
// {"error":{"message":…}}, {"error":"…"}, {"message":…} and plain text.
func errorMessage(data []byte) string {
	var v struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Detail  string          `json:"detail"`
	}
	if json.Unmarshal(data, &v) == nil {
		if len(v.Error) > 0 {
			var s string
			if json.Unmarshal(v.Error, &s) == nil && s != "" {
				return s
			}
			var o struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(v.Error, &o) == nil && o.Message != "" {
				return o.Message
			}
		}
		if v.Message != "" {
			return v.Message
		}
		if v.Detail != "" {
			return v.Detail
		}
	}
	s := strings.TrimSpace(string(data))
	if len(s) > 500 {
		s = s[:500] + "…"
	}
	if s == "" {
		s = "empty error response"
	}
	return s
}

// wire types for responses

type wireToolCall struct {
	Index    *int   `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type wireMessage struct {
	Content          *string        `json:"content"`
	Reasoning        string         `json:"reasoning"`
	ReasoningContent string         `json:"reasoning_content"`
	ToolCalls        []wireToolCall `json:"tool_calls"`
}

type wireChoice struct {
	Delta        wireMessage `json:"delta"`
	Message      wireMessage `json:"message"`
	FinishReason *string     `json:"finish_reason"`
}

type wireChunk struct {
	Choices []wireChoice    `json:"choices"`
	Usage   *Usage          `json:"usage"`
	Error   json.RawMessage `json:"error"`
}

// argString turns arguments into a string. Servers send either a JSON
// string ("{\"a\":1}") or, like Ollama's native shape, a raw object.
func argString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// assembler accumulates streamed fragments into a Response.
type assembler struct {
	resp  Response
	text  strings.Builder
	think strings.Builder
	split thinkSplitter
	calls []*ToolCall
	emit  func(Delta)
}

func (a *assembler) addMessage(m wireMessage) {
	if r := m.Reasoning + m.ReasoningContent; r != "" {
		a.think.WriteString(r)
		a.emit(Delta{Reasoning: r})
	}
	if m.Content != nil && *m.Content != "" {
		content, reasoning := a.split.feed(*m.Content)
		if reasoning != "" {
			a.think.WriteString(reasoning)
			a.emit(Delta{Reasoning: reasoning})
		}
		if content != "" {
			a.text.WriteString(content)
			a.emit(Delta{Content: content})
		}
	}
	for _, tc := range m.ToolCalls {
		a.addToolCall(tc)
	}
}

func (a *assembler) addToolCall(tc wireToolCall) {
	var call *ToolCall
	switch {
	case tc.Index != nil:
		for len(a.calls) <= *tc.Index {
			a.calls = append(a.calls, nil)
		}
		if a.calls[*tc.Index] == nil {
			a.calls[*tc.Index] = &ToolCall{Type: "function"}
		}
		call = a.calls[*tc.Index]
	case tc.ID != "":
		for _, c := range a.calls {
			if c != nil && c.ID == tc.ID {
				call = c
			}
		}
	}
	if call == nil {
		call = &ToolCall{Type: "function"}
		a.calls = append(a.calls, call)
	}
	if tc.ID != "" {
		call.ID = tc.ID
	}
	if tc.Function.Name != "" {
		call.Function.Name += tc.Function.Name
		// Some servers resend the full name in every fragment.
		if n := tc.Function.Name; strings.HasSuffix(call.Function.Name, n+n) {
			call.Function.Name = n
		}
	}
	call.Function.Arguments += argString(tc.Function.Arguments)
}

func (a *assembler) finish() *Response {
	content, reasoning := a.split.flush()
	if reasoning != "" {
		a.think.WriteString(reasoning)
		a.emit(Delta{Reasoning: reasoning})
	}
	if content != "" {
		a.text.WriteString(content)
		a.emit(Delta{Content: content})
	}
	a.resp.Content = a.text.String()
	a.resp.Reasoning = a.think.String()
	n := 0
	for _, c := range a.calls {
		if c == nil || c.Function.Name == "" {
			continue
		}
		n++
		if c.ID == "" {
			c.ID = fmt.Sprintf("call_%d_%d", time.Now().UnixNano()%1e9, n)
		}
		if strings.TrimSpace(c.Function.Arguments) == "" {
			c.Function.Arguments = "{}"
		}
		a.resp.ToolCalls = append(a.resp.ToolCalls, *c)
	}
	return &a.resp
}

func (a *assembler) chunk(ch wireChunk) error {
	if len(ch.Error) > 0 && string(ch.Error) != "null" {
		msg := errorMessage([]byte(`{"error":` + string(ch.Error) + `}`))
		return &Error{Kind: classifyStatus(0, msg), Msg: msg}
	}
	if ch.Usage != nil {
		a.resp.Usage = *ch.Usage
	}
	for _, c := range ch.Choices {
		a.addMessage(c.Delta)
		a.addMessage(c.Message)
		if c.FinishReason != nil && *c.FinishReason != "" {
			a.resp.FinishReason = *c.FinishReason
		}
	}
	return nil
}

func readStream(r io.Reader, emit func(Delta)) (*Response, error) {
	a := &assembler{emit: emit}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	var data strings.Builder
	flush := func() (bool, error) {
		payload := strings.TrimSpace(data.String())
		data.Reset()
		if payload == "" {
			return false, nil
		}
		if payload == "[DONE]" {
			return true, nil
		}
		var ch wireChunk
		if err := json.Unmarshal([]byte(payload), &ch); err != nil {
			return false, nil // tolerate keep-alive junk
		}
		return false, a.chunk(ch)
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			done, err := flush()
			if err != nil {
				return a.finish(), err
			}
			if done {
				return a.finish(), nil
			}
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		case strings.HasPrefix(line, ":"):
			// comment / keep-alive
		}
	}
	if err := sc.Err(); err != nil {
		return a.finish(), err
	}
	if _, err := flush(); err != nil {
		return a.finish(), err
	}
	return a.finish(), nil
}

func readWhole(r io.Reader, emit func(Delta)) (*Response, error) {
	data, err := io.ReadAll(io.LimitReader(r, 32<<20))
	if err != nil {
		return nil, err
	}
	a := &assembler{emit: emit}
	// Newline-delimited JSON (Ollama-native style) or one object.
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var ch wireChunk
		if err := json.Unmarshal(line, &ch); err != nil {
			return a.finish(), &Error{Kind: KindBadRequest, Msg: "unexpected response from server: " + errorMessage(data)}
		}
		if err := a.chunk(ch); err != nil {
			return a.finish(), err
		}
	}
	return a.finish(), nil
}

// ListModels returns the models the endpoint serves. It tries the OpenAI
// /models route first and falls back to Ollama's /api/tags.
func (c *Client) ListModels(ctx context.Context) ([]ModelInfo, error) {
	models, err := c.listOpenAI(ctx)
	if err == nil && len(models) > 0 {
		return models, nil
	}
	if alt, err2 := c.listOllama(ctx); err2 == nil && len(alt) > 0 {
		return alt, nil
	}
	if err == nil {
		return models, nil
	}
	return nil, err
}

func (c *Client) getJSON(ctx context.Context, url string, v any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := c.newRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return &Error{Kind: classifyTransport(err), Msg: transportMessage(err, c.BaseURL), Err: err}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 400 {
		msg := errorMessage(data)
		return &Error{Kind: classifyStatus(resp.StatusCode, msg), Status: resp.StatusCode, Msg: msg}
	}
	if err := json.Unmarshal(data, v); err != nil {
		return &Error{Kind: KindBadRequest, Msg: "unexpected response: " + errorMessage(data)}
	}
	return nil
}

func (c *Client) listOpenAI(ctx context.Context) ([]ModelInfo, error) {
	var v struct {
		Data []struct {
			ID           string   `json:"id"`
			OwnedBy      string   `json:"owned_by"`
			Capabilities []string `json:"capabilities"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, c.BaseURL+"/models", &v); err != nil {
		return nil, err
	}
	out := make([]ModelInfo, 0, len(v.Data))
	for _, m := range v.Data {
		if m.ID == "" {
			continue
		}
		info := ModelInfo{ID: m.ID, Detail: m.OwnedBy}
		switch info.Detail {
		case "library", "organization_owner", "system", "kalon", "ollama", "openai", "user":
			info.Detail = ""
		}
		if m.Capabilities != nil {
			info.Caps = m.Capabilities
			var shown []string
			for _, c := range m.Capabilities {
				if c != "completion" {
					shown = append(shown, c)
				}
			}
			info.Detail = strings.Join(shown, " · ")
		}
		out = append(out, info)
	}
	return out, nil
}

func (c *Client) listOllama(ctx context.Context) ([]ModelInfo, error) {
	root := strings.TrimSuffix(c.BaseURL, "/v1")
	var v struct {
		Models []struct {
			Name    string `json:"name"`
			Model   string `json:"model"`
			Details struct {
				ParameterSize string `json:"parameter_size"`
				Family        string `json:"family"`
				Quantization  string `json:"quantization_level"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := c.getJSON(ctx, root+"/api/tags", &v); err != nil {
		return nil, err
	}
	out := make([]ModelInfo, 0, len(v.Models))
	for _, m := range v.Models {
		id := m.Name
		if id == "" {
			id = m.Model
		}
		if id == "" {
			continue
		}
		var parts []string
		for _, p := range []string{m.Details.ParameterSize, m.Details.Family, m.Details.Quantization} {
			if p != "" {
				parts = append(parts, p)
			}
		}
		out = append(out, ModelInfo{ID: id, Detail: strings.Join(parts, " · ")})
	}
	return out, nil
}
