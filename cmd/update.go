package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/nabooai/grafcli/internal/cfg"
	"github.com/nabooai/grafcli/internal/clierr"
	"github.com/nabooai/grafcli/internal/update"
	"github.com/spf13/cobra"
)

func newUpdateCmd() *cobra.Command {
	var (
		check  bool
		quiet  bool
		asJSON bool
	)
	c := &cobra.Command{
		Use:   "update",
		Short: "Update this binary to the latest release",
		Long: `Check the latest GitHub release of nabooai/grafcli and, when it is newer than
this binary, download it, verify its checksum and swap it in place.

This also runs BY ITSELF: every other command spawns "graf update --quiet"
in the background at most once a day (state in ~/.naboo/update.json), so an
installed binary is rarely more than a day stale. Turn that off with
"graf config set auto_update false" or GRAF_NO_UPDATE=1.

The repository is private, so the check needs a GitHub token: GH_TOKEN,
GITHUB_TOKEN, or a logged-in gh CLI.`,
		Example: `  graf update            # check and install
  graf update --check    # only report
  graf config set auto_update false`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), global.timeout)
			defer cancel()
			if Version == "dev" {
				return clierr.New("usage", clierr.Usage, "this is a development build (version dev); it does not self-update").
					WithHint("install a release: uvx --from git+https://github.com/nabooai/grafcli graf")
			}
			res, err := update.Run(ctx, &http.Client{Timeout: 5 * time.Minute}, Version, !check)
			if quiet {
				return nil // outcome is in the state file; the next foreground run reports it
			}
			out := cmd.OutOrStdout()
			if asJSON {
				payload := map[string]any{
					"current": res.Current, "latest": res.Latest, "updated": res.Updated,
				}
				if err != nil {
					payload["error"] = err.Error()
				}
				return json.NewEncoder(out).Encode(payload)
			}
			if err != nil {
				return clierr.New("server", clierr.Server, "update failed: %v", err)
			}
			switch {
			case res.Updated:
				fmt.Fprintf(out, "updated graf %s → %s\n", res.Current, res.Latest)
			case res.Latest != "" && update.Newer(res.Latest, res.Current):
				fmt.Fprintf(out, "graf %s is available (this is %s); run `graf update` to install it\n", res.Latest, res.Current)
			default:
				fmt.Fprintf(out, "graf %s is up to date\n", res.Current)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&check, "check", false, "Only report whether an update exists")
	c.Flags().BoolVar(&quiet, "quiet", false, "Print nothing (the background mode)")
	c.Flags().BoolVar(&asJSON, "json", false, "Emit {current, latest, updated} as JSON")
	return c
}

// backgroundUpdate is the root's pre-run hook: at most once a day, spawn a
// detached `graf update --quiet`, and tell the user (once) when a previous
// background run installed a new version.
func backgroundUpdate(cmd *cobra.Command) {
	if Version == "dev" || os.Getenv(cfg.EnvNoUpdate) != "" {
		return
	}
	switch cmd.Name() {
	case "update", "__complete", "__completeNoDesc":
		return
	}
	st, err := update.LoadState()
	if err != nil {
		return
	}
	// Say so once — and only when the binary running IS the updated one (a
	// person may have copied an older binary over it since).
	if st.UpdatedTo != "" && st.UpdatedTo == Version {
		fmt.Fprintf(os.Stderr, "graf was updated to %s in the background\n", st.UpdatedTo)
		st.UpdatedTo = ""
		_ = update.SaveState(st)
	}
	// Commands that make no request of their own do not start a check either:
	// an agent probing the manifest should not kick off a download.
	switch cmd.Name() {
	case "version", "manifest", "help", "completion", "exit-codes":
		return
	}
	file, err := cfg.Load()
	if err != nil || !file.AutoUpdateEnabled() {
		return
	}
	if !update.Due(st, time.Now()) {
		return
	}
	// Stamp the check BEFORE spawning so concurrent invocations (an agent
	// running graf in a loop) do not each start an updater.
	st.LastCheck = time.Now()
	if err := update.SaveState(st); err != nil {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	child := exec.Command(exe, "update", "--quiet")
	child.Env = append(os.Environ(), cfg.EnvNoUpdate+"=1") // the child must not spawn another
	child.Stdin, child.Stdout, child.Stderr = nil, nil, nil
	detach(child)
	if err := child.Start(); err != nil {
		return
	}
	_ = child.Process.Release()
}
