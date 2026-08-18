package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/nabooai/grafcli/internal/clierr"
)

// Event is one decoded frame of the FDA event stream.
//
// The wire carries two shapes on the default (unnamed) SSE event: a
// `v13_step`, which is the agent's own trace, and a `message_update`, which
// carries answer deltas. Named events (`ready`, `done`) bracket the turn. An
// unknown `type` is kept rather than dropped — the server adds step kinds
// faster than a client can be released, and a CLI that rejects them would break
// on every deploy.
type Event struct {
	// Name is the SSE event: "" for a data frame, "ready" or "done".
	Name string
	// Type is the payload discriminator: "v13_step", "message_update", "error".
	Type string
	// Step is the decoded agent trace entry, when Type is "v13_step".
	Step Step
	// Delta is answer text, when Type is "message_update".
	Delta string
	// DeltaKind is the assistant event type, e.g. "text_delta" or
	// "thinking_delta". Reasoning deltas must never be printed as answer text.
	DeltaKind string
	// ReturnCode is set on the terminal "done" event.
	ReturnCode int
	// Error carries the message of an error frame or a failed done.
	Error string
	// Raw is the undecoded payload, for --debug and for step kinds this
	// build does not know about.
	Raw json.RawMessage
}

// Step is one entry of the agent's trace.
type Step struct {
	Kind string `json:"kind"`
	Turn int    `json:"turn,omitempty"`

	// name_hits / url_facts / reasoning / assistant_message
	Text string `json:"text,omitempty"`

	// tool_call
	Tool string          `json:"tool,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`

	// tool_result. The server sends the same payload twice, as `result` and
	// `output`; either may be absent depending on the step kind.
	Result string `json:"result,omitempty"`
	Output string `json:"output,omitempty"`

	// model_input
	Instructions string          `json:"instructions,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`

	// loop_end — the turn's accounting.
	Outcome      string             `json:"outcome,omitempty"`
	Detail       string             `json:"detail,omitempty"`
	NQueryCalls  int                `json:"n_query_calls,omitempty"`
	Turns        int                `json:"turns,omitempty"`
	InputTokens  int                `json:"input_tokens,omitempty"`
	OutputTokens int                `json:"output_tokens,omitempty"`
	CachedTokens int                `json:"cached_tokens,omitempty"`
	TotalTokens  int                `json:"total_tokens,omitempty"`
	CostUSD      float64            `json:"cost_usd,omitempty"`
	WallClockS   float64            `json:"wall_clock_s,omitempty"`
	AuxCalls     int                `json:"aux_calls,omitempty"`
	AuxCostUSD   float64            `json:"aux_cost_usd,omitempty"`
	AuxByModel   map[string]float64 `json:"aux_by_model,omitempty"`
	TTFTMedianS  float64            `json:"ttft_median_s,omitempty"`
	Model        string             `json:"model,omitempty"`
}

// ResultText returns the tool result, preferring `result` over `output`.
func (s Step) ResultText() string {
	if s.Result != "" {
		return s.Result
	}
	return s.Output
}

// StreamResult is what a completed turn produced.
type StreamResult struct {
	// Answer is the concatenation of every text delta.
	Answer string
	// ConversationID is the thread the turn ran in.
	ConversationID string
	// Steps are the trace entries, in arrival order.
	Steps []Step
	// LoopEnd is the accounting step, when the server sent one.
	LoopEnd *Step
	// ReturnCode is the terminal `done` value; 0 means the agent finished.
	ReturnCode int
}

// Stream runs one FDA turn and invokes onEvent for every decoded frame.
//
// onEvent is called from the reading goroutine, in order, and must not block
// for long — the server has no flow control and a slow consumer stalls the
// stream.
func (c *Client) Stream(ctx context.Context, req StreamRequest, onEvent func(Event)) (*StreamResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := c.newRequest(ctx, http.MethodPost, "/api/fda/stream", body)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")

	resp, err := c.do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var src io.Reader = resp.Body
	if c.Recorder != nil {
		src = io.TeeReader(src, c.Recorder)
	}

	res, err := Parse(src, onEvent)
	if err != nil {
		// A stream cut mid-turn is a transport failure, not an agent failure.
		if ce := contextError(err); ce != nil {
			return res, ce
		}
		return res, clierr.New("server", clierr.Server, "the event stream failed: %v", err)
	}
	res.ConversationID = req.ConversationID
	return res, nil
}

