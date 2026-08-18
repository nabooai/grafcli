package ui

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/nabooai/grafcli/internal/api"
)

func render(t *testing.T, fixture string, configure func(*Renderer)) string {
	t.Helper()
	f, err := os.Open("../../testdata/" + fixture)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	defer f.Close()

	var buf bytes.Buffer
	r := New(&buf, false, 100) // color off: the assertions are about content
	if configure != nil {
		configure(r)
	}
	r.SetContext(Context{ChatUUID: "chat-1", New: true, Endpoint: "https://graf.example.com/x"})
	r.Start()
	res, err := api.Parse(f, r.Handle)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	r.Finish(res)
	return buf.String()
}

func TestRendererDrawsTheTurn(t *testing.T) {
	out := render(t, "session.sse", nil)
	for _, want := range []string{
		"⟳ Connecting", "graf.example.com", // host only, not the full URL
		"✦ New conversation", "chat-1",
		"⊕ Name hits",
		"explore_schema", "run_query",
		"◆ Thinking",
		"◆ Answer",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("activity view is missing %q:\n%s", want, out)
		}
	}
}

// The answer belongs on stdout. If the renderer also echoed deltas, the text
// would be printed twice — once here and once by the caller.
func TestRendererNeverEchoesAnswerText(t *testing.T) {
	const marker = "UNIQUE-ANSWER-MARKER-9f3a"
	stream := "event: ready\ndata: {}\n\n" +
		`data: {"type":"v13_step","step":{"kind":"assistant_message","text":"` + marker + `"}}` + "\n\n" +
		`data: {"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"` + marker + `"}}` + "\n\n" +
		"event: done\ndata: {\"returncode\":0}\n\n"

	var buf bytes.Buffer
	r := New(&buf, false, 80)
	r.Start()
	res, err := api.Parse(strings.NewReader(stream), r.Handle)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	r.Finish(res)

	if strings.Contains(buf.String(), marker) {
		t.Errorf("the renderer echoed answer text:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "◆ Answer") {
		t.Error("the answer boundary was not marked")
	}
	if res.Answer != marker {
		t.Errorf("Answer = %q", res.Answer)
	}
}

// model_input carries the whole system prompt and is identical every turn, so
// it stays hidden unless asked for.
func TestModelInputIsHiddenByDefault(t *testing.T) {
	def := render(t, "session.sse", nil)
	if strings.Contains(def, "Model input") {
		t.Error("model_input was shown without --show-input")
	}
	shown := render(t, "session.sse", func(r *Renderer) { r.ShowInput(true) })
	if !strings.Contains(shown, "Model input") {
		t.Error("--show-input did not reveal model_input")
	}
	if len(shown) <= len(def) {
		t.Error("--show-input added nothing")
	}
}

func TestUnknownStepKindsAppearOnlyUnderSteps(t *testing.T) {
	def := render(t, "unknown.sse", nil)
	if strings.Contains(def, "some_new_kind") {
		t.Error("an unknown step kind was shown by default")
	}
	verbose := render(t, "unknown.sse", func(r *Renderer) { r.ShowSteps(true) })
	if !strings.Contains(verbose, "some_new_kind") {
		t.Error("--steps did not reveal an unknown step kind")
	}
}

// The box is drawn by hand, so its right edge is easy to get wrong by one cell.
func TestFooterBoxIsRectangular(t *testing.T) {
	out := render(t, "session.sse", nil)
	var widths []int
	for _, line := range strings.Split(out, "\n") {
		if strings.ContainsAny(line, "┌│└") {
			widths = append(widths, DisplayWidth(line))
		}
	}
	if len(widths) < 3 {
		t.Fatalf("expected a footer box, found %d lines", len(widths))
	}
	for i, w := range widths {
		if w != widths[0] {
			t.Errorf("footer line %d is %d cells, want %d:\n%s", i, w, widths[0], out)
			break
		}
	}
}

// replay passes a file label, not a URL; reducing it to a "host" would erase it.
func TestNonURLEndpointSurvives(t *testing.T) {
	var buf bytes.Buffer
	r := New(&buf, false, 80)
	r.SetContext(Context{Endpoint: "recording: /tmp/run.sse"})
	r.Start()
	if !strings.Contains(buf.String(), "/tmp/run.sse") {
		t.Errorf("the recording label was stripped:\n%s", buf.String())
	}
}

func TestSummarizeResult(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantBad bool
	}{
		{"warning wins", `{"warnings":["rows were capped"],"data":{"a":1}}`, "rows were capped", true},
		{"degraded", `{"degraded":{"cause":"index_missing_or_stale"},"options":[]}`, "degraded: index_missing_or_stale", true},
		{"errors", `{"errors":["Cannot query field 'x'"]}`, "Cannot query field 'x'", true},
		{"data", `{"warnings":[],"data":{"csa":{"sessionCount":3020}}}`, `{"csa":{"sessionCount":3020}}`, false},
		{"not json", "plain text", "plain text", false},
		{"empty", "", "", false},
	}
	for _, tc := range cases {
		got, bad := summarizeResult(tc.in)
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: got %q, want it to contain %q", tc.name, got, tc.want)
		}
		if bad != tc.wantBad {
			t.Errorf("%s: flagged = %v, want %v", tc.name, bad, tc.wantBad)
		}
	}
}

func TestSummarizeResultCountsExploreOptions(t *testing.T) {
	got, _ := summarizeResult(`{"options":[{"valid":true},{"valid":false},{"valid":true}]}`)
	if got != "3 options (2 valid)" {
		t.Errorf("got %q", got)
	}
}

// A GraphQL query arrives pretty-printed; left alone it would wreck the line.
func TestSummarizeArgsCollapsesWhitespace(t *testing.T) {
	got := summarizeArgs(json.RawMessage(`{"query":"{\n  csa {\n    sessionCount\n  }\n}"}`))
	if strings.Contains(got, "\n") {
		t.Errorf("newlines survived: %q", got)
	}
	if got != "{ csa { sessionCount } }" {
		t.Errorf("got %q", got)
	}
	// A lone argument prints bare; several are labelled and ordered.
	multi := summarizeArgs(json.RawMessage(`{"b":"2","a":"1"}`))
	if multi != "a: 1  b: 2" {
		t.Errorf("multi-arg form = %q", multi)
	}
	if summarizeArgs(nil) != "" {
		t.Error("nil args should render empty")
	}
}

func TestDisplayWidthHandlesEmojiAndCJK(t *testing.T) {
	cases := map[string]int{"abc": 3, "": 0, "✦": 1, "🤖": 2, "日本": 4}
	for in, want := range cases {
		if got := DisplayWidth(in); got != want {
			t.Errorf("DisplayWidth(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestRenderMarkdownIsAPassthroughWithoutColor(t *testing.T) {
	src := "**bold** and `code`"
	if got := RenderMarkdown(src, false); got != src {
		t.Errorf("markdown was altered with color off: %q", got)
	}
	if got := RenderMarkdown(src, true); got == src {
		t.Error("markdown was not styled with color on")
	}
}
