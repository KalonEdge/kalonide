package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ---- stdio ----

type stdioTransport struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[int64]chan *rpcResponse
	done    chan struct{} // closed when the server exits
	stderr  *tailBuffer
}

func startStdio(command string, args []string, env map[string]string) (*stdioTransport, error) {
	cmd := exec.Command(command, args...)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	t := &stdioTransport{cmd: cmd, stdin: stdin, pending: map[int64]chan *rpcResponse{}, done: make(chan struct{}), stderr: &tailBuffer{max: 4096}}
	cmd.Stderr = t.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", command, err)
	}
	go t.readLoop(stdout)
	return t, nil
}

func (t *stdioTransport) readLoop(r io.Reader) {
	defer close(t.done)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 32<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var msg rpcResponse
		if json.Unmarshal(line, &msg) != nil {
			continue // servers sometimes log to stdout; ignore non-JSON
		}
		switch {
		case msg.Method != "" && msg.ID != nil:
			t.answerServer(msg)
		case msg.ID != nil:
			t.mu.Lock()
			ch := t.pending[*msg.ID]
			delete(t.pending, *msg.ID)
			t.mu.Unlock()
			if ch != nil {
				ch <- &msg
			}
		}
	}
	_ = t.cmd.Wait()
}

// answerServer replies to requests the server sends us: ping is answered,
// everything else is politely declined.
func (t *stdioTransport) answerServer(msg rpcResponse) {
	reply := map[string]any{"jsonrpc": "2.0", "id": *msg.ID}
	if msg.Method == "ping" {
		reply["result"] = map[string]any{}
	} else {
		reply["error"] = map[string]any{"code": -32601, "message": "not supported by kalonide"}
	}
	data, _ := json.Marshal(reply)
	t.write(data)
}

func (t *stdioTransport) write(data []byte) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	_, err := t.stdin.Write(append(data, '\n'))
	return err
}

func (t *stdioTransport) roundTrip(ctx context.Context, req rpcRequest) (*rpcResponse, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var ch chan *rpcResponse
	if req.ID != 0 {
		ch = make(chan *rpcResponse, 1)
		t.mu.Lock()
		t.pending[req.ID] = ch
		t.mu.Unlock()
		defer func() {
			t.mu.Lock()
			delete(t.pending, req.ID)
			t.mu.Unlock()
		}()
	}
	if err := t.write(data); err != nil {
		return nil, t.exitError(err)
	}
	if ch == nil {
		return nil, nil
	}
	select {
	case resp := <-ch:
		return resp, nil
	case <-t.done:
		return nil, t.exitError(errors.New("the server exited"))
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// exitError adds the end of the server's error output, which usually says
// why it failed.
func (t *stdioTransport) exitError(err error) error {
	if tail := strings.TrimSpace(t.stderr.String()); tail != "" {
		return fmt.Errorf("%w: %s", err, lastLine(tail))
	}
	return err
}

func (t *stdioTransport) close() error {
	t.stdin.Close()
	select {
	case <-t.done:
	case <-time.After(2 * time.Second):
		_ = syscall.Kill(-t.cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-t.done:
		case <-time.After(2 * time.Second):
			_ = syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	return nil
}

func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	if len(l) > 300 {
		l = l[:300] + "…"
	}
	return l
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.max; over > 0 {
		b.buf = b.buf[over:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// ---- streamable HTTP ----

type httpTransport struct {
	url     string
	headers map[string]string
	client  *http.Client

	mu      sync.Mutex
	session string
}

func newHTTPTransport(url string, headers map[string]string) *httpTransport {
	return &httpTransport{url: url, headers: headers, client: &http.Client{Timeout: 5 * time.Minute}}
}

func (t *httpTransport) roundTrip(ctx context.Context, req rpcRequest) (*rpcResponse, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "application/json, text/event-stream")
	hreq.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	for k, v := range t.headers {
		hreq.Header.Set(k, v)
	}
	t.mu.Lock()
	if t.session != "" {
		hreq.Header.Set("Mcp-Session-Id", t.session)
	}
	t.mu.Unlock()

	resp, err := t.client.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		t.mu.Lock()
		t.session = s
		t.mu.Unlock()
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if req.ID == 0 {
		return nil, nil // notification: 202 Accepted, no body
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		return readSSEResponse(resp.Body, req.ID)
	}
	var out rpcResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("bad response: %w", err)
	}
	return &out, nil
}

// readSSEResponse reads server-sent events until the response to id.
func readSSEResponse(r io.Reader, id int64) (*rpcResponse, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 32<<20)
	var data strings.Builder
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			continue
		}
		if line != "" || data.Len() == 0 {
			continue
		}
		var msg rpcResponse
		if json.Unmarshal([]byte(data.String()), &msg) == nil && msg.ID != nil && *msg.ID == id && msg.Method == "" {
			return &msg, nil
		}
		data.Reset()
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("the server closed the stream without answering")
}

func (t *httpTransport) close() error {
	t.mu.Lock()
	session := t.session
	t.mu.Unlock()
	if session == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Mcp-Session-Id", session)
	if resp, err := t.client.Do(req); err == nil {
		resp.Body.Close()
	}
	return nil
}
