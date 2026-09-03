package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/nabooai/grafcli/internal/api"
	"github.com/nabooai/grafcli/internal/cfg"
	"github.com/nabooai/grafcli/internal/ui"
	"github.com/spf13/cobra"
)

func newHarnessCmd() *cobra.Command {
	var (
		asJSON   bool
		maxTurns int
		quiet    bool
	)
	c := &cobra.Command{
		Use:   "harness <question>",
		Short: "Run the entire answering harness single-shot, with receipts",
		Long: `Run the whole pipeline the answering agent is — the steers, the agent's
explore_schema/run_query loop, the grounding verifier — for one question, and
print the answer. Unlike "graf ask" nothing is streamed and no conversation is
kept: one request, one answer, and the receipts that produced it.

stdout is the answer. The receipts — what the agent was told (steers), every
tool it called, the GraphQL it ran — go to stderr when stderr is a terminal,
and are all on stdout under --json as {answer, ungrounded, steers, tools,
queries}. A non-empty "ungrounded" lists artifacts the verifier could not
find in any tool output; the answer already carries a visible caveat for them.

Pass "-" to read the question from stdin. Spends tokens; the server bounds
the run by --max-turns and its own timeout.`,
		Example: `  graf harness "how many open PRs are there?"
  graf harness "what shipped last week?" --json | jq -r .queries[]
  graf harness "who merged the most PRs?" --max-turns 8`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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

			spinner := ui.NewSpinner(os.Stderr, isTTY(os.Stderr), ui.ColorEnabled(os.Stderr), "running the harness")
			spinner.Start()
			result, err := client.Harness(ctx, api.HarnessRequest{
				Question: question,
				Model:    res.Model,
				MaxTurns: maxTurns,
			})
			spinner.Stop()
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if asJSON {
				b, err := json.Marshal(result)
				if err != nil {
					return err
				}
				fmt.Fprintln(out, string(b))
				return nil
			}
			if !quiet && isTTY(os.Stderr) {
				writeReceipts(os.Stderr, result)
			}
			fmt.Fprintln(out, result.Answer)
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "Emit {answer, ungrounded, steers, tools, queries} instead of bare text")
	c.Flags().IntVar(&maxTurns, "max-turns", 0, "Cap on agent turns (default: the server's; the server also caps it)")
	c.Flags().BoolVar(&quiet, "quiet", false, "Suppress the receipts on stderr")
	return c
}

// writeReceipts prints what produced the answer, for a human at a terminal.
func writeReceipts(w *os.File, r api.HarnessResult) {
	for _, s := range r.Steers {
		fmt.Fprintf(w, "steer %s:\n%s\n\n", s.Kind, s.Text)
	}
	for _, t := range r.Tools {
		args := string(t.Args)
		if len(args) > 100 {
			args = args[:100] + "…"
		}
		fmt.Fprintf(w, "tool  %s %s\n", t.Tool, args)
	}
	if len(r.Ungrounded) > 0 {
		fmt.Fprintf(w, "ungrounded: %d artifact(s) the verifier could not find in any tool output\n", len(r.Ungrounded))
	}
	if len(r.Tools) > 0 || len(r.Steers) > 0 {
		fmt.Fprintln(w)
	}
}
