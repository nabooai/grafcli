package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/nabooai/grafcli/internal/clierr"
)

// run executes the command tree with args, capturing stdout separately.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// The published exit-code table is asserted against the constants so the
// documentation cannot drift away from the behavior.
func TestExitCodeTableMatchesConstants(t *testing.T) {
	want := map[string]int{
		"ok": clierr.OK, "generic": clierr.Generic, "usage": clierr.Usage,
		"not_found": clierr.NotFound, "unauthenticated": clierr.Unauthenticated,
		"forbidden": clierr.Forbidden, "invalid_input": clierr.InvalidInput,
		"rate_limited": clierr.RateLimited, "timeout": clierr.Timeout,
		"server": clierr.Server, "agent_failed": clierr.AgentFailed,
		"graphql": clierr.GraphQLError,
	}
	if len(exitCodeTable) != len(want) {
		t.Fatalf("table has %d entries, constants have %d", len(exitCodeTable), len(want))
	}
	seen := map[int]string{}
	for _, e := range exitCodeTable {
		name, code := e["name"].(string), e["code"].(int)
		if w, ok := want[name]; !ok || w != code {
			t.Errorf("%s = %d, want %d", name, code, want[name])
		}
		if prev, dup := seen[code]; dup {
			t.Errorf("exit code %d is claimed by both %s and %s", code, prev, name)
		}
		seen[code] = name

		// Retryability is derived from the code in clierr; the table must agree.
		wantRetry := code == clierr.RateLimited || code == clierr.Timeout ||
			code == clierr.Server || code == clierr.AgentFailed
		if e["retryable"].(bool) != wantRetry {
			t.Errorf("%s retryable = %v, want %v", name, e["retryable"], wantRetry)
		}
	}
}

// The manifest is how an agent discovers the CLI, so it must work with no
// credential and no network.
func TestManifestNeedsNoCredential(t *testing.T) {
	t.Setenv("GRAF_CONFIG_DIR", t.TempDir())
	t.Setenv("GRAF_NO_DOTENV", "1")
	out, err := run(t, "manifest")
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	var m struct {
		Name     string `json:"name"`
		Commands []struct {
			Name     string `json:"name"`
			Short    string `json:"short"`
			Commands []struct {
				Name string `json:"name"`
			} `json:"commands"`
		} `json:"commands"`
		ExitCodes []map[string]any `json:"exit_codes"`
	}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("manifest is not valid JSON: %v\n%s", err, out)
	}
	if m.Name != "graf" {
		t.Errorf("name = %q", m.Name)
	}
	if len(m.ExitCodes) == 0 {
		t.Error("manifest carries no exit codes")
	}
	found := map[string]bool{}
	for _, c := range m.Commands {
		found[c.Name] = true
		if c.Short == "" {
			t.Errorf("command %q has no description", c.Name)
		}
	}
	for _, want := range []string{"ask", "query", "schema", "chats", "auth", "config", "doctor", "sources",
		"steer", "explore", "run-query", "harness", "update"} {
		if !found[want] {
			t.Errorf("manifest omits the %q command", want)
		}
	}
}

func TestVersionJSON(t *testing.T) {
	out, err := run(t, "version", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]string
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	for _, k := range []string{"version", "go", "os", "arch"} {
		if v[k] == "" {
			t.Errorf("version JSON is missing %q", k)
		}
	}
}

// Nothing but the payload may reach stdout: a caller pipes it without first
// checking whether the command succeeded.
func TestErrorsKeepStdoutClean(t *testing.T) {
	t.Setenv("GRAF_CONFIG_DIR", t.TempDir())
	t.Setenv("GRAF_NO_DOTENV", "1")
	for _, args := range [][]string{
		{"config", "get", "no_such_key"},
		{"config", "set", "fda_version", "not-a-number"},
		{"replay", "/nonexistent/file.sse"},
	} {
		out, err := run(t, args...)
		if err == nil {
			t.Errorf("%v: expected an error", args)
		}
		if out != "" {
			t.Errorf("%v: wrote %q to stdout on failure", args, out)
		}
	}
}

// A CLI that blocks on a prompt with no human present hangs its caller
// forever, which is worse than any error.
func TestNoQuestionDoesNotHang(t *testing.T) {
	t.Setenv("GRAF_NO_INPUT", "1")
	_, err := readQuestion(nil)
	if err == nil {
		t.Fatal("expected a usage error rather than a read from stdin")
	}
	var ce *clierr.Error
	if !as(err, &ce) || ce.ExitCode != clierr.Usage {
		t.Fatalf("want a usage error, got %v", err)
	}
	if ce.Fix == "" {
		t.Error("the error should carry a runnable fix")
	}
}

func TestReadQuestionTrimsAndRejectsEmpty(t *testing.T) {
	got, err := readQuestion([]string{"  what changed?  "})
	if err != nil || got != "what changed?" {
		t.Errorf("got %q, err %v", got, err)
	}
	if _, err := readQuestion([]string{"   "}); err == nil {
		t.Error("an all-whitespace question should be rejected")
	}
}

// A --record path that cannot be created must fail before a turn is spent.
func TestReplayRejectsAMissingFile(t *testing.T) {
	_, err := run(t, "replay", "/nonexistent/file.sse")
	var ce *clierr.Error
	if !as(err, &ce) || ce.ExitCode != clierr.NotFound {
		t.Fatalf("want not-found, got %v", err)
	}
}

func TestReplayRendersARecording(t *testing.T) {
	if _, err := os.Stat("../testdata/session.sse"); err != nil {
		t.Skip("fixture missing")
	}
	out, err := run(t, "replay", "../testdata/session.sse", "--plain")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	// The answer goes to stdout; the activity view goes to stderr.
	if !strings.Contains(out, "Alex Rivera") {
		t.Errorf("the answer did not reach stdout:\n%.300s", out)
	}
	if strings.Contains(out, "⟳ Connecting") {
		t.Error("the activity view leaked onto stdout")
	}
}

func TestReplayPropagatesAFailedTurn(t *testing.T) {
	out, err := run(t, "replay", "../testdata/failed.sse", "--plain")
	var ce *clierr.Error
	if !as(err, &ce) || ce.ExitCode != clierr.AgentFailed {
		t.Fatalf("want agent-failed, got %v", err)
	}
	if out != "" {
		t.Errorf("a failed turn wrote %q to stdout", out)
	}
}

func as(err error, target **clierr.Error) bool {
	for err != nil {
		if e, ok := err.(*clierr.Error); ok {
			*target = e
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
