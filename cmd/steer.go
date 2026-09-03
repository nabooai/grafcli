package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newSteerCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "steer <question>",
		Short: "Show what the agent is told about a question before it picks a tool",
		Long: `Print the steers for a question: the facts the store proves about it, which
the answering harness injects ahead of the question — each URL resolved to
the entity it names, each loose token matched to the stored values it could
name. Nothing is run; no tokens are spent.

An answer that looks wrong is usually explained here, so this is the first
thing to check. Pass "-" to read the question from stdin.

stdout carries one block per steer ("## <kind>" then its text). --json emits
{question, steers, input}, where input is the exact turn input the model
would receive.`,
		Example: `  graf steer "what is new with saki?"
  graf steer "https://github.com/acme/api/pull/42" --json | jq .steers`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), global.timeout)
			defer cancel()

			question, err := readQuestion(args)
			if err != nil {
				return err
			}
			client, err := newClient(ctx)
			if err != nil {
				return err
			}
			res, err := client.Steer(ctx, question)
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
			for i, s := range res.Steers {
				if i > 0 {
					fmt.Fprintln(out)
				}
				fmt.Fprintf(out, "## %s\n%s\n", s.Kind, s.Text)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "Emit {question, steers, input} instead of text")
	return c
}
