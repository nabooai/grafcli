package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/nabooai/grafcli/internal/api"
	"github.com/nabooai/grafcli/internal/cfg"
	"github.com/nabooai/grafcli/internal/clierr"
	"github.com/nabooai/grafcli/internal/ui"
	"github.com/spf13/cobra"
)

type askFlags struct {
	chat   string
	title  string
	asJSON bool
	events bool
	steps  bool
	input  bool
	plain  bool
	record string
	width  int
	quiet  bool
}

func newAskCmd() *cobra.Command {
	var f askFlags

	c := &cobra.Command{
		Use:   "ask <question>",
		Short: "Ask the agent a question about your organization's data",
		Long: `Ask a question. The agent locates the source in the graph, writes the
GraphQL, runs it, and reports the rows that came back.

Pass "-" as the question to read it from stdin.

Each question opens a new conversation unless --chat names one to continue.
The conversation id is printed to stderr, and is on stdout under --json, so a
follow-up can be scripted.`,
		Example: `  graf ask "latest csa sessions by navnav"
  graf ask "how many open PRs are there?" --json
  echo "which repos did nave touch last week?" | graf ask -
  graf ask "what changed?" --events
  ID=$(graf ask "summarize the incident" --json | jq -r .conversation_id)
  graf ask "who was on call?" --chat "$ID"`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAsk(cmd, args, &f)
		},
	}

	fl := c.Flags()
	fl.StringVar(&f.chat, "chat", "", "Continue an existing conversation by id")
	fl.StringVar(&f.title, "title", "", "Title for a newly created conversation")
	fl.BoolVar(&f.asJSON, "json", false, "Emit {answer, conversation_id, ...} instead of bare text")
	fl.BoolVar(&f.events, "events", false, "Show the agent's reasoning, tool calls and queries on stderr")
	fl.BoolVar(&f.steps, "steps", false, "With --events, also show step kinds hidden by default")
	fl.BoolVar(&f.input, "show-input", false, "With --events, also show the full model input")
	fl.BoolVar(&f.plain, "plain", false, "Disable color, Markdown rendering and progress output")
	fl.StringVar(&f.record, "record", "", "Save the raw event stream to a file for later replay")
	fl.IntVar(&f.width, "width", 0, "Render the activity view at a fixed width")
	fl.BoolVar(&f.quiet, "quiet", false, "Suppress the conversation id notice on stderr")

	return c
}

// readQuestion takes the question from argv, or from stdin when given "-".
func readQuestion(args []string) (string, error) {
	if len(args) == 1 && args[0] != "-" {
		q := strings.TrimSpace(args[0])
		if q == "" {
			return "", clierr.Usagef("the question is empty")
		}
		return q, nil
	}

	wantStdin := len(args) == 1 && args[0] == "-"
	if !wantStdin {
		// No question given. Reading stdin here would hang whenever no human
		// is present, so refuse and say exactly what to do instead.
		if noInput() {
			return "", clierr.Usagef("no question given").
				WithHint("stdin is not a terminal, so there is nothing to prompt").
				WithFix(`graf ask "your question here"`)
		}
		return "", clierr.Usagef("no question given").
			WithFix(`graf ask "your question here"`)
	}

	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	q := strings.TrimSpace(string(b))
	if q == "" {
		return "", clierr.Usagef("stdin was empty")
	}
	return q, nil
}

