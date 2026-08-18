// Package clierr defines the CLI's error contract: a documented exit code per
// failure class, plus a machine-readable envelope emitted on stderr.
//
// The envelope is written to stderr in every output mode so that stdout keeps a
// single shape and a caller never has to check for a success-vs-failure
// discriminator before parsing it.
package clierr

import (
	"encoding/json"
	"fmt"
	"io"
)

// Exit codes. There is no cross-CLI convention above 1 (gh uses 2 for
// cancellation, kubectl 3 for unchanged, sysexits 64 for usage), so this table
// is our own and is part of the public contract. Only Timeout, RateLimited and
// Server are worth retrying; AgentFailed is retryable with a different question.
const (
	OK              = 0
	Generic         = 1
	Usage           = 2
	NotFound        = 3
	Unauthenticated = 4
	Forbidden       = 5
	InvalidInput    = 7
	RateLimited     = 8
	Timeout         = 9
	Server          = 10
	// AgentFailed means the turn reached the agent but the agent could not
	// finish: a non-zero returncode on the terminal `done` event, or a stream
	// that ended without one. Distinct from Server because the request was
	// well-formed and accepted — retrying the same question may work, and
	// rephrasing it usually does.
	AgentFailed = 13
	// GraphQLError means /api/query executed but the graph returned errors.
	// The query is wrong, not the transport, so retrying it unchanged cannot help.
	GraphQLError = 14
)

// Error is both a Go error and the JSON envelope written to stderr.
type Error struct {
	Kind     string `json:"kind"`
	ExitCode int    `json:"exit_code"`
	Detail   string `json:"detail"`
	Hint     string `json:"hint,omitempty"`
	// Fix is a command the caller can run verbatim to make progress. Agents
	// re-run this string directly, so it must always be a complete command.
	Fix        string `json:"fix,omitempty"`
	RetryAfter int    `json:"retry_after,omitempty"`
	Retryable  bool   `json:"retryable"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Detail
}

// Write emits the human line followed by the JSON envelope, both on stderr.
func (e *Error) Write(w io.Writer) {
	if e == nil {
		return
	}
	fmt.Fprintf(w, "error: %s\n", e.Detail)
	if e.Hint != "" {
		fmt.Fprintf(w, "hint: %s\n", e.Hint)
	}
	if e.Fix != "" {
		fmt.Fprintf(w, "fix: %s\n", e.Fix)
	}
	b, err := json.Marshal(map[string]any{"error": e})
	if err != nil {
		return
	}
	fmt.Fprintf(w, "%s\n", b)
}

func retryable(code int) bool {
	switch code {
	case RateLimited, Timeout, Server, AgentFailed:
		return true
	}
	return false
}

// New builds an Error, deriving Retryable from the exit code so the two can
// never disagree.
func New(kind string, code int, format string, args ...any) *Error {
	return &Error{
		Kind:      kind,
		ExitCode:  code,
		Detail:    fmt.Sprintf(format, args...),
		Retryable: retryable(code),
	}
}

// WithHint returns the Error carrying a human-facing explanation.
func (e *Error) WithHint(format string, args ...any) *Error {
	e.Hint = fmt.Sprintf(format, args...)
	return e
}

// WithFix returns the Error carrying a runnable command.
func (e *Error) WithFix(format string, args ...any) *Error {
	e.Fix = fmt.Sprintf(format, args...)
	return e
}

// Usagef reports a malformed invocation.
func Usagef(format string, args ...any) *Error {
	return New("usage", Usage, format, args...)
}

// NotLoggedIn is returned whenever no usable credential is available.
func NotLoggedIn() *Error {
	return New("unauthenticated", Unauthenticated, "no Cloudflare Access credential").
		WithHint("set GRAF_CF_ACCESS_CLIENT_ID and GRAF_CF_ACCESS_CLIENT_SECRET, put them in .env, or store them with auth login").
		WithFix("graf auth login --with-token")
}
