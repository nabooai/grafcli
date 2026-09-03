package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/nabooai/grafcli/internal/auth"
	"github.com/nabooai/grafcli/internal/cfg"
	"github.com/nabooai/grafcli/internal/clierr"
	"github.com/spf13/cobra"
)

func newAuthCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "auth",
		Short: "Manage the Cloudflare Access credential",
		Long: `Graf sits behind Cloudflare Access, so authenticating means holding an
Access service token: a client id and secret sent as CF-Access-Client-Id and
CF-Access-Client-Secret. There is no separate application login.

Credentials are resolved in this order:

  1. GRAF_CF_ACCESS_CLIENT_ID + GRAF_CF_ACCESS_CLIENT_SECRET
  2. CLOUDFLARE_ACCESS_CLIENT_ID + CLOUDFLARE_ACCESS_CLIENT_SECRET
  3. GRAF_CF_AUTHORIZATION (a browser-issued CF_Authorization JWT)
  4. ~/.graf/.credentials.json, written by "graf auth login"

The deployment's own bearer token (its GRAF_API_TOKEN, checked by the
/api/cli endpoints steer/explore/run-query/harness use) resolves separately:
GRAF_API_TOKEN, then the same credentials file. A loopback deployment
(http://127.0.0.1:…) needs no credential at all.

A .env in the working directory (or any parent, up to six levels) is read
first and contributes those same variables WITHOUT overwriting anything
already exported — so a real environment variable always wins.

Credentials are never accepted as command-line arguments: argv is readable by
every other process on the machine.`,
	}
	c.AddCommand(newAuthLoginCmd(), newAuthStatusCmd(), newAuthLogoutCmd())
	return c
}

func newAuthLoginCmd() *cobra.Command {
	var withToken bool
	c := &cobra.Command{
		Use:   "login",
		Short: "Store a Cloudflare Access service token",
		Long: `Read a service token from stdin and store it at ~/.graf/.credentials.json,
mode 0600.

Supply it as two lines (id then secret), as "id:secret", or as KEY=VALUE lines
copied straight out of a .env — all three are accepted because all three are
what people actually have in hand. A GRAF_API_TOKEN=... line (or a lone bare
token) stores the deployment's own bearer token; it is merged with whatever is
already stored, so the two halves can be added in separate runs.`,
		Example: `  graf auth login --with-token < token.txt
  printf '%s\n%s\n' "$ID" "$SECRET" | graf auth login --with-token
  graf auth login --with-token <<< "$ID:$SECRET"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !withToken {
				// There is no browser flow to fall back to: Access issues
				// service tokens from its own dashboard, not to a CLI.
				return clierr.Usagef("only token-based login is supported").
					WithHint("Cloudflare Access issues service tokens from the Zero Trust dashboard; there is no CLI flow to mint one").
					WithFix("graf auth login --with-token < token.txt")
			}
			creds, err := parseTokenInput(os.Stdin)
			if err != nil {
				return err
			}
			// Overlay, never replace: storing the API token must not discard
			// a service token stored earlier (or the reverse).
			existing, _, err := auth.Load()
			if err != nil {
				return err
			}
			if err := auth.Save(existing.Merge(creds)); err != nil {
				return err
			}
			p, _ := auth.Path()
			fmt.Fprintf(os.Stderr, "stored credential in %s\n", p)
			return nil
		},
	}
	c.Flags().BoolVar(&withToken, "with-token", false, "Read the token from stdin")
	return c
}

// parseTokenInput accepts the three shapes a service token arrives in.
func parseTokenInput(r *os.File) (auth.Credentials, error) {
	var creds auth.Credentials
	sc := bufio.NewScanner(r)
	var bare []string
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		if k, v, ok := strings.Cut(line, "="); ok {
			v = strings.Trim(strings.TrimSpace(v), `"'`)
			switch strings.TrimSpace(k) {
			case cfg.EnvClientID, cfg.EnvAltClientID:
				creds.ClientID = v
			case cfg.EnvClientSecret, cfg.EnvAltClientSecret:
				creds.ClientSecret = v
			case cfg.EnvCookie, "CF_Authorization":
				creds.Cookie = v
			case cfg.EnvAPIToken:
				creds.APIToken = v
			}
			continue
		}
		if id, secret, ok := strings.Cut(line, ":"); ok && !strings.Contains(line, ".") {
			creds.ClientID, creds.ClientSecret = strings.TrimSpace(id), strings.TrimSpace(secret)
			continue
		}
		bare = append(bare, line)
	}
	if err := sc.Err(); err != nil {
		return creds, err
	}
	if creds.Empty() {
		switch len(bare) {
		case 2:
			creds.ClientID, creds.ClientSecret = bare[0], bare[1]
		case 1:
			// A lone value is a session JWT (three dot-separated segments) or
			// the deployment's API token; a service token is always a pair.
			if strings.Count(bare[0], ".") == 2 {
				creds.Cookie = bare[0]
			} else {
				creds.APIToken = bare[0]
			}
		}
	}
	if creds.Empty() {
		return creds, clierr.New("invalid_input", clierr.InvalidInput,
			"could not read a credential from stdin").
			WithHint("expected two lines (id, secret), \"id:secret\", or KEY=VALUE lines")
	}
	return creds, nil
}

func newAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show which credential is in effect and whether it works",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), global.timeout)
			defer cancel()

			res, err := cfg.Resolve(global.baseURL, global.model, global.reasoning, global.fdaVersion)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "deployment  %s (%s)\n", res.BaseURL, res.BaseURLOrigin)

			creds, source, err := auth.Resolve()
			if err != nil {
				fmt.Fprintf(out, "credential  none\n")
				return err
			}
			switch {
			case creds.ClientID != "":
				fmt.Fprintf(out, "credential  Access service token, id %s (%s)\n",
					auth.Redact(creds.ClientID), source)
			case creds.Cookie != "":
				fmt.Fprintf(out, "credential  CF_Authorization cookie (%s)\n", source)
			default:
				fmt.Fprintf(out, "credential  no Cloudflare Access credential\n")
			}
			if creds.APIToken != "" {
				fmt.Fprintf(out, "api token   %s\n", auth.Redact(creds.APIToken))
			} else {
				fmt.Fprintf(out, "api token   none\n")
			}

			// A credential that parses is not a credential that works: the
			// only honest check is a request through the Access edge.
			client, err := newClient(ctx)
			if err != nil {
				return err
			}
			if _, err := client.ListConversations(ctx); err != nil {
				fmt.Fprintf(out, "status      rejected\n")
				return err
			}
			fmt.Fprintf(out, "status      ok\n")
			return nil
		},
	}
}

func newAuthLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Delete the stored credential",
		Long: `Delete ~/.graf/.credentials.json.

This is local only. Cloudflare Access service tokens are revoked from the Zero
Trust dashboard; deleting the file does not invalidate the token.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := auth.Delete(); err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "deleted the stored credential (revoke the token in Cloudflare Zero Trust to disable it)")
			return nil
		},
	}
}
