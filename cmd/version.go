package cmd

import (
	"encoding/json"
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

func newVersionCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := map[string]string{
				"version": Version,
				"go":      runtime.Version(),
				"os":      runtime.GOOS,
				"arch":    runtime.GOARCH,
			}
			// A `go install`ed binary carries no -ldflags stamp, so fall back
			// to the module version the toolchain recorded.
			if Version == "dev" {
				if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
					info["version"] = bi.Main.Version
				}
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return json.NewEncoder(out).Encode(info)
			}
			fmt.Fprintf(out, "graf %s (%s %s/%s)\n",
				info["version"], info["go"], info["os"], info["arch"])
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "Emit the version record as JSON")
	return c
}
