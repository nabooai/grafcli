package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/nabooai/grafcli/internal/clierr"
	"github.com/spf13/cobra"
)

func newQueryCmd() *cobra.Command {
	var (
		raw     bool
		compact bool
	)
	c := &cobra.Command{
		Use:   "query <graphql>",
		Short: "Run GraphQL against the graph, with no model in the loop",
		Long: `Run a GraphQL query directly against the graph and print the rows.

This is the deterministic half of the CLI: same query, same rows, no tokens
spent and nothing to second-guess. "graf ask --json" reports the queries the
agent wrote under .queries, so the usual workflow is to ask once and then run
the query it found on a schedule.

Pass "-" to read the query from stdin. Prints the "data" object by default;
--raw keeps the server's full envelope including warnings and generation info.

Warnings are printed to stderr. Read them: a capped or truncated result means
the rows are a SUBSET, and presenting that as the whole answer is wrong.`,
		Example: `  graf query '{ csa { sessionCount } }'
  graf query - < query.graphql
  graf query '{ csa { session(first: 5) { sessionId project } } }' | jq '.csa.session'
  graf query '{ csa { sessionCount } }' --raw`,
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
			res, err := client.Query(ctx, query)
			if err != nil {
				return err
			}

			for _, w := range res.Warnings() {
				fmt.Fprintf(os.Stderr, "warning: %s\n", w)
			}

			// A GraphQL error is not a transport failure: the request
			// succeeded and the graph answered "your query is wrong". It gets
			// its own exit code so a caller can tell the two apart.
			if msgs := res.ErrorMessages(); len(msgs) > 0 {
				return clierr.New("graphql", clierr.GraphQLError, "%s", strings.Join(msgs, "; ")).
					WithHint("check field and argument names against `graf schema`")
			}

			out := cmd.OutOrStdout()
			payload := res.Data
			if raw {
				b, err := json.Marshal(res)
				if err != nil {
					return err
				}
				payload = b
			}
			if len(payload) == 0 {
				payload = json.RawMessage("null")
			}
			return writeJSON(out, payload, compact)
		},
	}
	c.Flags().BoolVar(&raw, "raw", false, "Print the server's full envelope, not just data")
	c.Flags().BoolVar(&compact, "compact", false, "Print one line of JSON instead of indenting")
	return c
}

func readQuery(args []string) (string, error) {
	if len(args) == 1 && args[0] != "-" {
		q := strings.TrimSpace(args[0])
		if q == "" {
			return "", clierr.Usagef("the query is empty")
		}
		return q, nil
	}
	if len(args) == 0 && noInput() {
		return "", clierr.Usagef("no query given").
			WithHint("stdin is not a terminal, so there is nothing to prompt").
			WithFix(`graf query '{ csa { sessionCount } }'`)
	}
	if len(args) == 0 {
		return "", clierr.Usagef("no query given").
			WithFix(`graf query '{ csa { sessionCount } }'`)
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

// writeJSON prints JSON, indented by default because a human reads it and jq
// does not care either way.
func writeJSON(w io.Writer, raw json.RawMessage, compact bool) error {
	if compact {
		fmt.Fprintln(w, string(raw))
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		fmt.Fprintln(w, string(raw))
		return nil
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
