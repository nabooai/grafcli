package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/nabooai/grafcli/internal/api"
)

// Context is what the renderer knows before the first event arrives.
type Context struct {
	ChatUUID string
	New      bool
	Model    string
	Endpoint string
	Version  int
}

// Renderer draws the agent's activity on stderr as it happens.
//
// The unit of display is the STEP, printed when it arrives, giving a log in
// arrival order. Graf's FDA runs one tool at a time within a turn, so unlike a
// concurrent agent there is no interleaving to untangle — but the same rule
// still applies: nothing is buffered waiting for a tree to close, because a
// stream that stalls mid-tool would otherwise show nothing at all.
type Renderer struct {
	w     io.Writer
	p     Palette
	width int

	showSteps bool
	showInput bool

	ctx       Context
	started   time.Time
	lastTool  time.Time
	callSeq   int
	answering bool
	wroteAny  bool
	wroteBody bool
}

// New builds a Renderer writing to w.
func New(w io.Writer, color bool, width int) *Renderer {
	if width <= 0 {
		width = 100
	}
	return &Renderer{w: w, p: newPalette(color), width: width}
}

// ShowSteps expands the steps that are hidden by default.
func (r *Renderer) ShowSteps(v bool) { r.showSteps = v }

// ShowInput expands the model_input step, which carries the full system prompt.
func (r *Renderer) ShowInput(v bool) { r.showInput = v }

// SetContext supplies pre-stream facts.
func (r *Renderer) SetContext(c Context) { r.ctx = c }

// Start prints the connecting line.
//
// It appears immediately because there is about half a second of TCP, TLS and
// server setup before the first event can arrive, and showing the destination
// keeps that gap from reading as a hang.
func (r *Renderer) Start() {
	r.started = time.Now()
	r.lastTool = r.started
	// Only a real URL gets reduced to its host; `replay` passes a file label,
	// which must survive intact.
	host := r.ctx.Endpoint
	if strings.Contains(host, "://") {
		trimmed := strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
		host, _, _ = strings.Cut(trimmed, "/")
	}
	r.line("%s⟳ Connecting%s  %s%s%s", r.p.Cyan, r.p.Reset, r.p.Dim, host, r.p.Reset)

	label := "Continued conversation"
	if r.ctx.New {
		label = "New conversation"
	}
	r.blank()
	r.line("%s✦ %s%s  %s%s%s", r.p.Magenta, label, r.p.Reset, r.p.Dim, r.ctx.ChatUUID, r.p.Reset)
	if r.ctx.Model != "" {
		r.line("  %smodel      %s%s", r.p.Dim, r.ctx.Model, r.p.Reset)
	}
}

// Handle draws one event.
func (r *Renderer) Handle(e api.Event) {
	switch e.Type {
	case "v13_step":
		r.step(e.Step)
	case "error":
		if e.Error != "" {
			r.blank()
			r.line("%s✗ Error%s  %s", r.p.Red, r.p.Reset, truncate(e.Error, r.width-10))
		}
	case "message_update":
		// The first answer delta is the boundary between working and
		// answering; label it once so the answer that follows on stdout is not
		// mistaken for more activity.
		if e.DeltaKind == "text_delta" && !r.answering {
			r.answering = true
			r.blank()
			r.line("%s◆ Answer%s", r.p.Green, r.p.Reset)
			r.blank()
		}
	}
}

func (r *Renderer) step(s api.Step) {
	switch s.Kind {
	case "name_hits", "url_facts":
		// Injected grounding: the real stored values the agent was handed
		// before it saw the question. Worth surfacing — an answer that looks
		// wrong is usually explained here.
		r.blank()
		r.line("%s⊕ %s%s", r.p.Blue, steerLabel(s.Kind), r.p.Reset)
		r.body(s.Text, 12)

	case "model_input":
		// Identical every turn and thousands of tokens long; only on request.
		if !r.showInput {
			return
		}
		r.blank()
		r.line("%s⊞ Model input%s", r.p.Gray, r.p.Reset)
		r.body(s.Instructions, 40)

	case "reasoning":
		if s.Text == "" {
			return
		}
		r.blank()
		r.line("%s◆ Thinking%s", r.p.Yellow, r.p.Reset)
		r.body(s.Text, 6)

	case "tool_call":
		r.spaceAfterBody()
		r.callSeq++
		r.lastTool = time.Now()
		args := summarizeArgs(s.Args)
		name := PadTo(s.Tool, 20)
		r.line("%s▸%s %s%s%s  %s%s%s",
			r.p.Cyan, r.p.Reset, r.p.Bold, name, r.p.Reset,
			r.p.Dim, truncate(args, max(20, r.width-32)), r.p.Reset)

	case "tool_result":
		took := time.Since(r.lastTool)
		name := PadTo(s.Tool, 20)
		note, bad := summarizeResult(s.ResultText())
		mark, color := "✓", r.p.Green
		if bad {
			mark, color = "!", r.p.Yellow
		}
		r.line("%s%s%s %s  %s%s  %ds%s",
			color, mark, r.p.Reset, name,
			r.p.Dim, truncate(note, max(20, r.width-38)),
			int(took.Seconds()), r.p.Reset)

	case "assistant_message":
		// The answer reaches stdout as deltas; a duplicate here would print it
		// twice.
		return

	case "loop_end":
		return // the footer is drawn by Finish, after the answer

	default:
		if !r.showSteps {
			return
		}
		text := s.Text
		if text == "" {
			text = s.Kind
		}
		r.line("  %s· %s  %s%s", r.p.Gray, PadTo(s.Kind, 18), truncate(text, max(20, r.width-30)), r.p.Reset)
	}
}

