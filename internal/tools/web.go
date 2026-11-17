package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// WebFetch downloads a public web page as text.
type WebFetch struct {
	// AllowPrivate permits private and loopback addresses (tests only).
	AllowPrivate bool
}

func (WebFetch) Name() string { return "web_fetch" }
func (WebFetch) Kind() Kind   { return KindNetwork }
func (WebFetch) Description() string {
	return "Fetch a public http(s) URL and return its text (HTML is converted to plain text). Not for localhost or private networks; use bash with curl for those."
}
func (WebFetch) Schema() json.RawMessage {
	return schema([]string{"url"}, [3]string{"url", "string", "http or https URL"})
}

type fetchArgs struct {
	URL string `json:"url"`
}

func (WebFetch) Summary(args json.RawMessage) string {
	var a fetchArgs
	_ = json.Unmarshal(args, &a)
	return a.URL
}

var errPrivate = errors.New("refusing to connect to a private, loopback or link-local address")

func blockPrivate(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() ||
		(ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1]&0xC0 == 64) { // CGNAT 100.64/10
		return errPrivate
	}
	return nil
}

func (w WebFetch) Run(ctx context.Context, _ *Env, args json.RawMessage) (string, error) {
	var a fetchArgs
	if err := decode(args, &a); err != nil {
		return "", err
	}
	u, err := url.Parse(strings.TrimSpace(a.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("not an http(s) URL: %q", a.URL)
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if !w.AllowPrivate {
		dialer.Control = blockPrivate // checked on every connection, including redirects
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
			Proxy:               nil,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "kalonide/0.1 (+terminal coding agent)")
	req.Header.Set("Accept", "text/html,text/plain,application/json,*/*;q=0.5")
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, errPrivate) {
			return "", fmt.Errorf("%s resolves to a private or local address; use bash with curl for local servers", u.Host)
		}
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 3<<20))
	if err != nil {
		return "", err
	}
	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, u.Host)
	}
	if looksBinary(data) && !strings.Contains(ct, "text") && !strings.Contains(ct, "json") {
		return fmt.Sprintf("%s returned binary content (%s, %d bytes); not shown.", u, ct, len(data)), nil
	}
	text := string(data)
	if strings.Contains(ct, "html") || strings.HasPrefix(strings.TrimSpace(strings.ToLower(text)), "<!doctype html") || strings.Contains(text[:min(len(text), 512)], "<html") {
		text = HTMLToText(text)
	}
	return fmt.Sprintf("URL: %s\nStatus: %d\n\n%s", resp.Request.URL, resp.StatusCode, Truncate(text, 20000)), nil
}

var (
	reDropBlocks = regexp.MustCompile(`(?is)<(script|style|noscript|svg|head|template|iframe)\b.*?</(script|style|noscript|svg|head|template|iframe)>`)
	reComments   = regexp.MustCompile(`(?s)<!--.*?-->`)
	reBlockTags  = regexp.MustCompile(`(?i)</?(p|div|br|li|ul|ol|h[1-6]|tr|table|section|article|header|footer|nav|blockquote|pre|hr)\b[^>]*>`)
	reHeading    = regexp.MustCompile(`(?i)<h([1-6])\b[^>]*>`)
	reListItem   = regexp.MustCompile(`(?i)<li\b[^>]*>`)
	reTags       = regexp.MustCompile(`(?s)<[^>]+>`)
	reSpaces     = regexp.MustCompile(`[ \t\f\v\r]+`)
	reBlankLines = regexp.MustCompile(`\n\s*\n\s*\n+`)
)

// HTMLToText reduces an HTML document to readable text.
func HTMLToText(s string) string {
	s = reComments.ReplaceAllString(s, "")
	s = reDropBlocks.ReplaceAllString(s, "")
	s = reHeading.ReplaceAllStringFunc(s, func(m string) string {
		n := reHeading.FindStringSubmatch(m)[1]
		return "\n\n" + strings.Repeat("#", int(n[0]-'0')) + " "
	})
	s = reListItem.ReplaceAllString(s, "\n- ")
	s = reBlockTags.ReplaceAllString(s, "\n")
	s = reTags.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = reSpaces.ReplaceAllString(s, " ")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	s = strings.Join(lines, "\n")
	s = reBlankLines.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
