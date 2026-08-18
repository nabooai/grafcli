// Package api is a hand-written client for the Graf HTTP API.
//
// The surface the CLI needs is small — conversations, one SSE turn endpoint,
// GraphQL passthrough, and the schema — so it is written out rather than
// generated. There is no OpenAPI document to generate from in any case.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nabooai/grafcli/internal/auth"
	"github.com/nabooai/grafcli/internal/clierr"
)

// Client talks to one Graf deployment.
type Client struct {
	HTTP    *http.Client
	BaseURL string
	Creds   auth.Credentials

	// Debug, when non-nil, receives a one-line trace per request. Credentials
	// are redacted at this single chokepoint so no call site can leak them.
	Debug io.Writer
	// Recorder, when non-nil, receives the raw SSE bytes for `--record`.
	Recorder io.Writer
	// UserAgent identifies the CLI build to the server.
	UserAgent string
}

// Conversation is one FDA thread.
type Conversation struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	CreatedAt float64 `json:"created_at"`
	UpdatedAt float64 `json:"updated_at"`
	Messages  []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages,omitempty"`
}

// Created returns CreatedAt as a time. The API sends float epoch seconds.
func (c Conversation) Created() time.Time { return epoch(c.CreatedAt) }

// Updated returns UpdatedAt as a time.
func (c Conversation) Updated() time.Time { return epoch(c.UpdatedAt) }

func epoch(f float64) time.Time {
	if f == 0 {
		return time.Time{}
	}
	sec, frac := int64(f), f-float64(int64(f))
	return time.Unix(sec, int64(frac*1e9))
}

// StreamRequest is the body of POST /api/fda/stream.
type StreamRequest struct {
	ConversationID string `json:"conversation_id"`
	Message        string `json:"message"`
	Model          string `json:"model,omitempty"`
	Reasoning      string `json:"reasoning,omitempty"`
	FdaVersion     int    `json:"fda_version,omitempty"`
}

func (c *Client) url(path string) string {
	return strings.TrimRight(c.BaseURL, "/") + path
}

// newRequest builds a request carrying the Cloudflare Access credential.
func (c *Client) newRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(path), rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	ua := c.UserAgent
	if ua == "" {
		ua = "grafcli"
	}
	req.Header.Set("User-Agent", ua)
	if c.Creds.ClientID != "" && c.Creds.ClientSecret != "" {
		req.Header.Set("CF-Access-Client-Id", c.Creds.ClientID)
		req.Header.Set("CF-Access-Client-Secret", c.Creds.ClientSecret)
	}
	if c.Creds.Cookie != "" {
		req.Header.Set("Cookie", "CF_Authorization="+c.Creds.Cookie)
	}
	if c.Debug != nil {
		fmt.Fprintf(c.Debug, "> %s %s (auth: %s)\n", method, req.URL, c.authKind())
	}
	return req, nil
}

func (c *Client) authKind() string {
	switch {
	case c.Creds.ClientID != "":
		return "service token " + auth.Redact(c.Creds.ClientID)
	case c.Creds.Cookie != "":
		return "CF_Authorization cookie"
	}
	return "none"
}

// do executes a request and maps a non-2xx response onto the exit-code table.
func (c *Client) do(req *http.Request) (*http.Response, error) {
	// Captured before the round trip: Cloudflare Access answers an unauthorized
	// request by redirecting to its own login host, so resp.Request.URL is the
	// login page, not the endpoint we asked for.
	reqPath := req.URL.Path
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, c.transportError(err)
	}
	if c.Debug != nil {
		fmt.Fprintf(c.Debug, "< %s %s\n", resp.Status, resp.Header.Get("Content-Type"))
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if e := accessChallenge(resp, reqPath); e != nil {
			resp.Body.Close()
			return nil, e
		}
		return resp, nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return nil, c.statusError(resp, body)
}

// accessChallenge detects a Cloudflare Access login page returned with a 200.
//
// When the credential is missing or wrong, Access does not fail the request —
// it answers with the HTML sign-in page, which would otherwise be parsed as a
// malformed API response and reported as a baffling JSON error.
func accessChallenge(resp *http.Response, reqPath string) *clierr.Error {
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		return nil
	}
	// The API only ever answers JSON or an event stream, so HTML in reply to an
	// /api path means the request never reached Graf.
	if !strings.HasPrefix(reqPath, "/api/") {
		return nil
	}
	return clierr.New("unauthenticated", clierr.Unauthenticated,
		"Cloudflare Access returned its sign-in page instead of the API").
		WithHint("the service token is missing, wrong, or not authorized for this application").
		WithFix("graf auth status")
}

