package cmd

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/nabooai/grafcli/internal/clierr"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// exitCodeTable is the published contract. It is asserted against
// internal/clierr in the tests so the documentation cannot drift from the code.
var exitCodeTable = []map[string]any{
	{"code": clierr.OK, "name": "ok", "retryable": false, "meaning": "success"},
	{"code": clierr.Generic, "name": "generic", "retryable": false, "meaning": "unclassified failure"},
	{"code": clierr.Usage, "name": "usage", "retryable": false, "meaning": "malformed invocation"},
	{"code": clierr.NotFound, "name": "not_found", "retryable": false, "meaning": "no such conversation or resource"},
	{"code": clierr.Unauthenticated, "name": "unauthenticated", "retryable": false, "meaning": "no usable Cloudflare Access credential"},
	{"code": clierr.Forbidden, "name": "forbidden", "retryable": false, "meaning": "credential valid but not authorized"},
	{"code": clierr.InvalidInput, "name": "invalid_input", "retryable": false, "meaning": "the server rejected the request body"},
	{"code": clierr.RateLimited, "name": "rate_limited", "retryable": true, "meaning": "too many requests; honor Retry-After"},
	{"code": clierr.Timeout, "name": "timeout", "retryable": true, "meaning": "the deadline passed; raise --timeout"},
	{"code": clierr.Server, "name": "server", "retryable": true, "meaning": "server or network failure"},
	{"code": clierr.AgentFailed, "name": "agent_failed", "retryable": true, "meaning": "the agent did not finish; rephrasing beats retrying"},
	{"code": clierr.GraphQLError, "name": "graphql", "retryable": false, "meaning": "the graph rejected the query; fix the query"},
}

func newManifestCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "manifest",
		Short: "Print the command tree and exit-code table as JSON",
		Long: `Emit a machine-readable description of this binary: every command, its
flags, and the exit-code contract.

Requires no credential and makes no network request, so an agent can discover
what the CLI can do before it can talk to anything.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			manifest := map[string]any{
				"name":       "graf",
				"version":    Version,
				"commands":   describe(cmd.Root()),
				"exit_codes": exitCodeTable,
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(manifest)
		},
	}
	return c
}

func describe(c *cobra.Command) []map[string]any {
	var out []map[string]any
	for _, sub := range c.Commands() {
		if sub.Hidden || !sub.Runnable() && len(sub.Commands()) == 0 {
			continue
		}
		entry := map[string]any{
			"name":  sub.Name(),
			"use":   sub.UseLine(),
			"short": sub.Short,
		}
		var flags []map[string]string
		sub.LocalFlags().VisitAll(func(f *pflag.Flag) {
			flags = append(flags, map[string]string{
				"name":    f.Name,
				"usage":   f.Usage,
				"default": f.DefValue,
			})
		})
		if len(flags) > 0 {
			entry["flags"] = flags
		}
		if subs := describe(sub); len(subs) > 0 {
			entry["commands"] = subs
		}
		out = append(out, entry)
	}
	return out
}

func newExitCodesTopic() *cobra.Command {
	return &cobra.Command{
		Use:   "exit-codes",
		Short: "Explain what each exit code means and which are worth retrying",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "CODE\tNAME\tRETRY\tMEANING")
			for _, e := range exitCodeTable {
				retry := "no"
				if e["retryable"].(bool) {
					retry = "yes"
				}
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\n",
					e["code"], e["name"], retry, e["meaning"])
			}
			return w.Flush()
		},
	}
}
