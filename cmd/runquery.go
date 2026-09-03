package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/nabooai/grafcli/internal/clierr"
	"github.com/spf13/cobra"
)

func newRunQueryCmd() *cobra.Command {
	var (
		asJSON   bool
		compact  bool
		question string
	)
	c := &cobra.Command{
		Use:   "run-query <graphql>",
		Short: "Run the agent's run_query tool and print the envelope it reads",
		Long: `Run a GraphQL query through the answering agent's run_query TOOL — not the
bare /api/query — and print the envelope the agent reads: {warnings, data,
generation, evaluated_at}, with reference ids pre-resolved (<field>__resolved
siblings) and large results trimmed to whole rows with a RESULT TRUNCATED
warning. "graf query" is the bare serve; this is what the model sees.

Pass "-" to read the query from stdin. Warnings are repeated on stderr: a
non-empty list means the rows may be a SUBSET — never report a flagged
result as the whole population.

Pass --question with the user's question when you have it: the tool exempts
the question's literals from its invented-literal check, as inside the agent.

When the graph rejects the query, the tool's error text (which names the
valid fields) goes to stderr with exit code 14 — fix the query, do not retry
it unchanged. No tokens are spent.`,
		Example: `  graf run-query '{ github { listPullRequests(first: 5) { title userLogin } } }'
  graf run-query - < query.graphql | jq .data`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), global.timeout)
			defer cancel()

			query, err := readQuery(args)
			if err != nil {
				return err
			}
			client, err := newClient(ctx)
			if err != nil {
				return err
			}
			res, err := client.RunQueryTool(ctx, query, question)
			if err != nil {
				return err
			}

			// The tool answers a JSON envelope on success and a plain-text
			// "Query errors:" block otherwise; the shape IS the discriminator.
			var envelope struct {
				Warnings []string `json:"warnings"`
			}
			if jerr := json.Unmarshal([]byte(res.Output), &envelope); jerr != nil {
				return clierr.New("graphql", clierr.GraphQLError, "%s", strings.TrimSpace(res.Output)).
					WithHint("the message lists the valid fields; fix the query rather than retrying it")
			}
			for _, w := range envelope.Warnings {
				fmt.Fprintf(os.Stderr, "warning: %s\n", w)
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
			return writeJSON(out, json.RawMessage(res.Output), compact)
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "Emit {query, output} (output as a string) instead of the envelope")
	c.Flags().BoolVar(&compact, "compact", false, "Print the envelope on one line instead of indenting")
	c.Flags().StringVar(&question, "question", "", "The question this query answers (its literals are exempt from the grounding check)")
	return c
}
