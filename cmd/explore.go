package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newExploreCmd() *cobra.Command {
	var (
		asJSON   bool
		question string
	)
	c := &cobra.Command{
		Use:   "explore <intent>",
		Short: "Run the agent's explore_schema tool: ranked, validated query options",
		Long: `Run explore_schema exactly as the answering agent would: describe what you
want in plain language and get back a small set of ranked query OPTIONS, each
a runnable GraphQL query that was offline-run against the graph, with its row
count, a sample of the rows and a one-line explanation.

Pass the question near-verbatim; padding it with invented synonyms buries the
right root under generic matches. When the intent paraphrases a question that
carried a URL or a key, pass the original with --question so the tool can
harvest those literals. Pass "-" to read the intent from stdin.

stdout is the tool's output, byte-identical to what the agent reads. --json
wraps it as {intent, output}. Spends tokens (the nested explainer).`,
		Example: `  graf explore "open pull requests in the api repo"
  graf explore "that PR" --question "https://github.com/acme/api/pull/42 who reviewed it?"`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), global.timeout)
			defer cancel()

			intent, err := readQuestion(args)
			if err != nil {
				return err
			}
			client, err := newClient(ctx)
			if err != nil {
				return err
			}
			res, err := client.Explore(ctx, intent, question)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				b, err := json.Marshal(res)
				if err != nil {
					return err
				}
				fmt.Fprintln(out, string(b))
				return nil
			}
			fmt.Fprintln(out, res.Output)
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "Emit {intent, output} instead of the bare output")
	c.Flags().StringVar(&question, "question", "", "The raw question the intent paraphrases (its URLs/keys are harvested)")
	return c
}