// Parse decodes an SSE stream, calling onEvent per frame and accumulating the
// turn's result. It is the single decoder for both the live path and `replay`,
// so a recording exercises exactly the code a live run does.
func Parse(r io.Reader, onEvent func(Event)) (*StreamResult, error) {
	res := &StreamResult{}
	sc := bufio.NewScanner(r)
	// Tool results routinely run to tens of kilobytes on one line; the default
	// 64KB limit would truncate them into a parse error.
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)

	var (
		name    string
		data    strings.Builder
		answer  strings.Builder
		sawDone bool
	)

	flush := func() {
		if data.Len() == 0 && name == "" {
			return
		}
		ev := decodeFrame(name, data.String())
		switch ev.Type {
		case "message_update":
			// Only real answer text accumulates. A thinking delta is the model
			// reasoning aloud and is not part of the answer.
			if ev.DeltaKind == "text_delta" {
				answer.WriteString(ev.Delta)
			}
		case "v13_step":
			res.Steps = append(res.Steps, ev.Step)
			if ev.Step.Kind == "loop_end" {
				s := ev.Step
				res.LoopEnd = &s
			}
		}
		if ev.Name == "done" {
			sawDone = true
			res.ReturnCode = ev.ReturnCode
		}
		if onEvent != nil {
			onEvent(ev)
		}
		name = ""
		data.Reset()
	}

	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // comment / keep-alive
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			field, value = line, ""
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			name = value
		case "data":
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(value)
		}
	}
	flush()
	if err := sc.Err(); err != nil {
		return res, err
	}

	res.Answer = answer.String()
	if !sawDone {
		// The turn ended without its terminal event: the agent died, or the
		// connection was cut. Either way the answer in hand may be a fragment,
		// so say so rather than printing half an answer as if it were whole.
		return res, clierr.New("agent_failed", clierr.AgentFailed,
			"the stream ended before the agent finished").
			WithHint("any answer printed above is incomplete")
	}
	return res, nil
}

func decodeFrame(name, data string) Event {
	ev := Event{Name: name, Raw: json.RawMessage(data)}
	if data == "" {
		return ev
	}

	var probe struct {
		Type       string          `json:"type"`
		Step       json.RawMessage `json:"step"`
		ReturnCode *int            `json:"returncode"`
		Error      string          `json:"error"`
		Message    string          `json:"message"`
		Assistant  struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		} `json:"assistantMessageEvent"`
	}
	if err := json.Unmarshal([]byte(data), &probe); err != nil {
		// Not JSON. Keep the frame rather than failing the turn — an
		// intermediary that injects a comment must not break the stream.
		return ev
	}

	ev.Type = probe.Type
	ev.Error = probe.Error
	if ev.Error == "" && probe.Type == "error" {
		ev.Error = probe.Message
	}
	if probe.ReturnCode != nil {
		ev.ReturnCode = *probe.ReturnCode
	}
	ev.DeltaKind = probe.Assistant.Type
	ev.Delta = probe.Assistant.Delta

	if len(probe.Step) > 0 {
		// A step whose shape this build does not know still yields its kind;
		// unknown fields are simply not populated.
		_ = json.Unmarshal(probe.Step, &ev.Step)
	}
	return ev
}

// Failed reports whether the turn ended badly, and why.
func (r *StreamResult) Failed() *clierr.Error {
	if r == nil {
		return clierr.New("agent_failed", clierr.AgentFailed, "the turn produced no result")
	}
	if r.ReturnCode == 0 {
		return nil
	}
	detail := "the agent did not finish"
	if r.LoopEnd != nil && r.LoopEnd.Detail != "" {
		detail = r.LoopEnd.Detail
	}
	return clierr.New("agent_failed", clierr.AgentFailed,
		"%s (returncode %d)", detail, r.ReturnCode).
		WithHint("rephrasing the question usually helps more than retrying it verbatim")
}

// Summary renders the loop_end accounting as compact key/value pairs.
func (r *StreamResult) Summary() [][2]string {
	if r == nil || r.LoopEnd == nil {
		return nil
	}
	s := r.LoopEnd
	var out [][2]string
	add := func(k, format string, args ...any) {
		out = append(out, [2]string{k, fmt.Sprintf(format, args...)})
	}
	if s.Model != "" {
		add("model", "%s", s.Model)
	}
	if s.Turns > 0 {
		add("turns", "%d", s.Turns)
	}
	if s.NQueryCalls > 0 {
		add("queries", "%d", s.NQueryCalls)
	}
	if s.TotalTokens > 0 {
		add("tokens", "%s in / %s out", comma(s.InputTokens), comma(s.OutputTokens))
	}
	if s.CostUSD > 0 || s.AuxCostUSD > 0 {
		add("cost", "$%.4f", s.CostUSD+s.AuxCostUSD)
	}
	if s.WallClockS > 0 {
		add("elapsed", "%.1fs", s.WallClockS)
	}
	if r.ConversationID != "" {
		add("chat", "%s", r.ConversationID)
	}
	return out
}

func comma(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
