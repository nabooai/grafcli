package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func newSourcesCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "sources",
		Short: "List the data sources wired into the graph",
		Long: `List the deployment's configured nodes — the connectors, tables and views
that make up the graph — with the endpoints each exposes.

Credential values are never returned by the API: the config holds "$VAR"
references, and the values stay on the server. "graf sources --secrets" lists
the NAMES the vault holds, which is what you need to see what is missing.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), global.timeout)
			defer cancel()
			client, err := newClient(ctx)
			if err != nil {
				return err
			}
			raw, err := client.Config(ctx)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), raw, false)
			}

			var doc struct {
				Nodes map[string]struct {
					Type      string `json:"type"`
					Connector string `json:"connector"`
					Metadata  struct {
						Name        string `json:"name"`
						Description string `json:"description"`
					} `json:"metadata"`
					Endpoints map[string]json.RawMessage `json:"endpoints"`
				} `json:"nodes"`
			}
			if err := json.Unmarshal(raw, &doc); err != nil {
				// The config shape is the server's to change; fall back to
				// printing it rather than failing on an unexpected field.
				return writeJSON(cmd.OutOrStdout(), raw, false)
			}

			names := make([]string, 0, len(doc.Nodes))
			for k := range doc.Nodes {
				names = append(names, k)
			}
			sort.Strings(names)

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			for _, n := range names {
				node := doc.Nodes[n]
				kind := node.Type
				if node.Connector != "" {
					kind = node.Connector
				}
				fmt.Fprintf(w, "%s\t%s\t%d endpoints\t%s\n",
					n, kind, len(node.Endpoints), firstSentence(node.Metadata.Description))
			}
			return w.Flush()
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "Emit the full graph configuration as JSON")
	c.AddCommand(newSecretsCmd())
	return c
}

// firstSentence keeps the table a table. Descriptions run to full paragraphs;
// --json is there for anyone who wants all of it.
func firstSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.Index(s, ". "); i > 0 && i < 140 {
		return s[:i+1]
	}
	if len(s) > 140 {
		return s[:139] + "…"
	}
	return s
}

func newSecretsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "secrets",
		Short: "List the names of the secrets the deployment holds",
		Long: `List secret NAMES. Values never leave the server — this exists so you can
see which credential a source is waiting on, not to read it back.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), global.timeout)
			defer cancel()
			client, err := newClient(ctx)
			if err != nil {
				return err
			}
			names, err := client.SecretNames(ctx)
			if err != nil {
				return err
			}
			for _, n := range names {
				fmt.Fprintln(cmd.OutOrStdout(), n)
			}
			if len(names) == 0 && isTTY(os.Stderr) {
				fmt.Fprintln(os.Stderr, "no secrets configured")
			}
			return nil
		},
	}
}
