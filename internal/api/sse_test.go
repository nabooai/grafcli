package api

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/nabooai/grafcli/internal/clierr"
)

func parseFile(t *testing.T, name string) (*StreamResult, []Event, error) {
	t.Helper()
	f, err := os.Open("../../testdata/" + name)
	if err != nil {
		t.Fatalf("opening fixture: %v", err)
	}
	defer f.Close()
	var events []Event
	res, err := Parse(f, func(e Event) { events = append(events, e) })
	return res, events, err
}

func TestParseRecordedSession(t *testing.T) {
	res, events, err := parseFile(t, "session.sse")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("no events decoded")
	}
	if res.ReturnCode != 0 {
		t.Errorf("ReturnCode = %d, want 0", res.ReturnCode)
	}
	if e := res.Failed(); e != nil {
		t.Errorf("Failed() = %v, want nil", e)
	}
	if !strings.Contains(res.Answer, "Alex Rivera") {
		t.Errorf("answer does not carry the expected text: %.80q", res.Answer)
	}
	if res.LoopEnd == nil {
		t.Fatal("loop_end step was not captured")
	}
	if res.LoopEnd.Turns == 0 || res.LoopEnd.WallClockS == 0 {
		t.Errorf("loop_end accounting is empty: %+v", res.LoopEnd)
	}

	var toolCalls, toolResults int
	for _, s := range res.Steps {
		switch s.Kind {
		case "tool_call":
			toolCalls++
		case "tool_result":
			toolResults++
		}
	}
	if toolCalls == 0 || toolResults == 0 {
		t.Errorf("tool steps missing: %d calls, %d results", toolCalls, toolResults)
	}
}

// A stream cut before its terminal event must not be reported as a success:
// the answer in hand is a fragment.
func TestParseTruncatedStreamFails(t *testing.T) {
	res, _, err := parseFile(t, "truncated.sse")
	if err == nil {
		t.Fatal("want an error for a stream with no done event")
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.ExitCode != clierr.AgentFailed {
		t.Fatalf("want exit %d, got %v", clierr.AgentFailed, err)
	}
	// The partial answer is still returned so the caller can show what arrived.
	if res.Answer != "There are 3,020" {
		t.Errorf("partial answer = %q", res.Answer)
	}
}

func TestParseFailedTurn(t *testing.T) {
	res, _, err := parseFile(t, "failed.sse")
	if err != nil {
		t.Fatalf("Parse should succeed on a well-formed failure stream: %v", err)
	}
	if res.ReturnCode != 1 {
		t.Fatalf("ReturnCode = %d, want 1", res.ReturnCode)
	}
	e := res.Failed()
	if e == nil {
		t.Fatal("Failed() = nil for a returncode 1 turn")
	}
	if e.ExitCode != clierr.AgentFailed {
		t.Errorf("exit code = %d, want %d", e.ExitCode, clierr.AgentFailed)
	}
	if !strings.Contains(e.Detail, "provider unavailable") {
		t.Errorf("detail does not carry the server's reason: %q", e.Detail)
	}
	if !e.Retryable {
		t.Error("an agent failure should be marked retryable")
	}
}

// The server adds event types and step kinds faster than a client can ship, so
// unknown ones must pass through instead of failing the turn.
func TestParseToleratesUnknownShapes(t *testing.T) {
	res, events, err := parseFile(t, "unknown.sse")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// A reasoning delta is not answer text and must never be concatenated
	// into it — that would put the model's private thinking on stdout.
	if res.Answer != "ok" {
		t.Errorf("Answer = %q, want %q", res.Answer, "ok")
	}
	var sawFuture, sawNewKind bool
	for _, e := range events {
		if e.Type == "v99_future_event" {
			sawFuture = true
		}
		if e.Step.Kind == "some_new_kind" {
			sawNewKind = true
		}
	}
	if !sawFuture {
		t.Error("an unknown event type was dropped instead of passed through")
	}
	if !sawNewKind {
		t.Error("an unknown step kind was dropped instead of passed through")
	}
}

// Tool results run to tens of kilobytes on a single line; the scanner's
// default 64KB limit would turn one into a parse error.
func TestParseHandlesVeryLongLines(t *testing.T) {
	huge := strings.Repeat("x", 400_000)
	stream := "event: ready\ndata: {}\n\n" +
		`data: {"type":"v13_step","step":{"kind":"tool_result","tool":"run_query","result":"` + huge + `"}}` +
		"\n\nevent: done\ndata: {\"returncode\":0}\n\n"
	res, err := Parse(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Steps) != 1 || len(res.Steps[0].ResultText()) != len(huge) {
		t.Fatalf("long tool result was truncated: %d steps", len(res.Steps))
	}
}

func TestSummaryFormatsAccounting(t *testing.T) {
	res, _, err := parseFile(t, "session.sse")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	res.ConversationID = "abc-123"
	got := map[string]string{}
	for _, kv := range res.Summary() {
		got[kv[0]] = kv[1]
	}
	if got["chat"] != "abc-123" {
		t.Errorf("chat = %q", got["chat"])
	}
	if !strings.Contains(got["tokens"], ",") {
		t.Errorf("tokens are not thousands-separated: %q", got["tokens"])
	}
	if !strings.HasPrefix(got["cost"], "$") {
		t.Errorf("cost = %q", got["cost"])
	}
}

func TestComma(t *testing.T) {
	for in, want := range map[int]string{0: "0", 7: "7", 999: "999", 1000: "1,000", 15417: "15,417", 1234567: "1,234,567"} {
		if got := comma(in); got != want {
			t.Errorf("comma(%d) = %q, want %q", in, got, want)
		}
	}
}
