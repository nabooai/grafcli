// Package cmd implements the graf command tree.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/nabooai/grafcli/internal/api"
	"github.com/nabooai/grafcli/internal/auth"
	"github.com/nabooai/grafcli/internal/cfg"
	"github.com/nabooai/grafcli/internal/clierr"
	"github.com/spf13/cobra"
)

// Version is overwritten at build time via -ldflags.
var Version = "dev"

type globalFlags struct {
	baseURL    string
	model      string
	reasoning  string
	fdaVersion int
	debug      bool
	timeout    time.Duration
	noInput    bool
	envFile    string
}

var global globalFlags

// isTTY reports whether f is an interactive terminal.
//
// Three separate checks exist across the CLI — output format keys off stdout,
// progress off stderr, prompting off stdin — so they are never conflated.
func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// noInput reports whether the CLI may block for interactive input.
//
// Blocking on a prompt with no human present is the single most common way a
// CLI hangs an agent, so the default is to refuse rather than wait.
func noInput() bool {
	if global.noInput || os.Getenv(cfg.EnvNoInput) != "" {
		return true
	}
	return !isTTY(os.Stdin)
}

func debugWriter() io.Writer {
	if global.debug {
		return os.Stderr
	}
	return nil
}

// loadEnvFiles imports credentials from a .env, without overwriting the real
// environment. Searched from the working directory upward, because a service
// token normally lives at the root of the checkout you are working in.
func loadEnvFiles() {
	if global.envFile != "" {
		if _, err := cfg.LoadDotenv(global.envFile); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not read %s: %v\n", global.envFile, err)
		}
		return
	}
	dir, err := os.Getwd()
	if err != nil {
		return
	}
	for i := 0; i < 6; i++ {
		p := filepath.Join(dir, ".env")
		if n, err := cfg.LoadDotenv(p); err == nil && n > 0 {
			if global.debug {
				fmt.Fprintf(os.Stderr, "read %d credential key(s) from %s\n", n, p)
			}
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return
		}
		dir = parent
	}
}

func httpClient() *http.Client {
	return &http.Client{Timeout: 0} // per-request deadlines come from context
}

// newClient builds an API client for the resolved deployment and credential.
func newClient(_ context.Context) (*api.Client, error) {
	res, err := cfg.Resolve(global.baseURL, global.model, global.reasoning, global.fdaVersion)
	if err != nil {
		return nil, err
	}
	creds, _, err := auth.Resolve()
	if err != nil {
		// A deployment on this machine (a dev serve, an ssh tunnel) has no
		// Access edge and may run with no token; only a MISSING credential is
		// forgiven there, never a malformed one.
		var ce *clierr.Error
		if !(errors.As(err, &ce) && ce.ExitCode == clierr.Unauthenticated && cfg.IsLoopback(res.BaseURL)) {
			return nil, err
		}
		creds = auth.Credentials{}
	}
	return &api.Client{
		HTTP:      httpClient(),
		BaseURL:   res.BaseURL,
		Creds:     creds,
		Debug:     debugWriter(),
		UserAgent: "grafcli/" + Version,
	}, nil
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "graf",
		Short: "Query your organization's data graph from the command line",
		Long: `graf queries a Graf deployment — the company's systems joined into one
GraphQL graph — from the command line.

"graf ask" puts a question to the agent, which finds the source, writes the
query and reports the rows. "graf query" runs GraphQL against the graph
directly, with no model in the loop.

The answering harness is also exposed one stage at a time: "graf steer" shows
what the agent is told about a question before it picks a tool, "graf explore"
runs its explore_schema tool, "graf run-query" runs its run_query tool, and
"graf harness" runs the whole pipeline single-shot with receipts.

Output contract:
  stdout carries only the payload. Progress, warnings and errors go to stderr,
  so piping stdout is always safe.

Exit codes:
  0 ok   2 usage   3 not-found   4 unauthenticated   5 forbidden
  7 invalid-input   8 rate-limited   9 timeout   10 server
  13 agent-failed   14 graphql-error
  Retry only 8, 9, 10 and 13. Run "graf help exit-codes" for the full table.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(cmd *cobra.Command, _ []string) {
			loadEnvFiles()
			backgroundUpdate(cmd)
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&global.baseURL, "url", "", "Graf deployment URL (default "+cfg.DefaultBaseURL+")")
	pf.StringVar(&global.model, "model", "", "Model identifier for agent turns (default: the server's)")
	pf.StringVar(&global.reasoning, "reasoning", "", "Reasoning model or effort for agent turns")
	pf.IntVar(&global.fdaVersion, "fda-version", 0, "FDA agent version to run (default 14)")
	pf.BoolVar(&global.debug, "debug", false, "Trace HTTP requests to stderr (credentials are redacted)")
	pf.DurationVar(&global.timeout, "timeout", 10*time.Minute, "Overall deadline for the command")
	pf.BoolVar(&global.noInput, "no-input", false, "Never prompt; fail instead of waiting for input")
	pf.StringVar(&global.envFile, "env-file", "", "Read credentials from this .env instead of searching upward")

	root.AddCommand(
		newAskCmd(),
		newSteerCmd(),
		newExploreCmd(),
		newRunQueryCmd(),
		newHarnessCmd(),
		newChatsCmd(),
		newQueryCmd(),
		newSchemaCmd(),
		newSourcesCmd(),
		newAuthCmd(),
		newConfigCmd(),
		newDoctorCmd(),
		newReplayCmd(),
		newManifestCmd(),
		newUpdateCmd(),
		newVersionCmd(),
		newExitCodesTopic(),
	)
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	root := newRootCmd()
	err := root.Execute()
	if err == nil {
		return clierr.OK
	}

	var ce *clierr.Error
	if errors.As(err, &ce) {
		ce.Write(os.Stderr)
		return ce.ExitCode
	}
	if errors.Is(err, context.DeadlineExceeded) {
		e := clierr.New("timeout", clierr.Timeout, "the command exceeded its %s deadline", global.timeout).
			WithHint("raise it with --timeout")
		e.Write(os.Stderr)
		return e.ExitCode
	}
	// Cobra reports unknown flags and bad arguments as plain errors.
	e := clierr.Usagef("%v", err)
	e.Write(os.Stderr)
	return e.ExitCode
}

// termWidth picks the activity view width.
func termWidth(flagWidth int) int {
	if flagWidth > 0 {
		return flagWidth
	}
	return 100
}