func (c *Client) transportError(err error) error {
	if ce := contextError(err); ce != nil {
		return ce
	}
	return clierr.New("network", clierr.Server, "could not reach %s: %v", c.BaseURL, err).
		WithHint("check the URL with --url, and that you are on a network that can see it")
}

func contextError(err error) *clierr.Error {
	switch {
	case err == nil:
		return nil
	case strings.Contains(err.Error(), "context deadline exceeded"):
		return clierr.New("timeout", clierr.Timeout, "the request exceeded its deadline").
			WithHint("raise it with --timeout")
	case strings.Contains(err.Error(), "context canceled"):
		return clierr.New("canceled", clierr.Generic, "canceled")
	}
	return nil
}

// statusError maps an HTTP status onto the documented exit-code table.
func (c *Client) statusError(resp *http.Response, body []byte) error {
	detail := detailFrom(body)

	switch resp.StatusCode {
	case http.StatusBadRequest:
		return clierr.New("invalid_input", clierr.InvalidInput, "%s", detail)
	case http.StatusUnauthorized:
		return clierr.New("unauthenticated", clierr.Unauthenticated, "%s", detail).
			WithFix("graf auth status")
	case http.StatusForbidden:
		return clierr.New("forbidden", clierr.Forbidden, "%s", detail).
			WithHint("the credential is valid but not authorized for this deployment")
	case http.StatusNotFound:
		return clierr.New("not_found", clierr.NotFound, "%s", detail)
	case http.StatusTooManyRequests:
		e := clierr.New("rate_limited", clierr.RateLimited, "%s", detail)
		if ra, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
			e.RetryAfter = ra
			e.WithHint("retry after %ds", ra)
		}
		return e
	}
	if resp.StatusCode >= 500 {
		return clierr.New("server", clierr.Server, "server error %d: %s", resp.StatusCode, detail)
	}
	return clierr.New("http", clierr.Generic, "unexpected status %d: %s", resp.StatusCode, detail)
}

// detailFrom reads the error message out of a response body. FastAPI answers
// {"detail": ...}, but a proxy in front of it may answer anything at all, so
// fall back to the raw text rather than reporting an empty error.
func detailFrom(body []byte) string {
	var env struct {
		Detail json.RawMessage `json:"detail"`
		Error  string          `json:"error"`
		Msg    string          `json:"message"`
	}
	if err := json.Unmarshal(body, &env); err == nil {
		if len(env.Detail) > 0 {
			var s string
			if json.Unmarshal(env.Detail, &s) == nil && s != "" {
				return s
			}
			return string(env.Detail)
		}
		if env.Error != "" {
			return env.Error
		}
		if env.Msg != "" {
			return env.Msg
		}
	}
	s := strings.TrimSpace(string(body))
	if s == "" {
		return "no response body"
	}
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decode(resp.Body, out)
}

func (c *Client) sendJSON(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		body, err = json.Marshal(in)
		if err != nil {
			return err
		}
	}
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return decode(resp.Body, out)
}

func decode(r io.Reader, out any) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, out); err != nil {
		return clierr.New("server", clierr.Server,
			"could not parse the server's response: %v", err)
	}
	return nil
}

// ListConversations returns every FDA conversation, newest first.
func (c *Client) ListConversations(ctx context.Context) ([]Conversation, error) {
	var out struct {
		Conversations []Conversation `json:"conversations"`
	}
	if err := c.getJSON(ctx, "/api/fda/conversations", &out); err != nil {
		return nil, err
	}
	return out.Conversations, nil
}

// CreateConversation opens a new thread. An empty title lets the server derive
// one from the first message.
func (c *Client) CreateConversation(ctx context.Context, title string) (Conversation, error) {
	var out Conversation
	in := map[string]any{}
	if title != "" {
		in["title"] = title
	}
	err := c.sendJSON(ctx, http.MethodPost, "/api/fda/conversations", in, &out)
	return out, err
}

// GetConversation returns one thread including its messages.
func (c *Client) GetConversation(ctx context.Context, id string) (Conversation, error) {
	var out Conversation
	err := c.getJSON(ctx, "/api/fda/conversations/"+urlSeg(id), &out)
	return out, err
}

