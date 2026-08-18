package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nabooai/grafcli/internal/auth"
	"github.com/nabooai/grafcli/internal/clierr"
)

func testClient(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{
		HTTP:    srv.Client(),
		BaseURL: srv.URL,
		Creds:   auth.Credentials{ClientID: "id", ClientSecret: "secret"},
	}, srv
}

func TestSendsAccessHeaders(t *testing.T) {
	var gotID, gotSecret, gotUA string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotID = r.Header.Get("CF-Access-Client-Id")
		gotSecret = r.Header.Get("CF-Access-Client-Secret")
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"conversations":[]}`))
	})
	c.UserAgent = "grafcli/test"
	if _, err := c.ListConversations(context.Background()); err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if gotID != "id" || gotSecret != "secret" {
		t.Errorf("Access headers not sent: id=%q secret=%q", gotID, gotSecret)
	}
	if gotUA != "grafcli/test" {
		t.Errorf("User-Agent = %q", gotUA)
	}
}

// Cloudflare Access answers an unauthorized request with its sign-in PAGE on a
// 200, after redirecting off our host. Reported as unauthenticated, not as a
// malformed response.
func TestAccessSignInPageIsUnauthenticated(t *testing.T) {
	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<!doctype html><html>sign in</html>"))
	}))
	t.Cleanup(login.Close)

	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, login.URL+"/cdn-cgi/access/login", http.StatusFound)
	})
	_, err := c.Query(context.Background(), "{ __typename }")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.ExitCode != clierr.Unauthenticated {
		t.Fatalf("want exit %d, got %v", clierr.Unauthenticated, err)
	}
}

func TestStatusCodeMapping(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   int
	}{
		{http.StatusBadRequest, `{"detail":"message is required"}`, clierr.InvalidInput},
		{http.StatusUnauthorized, `{"detail":"nope"}`, clierr.Unauthenticated},
		{http.StatusForbidden, `{"detail":"nope"}`, clierr.Forbidden},
		{http.StatusNotFound, `{"detail":"conversation not found"}`, clierr.NotFound},
		{http.StatusTooManyRequests, `{"detail":"slow down"}`, clierr.RateLimited},
		{http.StatusInternalServerError, `{"detail":"boom"}`, clierr.Server},
		{http.StatusBadGateway, `upstream died`, clierr.Server},
	}
	for _, tc := range cases {
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(tc.status)
			w.Write([]byte(tc.body))
		})
		_, err := c.ListConversations(context.Background())
		var ce *clierr.Error
		if !errors.As(err, &ce) {
			t.Errorf("status %d: want a clierr, got %v", tc.status, err)
			continue
		}
		if ce.ExitCode != tc.want {
			t.Errorf("status %d: exit = %d, want %d", tc.status, ce.ExitCode, tc.want)
		}
	}
}

func TestRetryAfterIsCarried(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"detail":"slow down"}`))
	})
	_, err := c.ListConversations(context.Background())
	var ce *clierr.Error
	if !errors.As(err, &ce) {
		t.Fatalf("want a clierr, got %v", err)
	}
	if ce.RetryAfter != 30 {
		t.Errorf("RetryAfter = %d, want 30", ce.RetryAfter)
	}
	if !ce.Retryable {
		t.Error("a 429 must be marked retryable")
	}
}

// The API reports query faults as `errors` STRINGS with a 200, but a stock
// GraphQL server would send {message} objects. Both must read.
func TestErrorMessagesAcceptsBothShapes(t *testing.T) {
	strs := QueryResult{Errors: json.RawMessage(`["Cannot query field 'x'.","second"]`)}
	if got := strs.ErrorMessages(); len(got) != 2 || !strings.HasPrefix(got[0], "Cannot query") {
		t.Errorf("string form: %#v", got)
	}
	objs := QueryResult{Errors: json.RawMessage(`[{"message":"boom"}]`)}
	if got := objs.ErrorMessages(); len(got) != 1 || got[0] != "boom" {
		t.Errorf("object form: %#v", got)
	}
	for _, empty := range []string{``, `null`, `[]`} {
		if got := (QueryResult{Errors: json.RawMessage(empty)}).ErrorMessages(); got != nil {
			t.Errorf("%q should yield no errors, got %#v", empty, got)
		}
	}
}

// Warnings travel under graf.warnings as a code→message map. Dropping them
// turns a truncated page into a confidently wrong answer.
func TestWarningsAreReadFromGraf(t *testing.T) {
	r := QueryResult{Graf: json.RawMessage(`{"warnings":{"TRUNCATED":"rows were capped","AUTO_CORRECTED_0":"renamed a field"}}`)}
	got := r.Warnings()
	if len(got) != 2 {
		t.Fatalf("got %d warnings: %#v", len(got), got)
	}
	// Sorted by code, so output is stable between runs.
	if !strings.HasPrefix(got[0], "AUTO_CORRECTED_0:") || !strings.HasPrefix(got[1], "TRUNCATED:") {
		t.Errorf("warnings are not in a stable order: %#v", got)
	}
	if (QueryResult{}).Warnings() != nil {
		t.Error("an absent graf key should yield no warnings")
	}
}

func TestDetailFromFallsBackToRawBody(t *testing.T) {
	if got := detailFrom([]byte(`{"detail":"a"}`)); got != "a" {
		t.Errorf("detail key: %q", got)
	}
	if got := detailFrom([]byte(`{"message":"b"}`)); got != "b" {
		t.Errorf("message key: %q", got)
	}
	if got := detailFrom([]byte(`plain text`)); got != "plain text" {
		t.Errorf("raw body: %q", got)
	}
	if got := detailFrom(nil); got != "no response body" {
		t.Errorf("empty body: %q", got)
	}
}

// A conversation id comes from the caller and must not be able to climb out of
// the path it is interpolated into.
func TestConversationIDCannotEscapeThePath(t *testing.T) {
	var gotPath string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	})
	_, _ = c.GetConversation(context.Background(), "../../api/config")
	if strings.Contains(gotPath, "/api/config") {
		t.Errorf("path traversal reached %q", gotPath)
	}
}

func TestDebugRedactsCredentials(t *testing.T) {
	var log strings.Builder
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"conversations":[]}`))
	})
	c.Creds = auth.Credentials{ClientID: "0123456789abcdef", ClientSecret: "super-secret-value"}
	c.Debug = &log
	if _, err := c.ListConversations(context.Background()); err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if strings.Contains(log.String(), "super-secret-value") {
		t.Fatalf("--debug leaked the client secret:\n%s", log.String())
	}
	if strings.Contains(log.String(), "0123456789abcdef") {
		t.Fatalf("--debug leaked the full client id:\n%s", log.String())
	}
}