func runAsk(cmd *cobra.Command, args []string, f *askFlags) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), global.timeout)
	defer cancel()

	question, err := readQuestion(args)
	if err != nil {
		return err
	}

	res, err := cfg.Resolve(global.baseURL, global.model, global.reasoning, global.fdaVersion)
	if err != nil {
		return err
	}
	client, err := newClient(ctx)
	if err != nil {
		return err
	}

	if f.steps || f.input {
		f.events = true
	}

	if f.record != "" {
		rec, err := os.Create(f.record)
		if err != nil {
			return clierr.New("invalid_input", clierr.InvalidInput,
				"could not open --record file: %v", err)
		}
		defer rec.Close()
		client.Recorder = rec
	}

	// A conversation must exist before a turn can run in it: the stream
	// endpoint 404s on an unknown id rather than creating one.
	convID, isNew := f.chat, false
	if convID == "" {
		conv, err := client.CreateConversation(ctx, f.title)
		if err != nil {
			return err
		}
		convID, isNew = conv.ID, true
	}

	stdout := cmd.OutOrStdout()
	// Deltas are the answer text itself, so they may only go to stdout when
	// stdout is where the final payload would land anyway.
	bareText := !f.asJSON

	// Terminal affordances are opt-out and TTY-gated. Piped output stays
	// byte-identical to what the API returned, so none of this touches a
	// non-terminal stdout.
	renderMD := !f.plain && bareText && isTTY(os.Stdout) && ui.ColorEnabled(os.Stdout)
	streamToStdout := bareText && !renderMD

	// Without this a plain ask shows nothing at all for several seconds. It is
	// suppressed under --events, where the activity view already owns stderr.
	spinner := ui.NewSpinner(os.Stderr,
		!f.plain && !f.events && isTTY(os.Stderr),
		ui.ColorEnabled(os.Stderr), "thinking")
	spinner.Start()
	defer spinner.Stop()

	var renderer *ui.Renderer
	if f.events && !f.plain {
		renderer = ui.New(os.Stderr, ui.ColorEnabled(os.Stderr), termWidth(f.width))
		renderer.ShowSteps(f.steps)
		renderer.ShowInput(f.input)
		renderer.SetContext(ui.Context{
			ChatUUID: convID,
			New:      isNew,
			Model:    res.Model,
			Endpoint: res.BaseURL,
			Version:  res.FdaVersion,
		})
		renderer.Start()
	}

	var sink io.Writer = io.Discard
	if streamToStdout {
		sink = stdout
	}

	var stopped bool
	result, err := client.Stream(ctx, api.StreamRequest{
		ConversationID: convID,
		Message:        question,
		Model:          res.Model,
		Reasoning:      res.Reasoning,
		FdaVersion:     res.FdaVersion,
	}, func(e api.Event) {
		// The first event is the signal that waiting is over; the spinner must
		// clear its line before anything else writes to stderr.
		if !stopped {
			spinner.Stop()
			stopped = true
		}
		if renderer != nil {
			renderer.Handle(e)
		}
		if e.Type == "message_update" && e.DeltaKind == "text_delta" {
			fmt.Fprint(sink, e.Delta)
		}
	})
	spinner.Stop()
	if streamToStdout && result != nil && result.Answer != "" {
		fmt.Fprintln(stdout)
	}
	if err != nil {
		// A partial answer has already reached stdout; the error still governs
		// the exit code so a caller never mistakes a fragment for a result.
		return err
	}
	if renderer != nil {
		renderer.Finish(result)
	}
	if e := result.Failed(); e != nil {
		return e
	}

	if !f.quiet && !f.asJSON && isTTY(os.Stderr) {
		fmt.Fprintf(os.Stderr, "\nconversation: %s\n", convID)
	}

	return emitAnswer(stdout, result, f, streamToStdout, renderMD)
}

func emitAnswer(w io.Writer, res *api.StreamResult, f *askFlags, alreadyWritten, renderMD bool) error {
	if f.asJSON {
		payload := map[string]any{
			"answer":          res.Answer,
			"conversation_id": res.ConversationID,
		}
		if res.LoopEnd != nil {
			payload["usage"] = res.LoopEnd
		}
		if q := collectQueries(res); len(q) > 0 {
			payload["queries"] = q
		}
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		fmt.Fprintln(w, string(b))
		return nil
	}

	if alreadyWritten {
		return nil // already written incrementally
	}
	if renderMD {
		fmt.Fprintln(w, ui.RenderMarkdown(res.Answer, true))
		return nil
	}
	fmt.Fprintln(w, res.Answer)
	return nil
}

// collectQueries pulls the GraphQL the agent actually ran out of the trace.
//
// This is the most reusable thing a turn produces: once the agent has found the
// right query, `graf query` can run it again for free.
func collectQueries(res *api.StreamResult) []string {
	var out []string
	for _, s := range res.Steps {
		if s.Kind != "tool_call" || s.Tool != "run_query" || len(s.Args) == 0 {
			continue
		}
		var a struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(s.Args, &a); err == nil && a.Query != "" {
			out = append(out, a.Query)
		}
	}
	return out
}