// RenameConversation sets a thread's title.
func (c *Client) RenameConversation(ctx context.Context, id, title string) error {
	return c.sendJSON(ctx, http.MethodPatch, "/api/fda/conversations/"+urlSeg(id),
		map[string]string{"title": title}, nil)
}

// DeleteConversation removes a thread and its stored agent session.
func (c *Client) DeleteConversation(ctx context.Context, id string) error {
	return c.sendJSON(ctx, http.MethodDelete, "/api/fda/conversations/"+urlSeg(id), nil, nil)
}

// QueryResult is the response of POST /api/query.
//
// The serve answers {data, errors, graf}. A query fault is reported as an
// `errors` entry rather than a 500, so a non-nil Errors is the normal way a bad
// query comes back — not an exception.
type QueryResult struct {
	Data json.RawMessage `json:"data"`
	// Errors is a list of STRINGS on this API, already enriched server-side
	// with "did you mean" hints. Decoded loosely because a plain GraphQL
	// server would send objects here instead.
	Errors json.RawMessage `json:"errors,omitempty"`
	Graf   json.RawMessage `json:"graf,omitempty"`
}

// ErrorMessages returns the query errors as text, accepting both the list of
// strings this API sends and the {message} objects GraphQL specifies.
func (q QueryResult) ErrorMessages() []string {
	if len(q.Errors) == 0 || string(q.Errors) == "null" {
		return nil
	}
	var asStrings []string
	if err := json.Unmarshal(q.Errors, &asStrings); err == nil {
		return nonEmpty(asStrings)
	}
	var asObjects []struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(q.Errors, &asObjects); err == nil {
		out := make([]string, 0, len(asObjects))
		for _, e := range asObjects {
			out = append(out, e.Message)
		}
		return nonEmpty(out)
	}
	return []string{string(q.Errors)}
}

// Warnings returns the serve's warning channel, which travels under
// graf.warnings as a code→message map. Ignoring these is how a truncated page
// gets reported as a complete answer.
func (q QueryResult) Warnings() []string {
	if len(q.Graf) == 0 {
		return nil
	}
	var g struct {
		Warnings map[string]string `json:"warnings"`
	}
	if err := json.Unmarshal(q.Graf, &g); err != nil {
		return nil
	}
	codes := make([]string, 0, len(g.Warnings))
	for k := range g.Warnings {
		codes = append(codes, k)
	}
	sort.Strings(codes)
	out := make([]string, 0, len(codes))
	for _, c := range codes {
		out = append(out, c+": "+g.Warnings[c])
	}
	return out
}

func nonEmpty(in []string) []string {
	out := in[:0]
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Query runs a GraphQL query against the graph.
func (c *Client) Query(ctx context.Context, query string) (QueryResult, error) {
	var out QueryResult
	err := c.sendJSON(ctx, http.MethodPost, "/api/query",
		map[string]string{"query": query}, &out)
	return out, err
}

// Schema returns the graph's GraphQL SDL. v2 selects the newer projection.
func (c *Client) Schema(ctx context.Context, v2 bool) (string, error) {
	path := "/api/schema"
	if v2 {
		path = "/api/schema/v2"
	}
	var out struct {
		Schema string `json:"schema"`
	}
	if err := c.getJSON(ctx, path, &out); err != nil {
		return "", err
	}
	return out.Schema, nil
}

// Config returns the deployment's graph configuration (data sources, endpoints,
// relationships). Credential VALUES are `$VAR` references in this document, not
// secrets, but it is large — callers should expect kilobytes.
func (c *Client) Config(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.getJSON(ctx, "/api/config", &out)
	return out, err
}

// SecretNames lists the names of the secrets the deployment holds. Values never
// leave the server.
func (c *Client) SecretNames(ctx context.Context) ([]string, error) {
	var out struct {
		Names []string `json:"names"`
	}
	err := c.getJSON(ctx, "/api/fda/secrets", &out)
	return out.Names, err
}

// urlSeg escapes a path segment. Conversation ids are uuids in practice, but
// they arrive from the caller and must not be able to escape the path.
func urlSeg(s string) string {
	return strings.NewReplacer("/", "%2F", "?", "%3F", "#", "%23", " ", "%20").Replace(s)
}
