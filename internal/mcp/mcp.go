// Package mcp is a Model Context Protocol client. It starts or connects to
// configured MCP servers, lists their tools and calls them, over stdio
// (a local command) or streamable HTTP (a URL).
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// ProtocolVersion is the MCP revision Kalonide speaks.
const ProtocolVersion = "2025-06-18"

// ServerConfig is one entry of "mcpServers" in settings.json. It uses the
// same shape as other MCP clients, so existing configs can be pasted in.
type ServerConfig struct {
	Command  string            `json:"command,omitempty"`
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	URL      string            `json:"url,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`
}

// Tool is a tool an MCP server offers.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations struct {
		ReadOnlyHint    bool `json:"readOnlyHint"`
		DestructiveHint bool `json:"destructiveHint"`
	} `json:"annotations"`
}

// transport sends one JSON-RPC message and, for requests, returns the
// response. Notifications (id 0) return nil.
type transport interface {
	roundTrip(ctx context.Context, msg rpcRequest) (*rpcResponse, error)
	close() error
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
	Method  string          `json:"method"` // set on server-to-client messages
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("MCP error %d: %s", e.Code, e.Message) }

// Client is a connection to one MCP server.
type Client struct {
	Name         string
	ServerName   string // as the server reports itself
	Instructions string
	t            transport
	nextID       atomic.Int64
	mu           sync.Mutex
	tools        []Tool
}

// Connect starts or reaches the server and performs the MCP handshake.
func Connect(ctx context.Context, name string, cfg ServerConfig) (*Client, error) {
	var t transport
	var err error
	switch {
	case cfg.URL != "":
		t = newHTTPTransport(cfg.URL, cfg.Headers)
	case cfg.Command != "":
		t, err = startStdio(cfg.Command, cfg.Args, cfg.Env)
	default:
		return nil, errors.New("needs a command or a url")
	}
	if err != nil {
		return nil, err
	}
	c := &Client{Name: name, t: t}
	if err := c.initialize(ctx); err != nil {
		t.close()
		return nil, err
	}
	return c, nil
}

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	resp, err := c.t.roundTrip(ctx, rpcRequest{JSONRPC: "2.0", ID: c.nextID.Add(1), Method: method, Params: params})
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return resp.Error
	}
	if out != nil {
		return json.Unmarshal(resp.Result, out)
	}
	return nil
}

func (c *Client) notify(ctx context.Context, method string) error {
	_, err := c.t.roundTrip(ctx, rpcRequest{JSONRPC: "2.0", Method: method})
	return err
}

func (c *Client) initialize(ctx context.Context) error {
	var res struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
		Instructions string `json:"instructions"`
	}
	err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "kalonide", "version": "1"},
	}, &res)
	if err != nil {
		return fmt.Errorf("handshake: %w", err)
	}
	c.ServerName, c.Instructions = res.ServerInfo.Name, res.Instructions
	return c.notify(ctx, "notifications/initialized")
}

// Tools lists the server's tools, following pagination.
func (c *Client) Tools(ctx context.Context) ([]Tool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tools != nil {
		return c.tools, nil
	}
	var all []Tool
	cursor := ""
	for page := 0; page < 50; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var res struct {
			Tools      []Tool `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := c.call(ctx, "tools/list", params, &res); err != nil {
			return nil, err
		}
		all = append(all, res.Tools...)
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	c.tools = all
	return all, nil
}

// Call runs a tool and returns its text output. isError is true when the
// tool reported a failure.
func (c *Client) Call(ctx context.Context, tool string, args json.RawMessage) (string, bool, error) {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	var res struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			MimeType string `json:"mimeType"`
			Resource struct {
				URI  string `json:"uri"`
				Text string `json:"text"`
			} `json:"resource"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if err := c.call(ctx, "tools/call", map[string]any{"name": tool, "arguments": args}, &res); err != nil {
		return "", false, err
	}
	var parts []string
	for _, p := range res.Content {
		switch p.Type {
		case "text":
			parts = append(parts, p.Text)
		case "resource":
			parts = append(parts, fmt.Sprintf("[resource %s]\n%s", p.Resource.URI, p.Resource.Text))
		case "image", "audio":
			parts = append(parts, fmt.Sprintf("[%s omitted: %s]", p.Type, p.MimeType))
		}
	}
	if len(parts) == 0 && len(res.StructuredContent) > 0 {
		parts = append(parts, string(res.StructuredContent))
	}
	return strings.Join(parts, "\n"), res.IsError, nil
}

// Close ends the connection and stops a local server.
func (c *Client) Close() error { return c.t.close() }
