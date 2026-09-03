package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nabooai/grafcli/internal/clierr"
)

// cliServer fakes the graf serve's /api/cli endpoints and records the bearer
// it was sent. Each handler answers the documented shape.
func cliServer(t *testing.T) (*httptest.Server, *string) {
	t.Helper()
	var bearer string
	mux := http.NewServeMux()
	record := func(r *http.Request) { bearer = r.Header.Get("Authorization") }
	mux.HandleFunc("/api/cli/steer", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"question": in["question"],
			"steers": []map[string]string{
				{"kind": "name_hits", "text": "saki is a customer"},
			},
			"input": []map[string]string{{"role": "user", "content": in["question"]}},
		})
	})
	mux.HandleFunc("/api/cli/explore", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"intent": in["intent"], "output": "OPTIONS for " + in["intent"] + " q=" + in["question"],
		})
	})
	mux.HandleFunc("/api/cli/run_query", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		w.Header().Set("Content-Type", "application/json")
		output := `{"warnings": ["scope-bounded"], "data": {"github": {"listReleases": [{"id": 1}]}}}`
		if strings.Contains(in["query"], "noSuchRoot") {
			output = "Query errors:\nCannot query field 'noSuchRoot' on type 'Query'."
		}
		json.NewEncoder(w).Encode(map[string]string{"query": in["query"], "output": output})
	})
	mux.HandleFunc("/api/cli/ask", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"answer":     "42 PRs (max_turns " + jsonNum(in["max_turns"]) + ")",
			"ungrounded": []string{},
			"steers":     []map[string]string{},
			"tools": []map[string]any{
				{"tool": "run_query", "args": map[string]string{"query": "{ github { listPullRequestsCount } }"}},
			},
			"queries": []string{"{ github { listPullRequestsCount } }"},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &bearer
}

func jsonNum(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func setupCLI(t *testing.T) (*httptest.Server, *string) {
	t.Helper()
	t.Setenv("GRAF_CONFIG_DIR", t.TempDir())
	t.Setenv("GRAF_NO_DOTENV", "1")
	t.Setenv("GRAF_API_TOKEN", "tok-123")
	srv, bearer := cliServer(t)
	t.Setenv("GRAF_URL", srv.URL)
	return srv, bearer
}

func TestSteerSendsTheTokenAndPrintsBlocks(t *testing.T) {
	_, bearer := setupCLI(t)
	out, err := run(t, "steer", "what is new with saki?")
	if err != nil {
		t.Fatalf("steer: %v", err)
	}
	if *bearer != "Bearer tok-123" {
		t.Errorf("Authorization = %q", *bearer)
	}
	if out != "## name_hits\nsaki is a customer\n" {
		t.Errorf("stdout = %q", out)
	}
	out, err = run(t, "steer", "what is new with saki?", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Steers []struct{ Kind string } `json:"steers"`
		Input  json.RawMessage         `json:"input"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || len(v.Steers) != 1 || v.Steers[0].Kind != "name_hits" || len(v.Input) == 0 {
		t.Errorf("--json = %q (%v)", out, err)
	}
}

func TestExplorePassesTheQuestionThrough(t *testing.T) {
	setupCLI(t)
	out, err := run(t, "explore", "open PRs", "--question", "which PRs are open?")
	if err != nil {
		t.Fatalf("explore: %v", err)
	}
	if out != "OPTIONS for open PRs q=which PRs are open?\n" {
		t.Errorf("stdout = %q", out)
	}
}

func TestRunQueryPrintsTheEnvelopeAndMapsErrors(t *testing.T) {
	setupCLI(t)
	out, err := run(t, "run-query", "{ github { listReleases { id } } }", "--compact")
	if err != nil {
		t.Fatalf("run-query: %v", err)
	}
	if !strings.Contains(out, `"listReleases"`) || !strings.Contains(out, `"warnings"`) {
		t.Errorf("stdout = %q", out)
	}
	out, err = run(t, "run-query", "{ noSuchRoot { id } }")
	var ce *clierr.Error
	if !as(err, &ce) || ce.ExitCode != clierr.GraphQLError {
		t.Fatalf("want a graphql error (exit %d), got %v", clierr.GraphQLError, err)
	}
	if !strings.Contains(ce.Detail, "noSuchRoot") {
		t.Errorf("the error should carry the tool's text, got %q", ce.Detail)
	}
	if out != "" {
		t.Errorf("a rejected query wrote %q to stdout", out)
	}
}

func TestHarnessReturnsTheAnswerAndReceipts(t *testing.T) {
	setupCLI(t)
	out, err := run(t, "harness", "how many PRs?", "--max-turns", "7")
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	if out != "42 PRs (max_turns 7)\n" {
		t.Errorf("stdout = %q", out)
	}
	out, err = run(t, "harness", "how many PRs?", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Answer  string   `json:"answer"`
		Queries []string `json:"queries"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || len(v.Queries) != 1 {
		t.Errorf("--json = %q (%v)", out, err)
	}
}

// A 401 from the token gate is reported as unauthenticated with a fix.
func TestTokenRejectionIsUnauthenticated(t *testing.T) {
	t.Setenv("GRAF_CONFIG_DIR", t.TempDir())
	t.Setenv("GRAF_NO_DOTENV", "1")
	t.Setenv("GRAF_API_TOKEN", "wrong")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"detail":"missing/invalid bearer token"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("GRAF_URL", srv.URL)
	out, err := run(t, "steer", "q")
	var ce *clierr.Error
	if !as(err, &ce) || ce.ExitCode != clierr.Unauthenticated {
		t.Fatalf("want unauthenticated, got %v", err)
	}
	if out != "" {
		t.Errorf("wrote %q to stdout", out)
	}
}

// A loopback deployment needs no credential; anywhere else, a missing one is
// still an error before any request is made.
func TestLoopbackNeedsNoCredential(t *testing.T) {
	t.Setenv("GRAF_CONFIG_DIR", t.TempDir())
	t.Setenv("GRAF_NO_DOTENV", "1")
	srv, bearer := cliServer(t)
	t.Setenv("GRAF_URL", srv.URL) // httptest binds 127.0.0.1
	if _, err := run(t, "steer", "q"); err != nil {
		t.Fatalf("loopback without a credential: %v", err)
	}
	if *bearer != "" {
		t.Errorf("sent a bearer with no credential: %q", *bearer)
	}
	t.Setenv("GRAF_URL", "https://graf.example.com")
	_, err := run(t, "steer", "q")
	var ce *clierr.Error
	if !as(err, &ce) || ce.ExitCode != clierr.Unauthenticated {
		t.Fatalf("want unauthenticated off-loopback, got %v", err)
	}
}
