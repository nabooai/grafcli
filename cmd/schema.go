package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newSchemaCmd() *cobra.Command {
	var (
		v1     bool
		filter string
	)
	c := &cobra.Command{
		Use:   "schema",
		Short: "Print the graph's GraphQL schema",
		Long: `Print the deployment's GraphQL SDL — every root, type and filterable column
"graf query" can address.

This is the v2 schema, which is the one "graf query" actually serves: the
legacy v1 projection is a DIFFERENT graph whose roots and columns do not match
what you can run. --v1 prints it anyway, for comparison.

The schema is megabytes. --grep prints only the definitions whose text matches,
which is what you want when hunting for one root's columns.`,
		Example: `  graf schema | less
  graf schema --grep CsaSession
  graf schema > schema.graphql`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), global.timeout)
			defer cancel()
			client, err := newClient(ctx)
			if err != nil {
				return err
			}
			sdl, err := client.Schema(ctx, !v1)
			if err != nil {
				return err
			}
			if filter != "" {
				sdl = grepBlocks(sdl, filter)
			}
			fmt.Fprintln(cmd.OutOrStdout(), strings.TrimRight(sdl, "\n"))
			return nil
		},
	}
	c.Flags().BoolVar(&v1, "v1", false, "Print the legacy v1 projection instead (does not match `graf query`)")
	c.Flags().StringVar(&filter, "grep", "", "Print only definitions matching this substring (case-insensitive)")
	return c
}

// grepBlocks returns the SDL definitions whose text contains needle.
//
// Definitions are separated by blank lines in the served SDL, so a paragraph
// split keeps each type whole — a line grep would strip the fields, which are
// the part worth reading.
func grepBlocks(sdl, needle string) string {
	needle = strings.ToLower(needle)
	var keep []string
	for _, block := range strings.Split(sdl, "\n\n") {
		if strings.Contains(strings.ToLower(block), needle) {
			keep = append(keep, block)
		}
	}
	return strings.Join(keep, "\n\n")
}