// Finish draws the accounting footer.
func (r *Renderer) Finish(res *api.StreamResult) {
	rows := res.Summary()
	if len(rows) == 0 {
		return
	}
	if res.ConversationID == "" && r.ctx.ChatUUID != "" {
		rows = append(rows, [2]string{"chat", r.ctx.ChatUUID})
	}

	keyW, valW := 0, 0
	for _, kv := range rows {
		if n := DisplayWidth(kv[0]); n > keyW {
			keyW = n
		}
		if n := DisplayWidth(kv[1]); n > valW {
			valW = n
		}
	}
	if maxVal := r.width - keyW - 9; valW > maxVal && maxVal > 10 {
		valW = maxVal
	}
	inner := keyW + valW + 3

	r.blank()
	r.line("%s┌%s┐%s", r.p.Gray, strings.Repeat("─", inner+1), r.p.Reset)
	for _, kv := range rows {
		val := truncate(kv[1], valW)
		r.line("%s│%s %s%s%s  %s %s│%s",
			r.p.Gray, r.p.Reset,
			r.p.Dim, PadTo(kv[0], keyW), r.p.Reset,
			PadTo(val, valW),
			r.p.Gray, r.p.Reset)
	}
	r.line("%s└%s┘%s", r.p.Gray, strings.Repeat("─", inner+1), r.p.Reset)
}

// spaceAfterBody separates a tool line from a preceding indented excerpt, so
// the log does not run into the prose above it.
func (r *Renderer) spaceAfterBody() {
	if r.wroteBody {
		fmt.Fprintln(r.w)
		r.wroteBody = false
	}
}

func (r *Renderer) line(format string, args ...any) {
	r.wroteAny = true
	fmt.Fprintf(r.w, "  "+format+"\n", args...)
}

func (r *Renderer) blank() {
	if !r.wroteAny {
		return
	}
	fmt.Fprintln(r.w)
}

// body prints an indented, wrapped excerpt of at most n lines.
func (r *Renderer) body(text string, n int) {
	if text == "" {
		return
	}
	lines := wrap(text, max(30, r.width-8))
	clipped := false
	if len(lines) > n {
		lines, clipped = lines[:n], true
	}
	for _, l := range lines {
		fmt.Fprintf(r.w, "    %s%s%s\n", r.p.Dim, l, r.p.Reset)
	}
	if clipped {
		fmt.Fprintf(r.w, "    %s…%s\n", r.p.Gray, r.p.Reset)
	}
	r.wroteBody = true
}

func steerLabel(kind string) string {
	switch kind {
	case "name_hits":
		return "Name hits"
	case "url_facts":
		return "URL facts"
	}
	return kind
}

// summarizeArgs renders a tool call's arguments as a one-line preview.
//
// A GraphQL query is the interesting case: it arrives pretty-printed with
// newlines, which would blow up the line, so whitespace is collapsed.
func summarizeArgs(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return collapse(string(raw))
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// A lone argument needs no key: `run_query {query: "{ csa ..."}` reads
	// worse than the query itself.
	if len(keys) == 1 {
		return collapse(fmt.Sprint(m[keys[0]]))
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %s", k, collapse(fmt.Sprint(m[k]))))
	}
	return strings.Join(parts, "  ")
}

// summarizeResult extracts the signal from a tool result and reports whether
// it carried a warning worth flagging.
//
// Results are JSON blobs of tens of kilobytes. What a human watching wants is
// the row count, the warning, or the failure — not the payload.
func summarizeResult(text string) (string, bool) {
	if text == "" {
		return "", false
	}
	var env struct {
		Warnings []string        `json:"warnings"`
		Errors   json.RawMessage `json:"errors"`
		Degraded struct {
			Cause string `json:"cause"`
		} `json:"degraded"`
		Data    json.RawMessage `json:"data"`
		Options []struct {
			Valid bool `json:"valid"`
		} `json:"options"`
	}
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		return collapse(text), false
	}
	switch {
	case len(env.Errors) > 0 && string(env.Errors) != "null" && string(env.Errors) != "[]":
		return collapse(string(env.Errors)), true
	case len(env.Warnings) > 0:
		return collapse(env.Warnings[0]), true
	case env.Degraded.Cause != "":
		return "degraded: " + env.Degraded.Cause, true
	case len(env.Options) > 0:
		valid := 0
		for _, o := range env.Options {
			if o.Valid {
				valid++
			}
		}
		return fmt.Sprintf("%d options (%d valid)", len(env.Options), valid), false
	case len(env.Data) > 0:
		return collapse(string(env.Data)), false
	}
	return collapse(text), false
}

func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
