package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nabooai/grafcli/internal/auth"
	"github.com/nabooai/grafcli/internal/cfg"
	"github.com/nabooai/grafcli/internal/clierr"
	"github.com/spf13/cobra"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check connectivity, credentials and the graph's health",
		Long: `Run the checks that explain most failures, in the order they break:
credential resolution, the Access edge, the API, the schema, and the file
permissions on ~/.graf.

Exits non-zero if any check fails, so it can gate a script.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), global.timeout)
			defer cancel()

			out := cmd.OutOrStdout()
			failed := false
			check := func(name string, fn func() (string, error)) {
				note, err := fn()
				switch {
				case err != nil:
					failed = true
					fmt.Fprintf(out, "✗ %-14s %v\n", name, err)
				case note != "":
					fmt.Fprintf(out, "✓ %-14s %s\n", name, note)
				default:
					fmt.Fprintf(out, "✓ %-14s\n", name)
				}
			}

			res, cfgErr := cfg.Resolve(global.baseURL, global.model, global.reasoning, global.fdaVersion)
			check("config", func() (string, error) {
				if cfgErr != nil {
					return "", cfgErr
				}
				return fmt.Sprintf("%s (%s)", res.BaseURL, res.BaseURLOrigin), nil
			})

			var haveCreds bool
			check("credential", func() (string, error) {
				creds, source, err := auth.Resolve()
				if err != nil {
					return "", err
				}
				haveCreds = true
				if creds.ClientID != "" {
					return fmt.Sprintf("Access service token from %s", source), nil
				}
				return fmt.Sprintf("CF_Authorization cookie from %s", source), nil
			})

			check("permissions", func() (string, error) {
				d, err := cfg.Dir()
				if err != nil {
					return "", err
				}
				fi, err := os.Stat(d)
				if os.IsNotExist(err) {
					return "no state directory yet", nil
				}
				if err != nil {
					return "", err
				}
				if perm := fi.Mode().Perm(); perm != 0o700 {
					// Reported, never fixed: silently chmod-ing a user's home
					// directory is not a diagnostic's job.
					return "", fmt.Errorf("%s is %04o, want 0700", d, perm)
				}
				return d, nil
			})

			if !haveCreds {
				fmt.Fprintln(out, "- skipping the network checks: no credential to present")
				return exitIf(failed)
			}

			client, err := newClient(ctx)
			if err != nil {
				return err
			}

			check("api", func() (string, error) {
				start := time.Now()
				convs, err := client.ListConversations(ctx)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("%d conversations, %dms",
					len(convs), time.Since(start).Milliseconds()), nil
			})

			check("schema", func() (string, error) {
				sdl, err := client.Schema(ctx, true)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("%d types, %.0f KB",
					strings.Count(sdl, "\ntype "), float64(len(sdl))/1024), nil
			})

			check("graph", func() (string, error) {
				// The cheapest query that proves the serve path works end to
				// end, not merely that the schema can be read.
				r, err := client.Query(ctx, "{ __typename }")
				if err != nil {
					return "", err
				}
				if msgs := r.ErrorMessages(); len(msgs) > 0 {
					return "", fmt.Errorf("%s", msgs[0])
				}
				return "queryable", nil
			})

			return exitIf(failed)
		},
	}
}

func exitIf(failed bool) error {
	if failed {
		return clierr.New("unhealthy", clierr.Generic, "one or more checks failed")
	}
	return nil
}

var _ io.Writer = os.Stderr
