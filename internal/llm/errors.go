package llm

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
)

// Kind classifies a failure so callers can react to it.
type Kind int

const (
	KindOther          Kind = iota
	KindUnreachable         // server not running or not reachable
	KindOverloaded          // 429 / 503 / 529 after retries
	KindAuth                // 401 / 403
	KindNotFound            // 404: wrong URL or unknown model
	KindToolsRejected       // model or server does not support tool calling
	KindContextTooLong      // prompt exceeds the context window
	KindStalled             // stream stopped sending data
	KindBadRequest          // other 4xx
	KindServer              // other 5xx
)

// Error is a classified client error.
type Error struct {
	Kind   Kind
	Status int
	Msg    string
	Err    error
}

func (e *Error) Error() string {
	var b strings.Builder
	if e.Status != 0 {
		fmt.Fprintf(&b, "HTTP %d: ", e.Status)
	}
	b.WriteString(e.Msg)
	if e.Err != nil && e.Msg == "" {
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// KindOf returns the Kind of err, or KindOther.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return KindOther
}

// classifyStatus maps an HTTP error response to a Kind using the status
// and the server's message.
func classifyStatus(status int, msg string) Kind {
	low := strings.ToLower(msg)
	switch {
	case status == 401 || status == 403:
		return KindAuth
	case status == 429 || status == 503 || status == 529:
		return KindOverloaded
	case mentionsToolsUnsupported(low):
		return KindToolsRejected
	case mentionsContextTooLong(low):
		return KindContextTooLong
	case status == 404:
		return KindNotFound
	case status == 413:
		return KindContextTooLong
	case status >= 500:
		return KindServer
	case status >= 400:
		return KindBadRequest
	}
	return KindOther
}

func mentionsToolsUnsupported(low string) bool {
	if !strings.Contains(low, "tool") && !strings.Contains(low, "function") {
		return false
	}
	for _, p := range []string{"not support", "unsupported", "does not support", "no support", "not available", "not enabled"} {
		if strings.Contains(low, p) {
			return true
		}
	}
	return false
}

func mentionsContextTooLong(low string) bool {
	for _, p := range []string{"context length", "context window", "maximum context", "context_length", "too many tokens", "prompt is too long", "input is too long", "exceeds the context", "reduce the length"} {
		if strings.Contains(low, p) {
			return true
		}
	}
	return false
}

// classifyTransport maps a network error to a Kind.
func classifyTransport(err error) Kind {
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return KindUnreachable
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return KindUnreachable
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return KindUnreachable
	}
	return KindOther
}

// retryable reports whether a request that failed this way may be resent.
func retryable(k Kind) bool {
	switch k {
	case KindOverloaded, KindServer, KindStalled, KindOther, KindUnreachable:
		return true
	}
	return false
}
