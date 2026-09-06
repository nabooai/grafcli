package cmd

import (
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"

	"github.com/nabooai/grafcli/internal/cfg"
	"github.com/nabooai/grafcli/internal/clierr"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "config",
		Short: "Read and write the CLI's own settings",
		Long: `Settings live in ~/.naboo/config.json (~/.graf/config.json, the pre-0.2 location, is still read). Precedence is flag > environment >
config file > default; "graf config list --explain" shows which one supplied
each value.

This is the CLI's configuration. The DEPLOYMENT's graph configuration — its
data sources and endpoints — is a different thing, and is read with
"graf sources".`,
	}
	c.AddCommand(newConfigListCmd(), newConfigGetCmd(), newConfigSetCmd())
	return c
}

var configKeys = []string{"base_url", "model", "reasoning", "fda_version", "auto_update"}

func newConfigListCmd() *cobra.Command {
	var explain bool
	c := &cobra.Command{
		Use:   "list",
		Short: "Show the effective settings",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := cfg.Resolve(global.baseURL, global.model, global.reasoning, global.fdaVersion)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			rows := [][3]string{
				{"base_url", res.BaseURL, res.BaseURLOrigin},
				{"model", orDefault(res.Model), res.ModelOrigin},
				{"reasoning", orDefault(res.Reasoning), res.ReasoningOrigin},
				{"fda_version", strconv.Itoa(res.FdaVersion), res.FdaVersionOrigin},
			}
			for _, r := range rows {
				if explain {
					fmt.Fprintf(w, "%s\t%s\t(%s)\n", r[0], r[1], r[2])
				} else {
					fmt.Fprintf(w, "%s\t%s\n", r[0], r[1])
				}
			}
			return w.Flush()
		},
	}
	c.Flags().BoolVar(&explain, "explain", false, "Show where each value came from")
	return c
}

func orDefault(s string) string {
	if s == "" {
		return "(server default)"
	}
	return s
}

func newConfigGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:       "get <key>",
		Short:     "Print one setting's effective value",
		Args:      cobra.ExactArgs(1),
		ValidArgs: configKeys,
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := cfg.Resolve(global.baseURL, global.model, global.reasoning, global.fdaVersion)
			if err != nil {
				return err
			}
			var v string
			switch args[0] {
			case "base_url":
				v = res.BaseURL
			case "model":
				v = res.Model
			case "reasoning":
				v = res.Reasoning
			case "fda_version":
				v = strconv.Itoa(res.FdaVersion)
			case "auto_update":
				file, err := cfg.Load()
				if err != nil {
					return err
				}
				v = strconv.FormatBool(file.AutoUpdateEnabled() && os.Getenv(cfg.EnvNoUpdate) == "")
			default:
				return unknownKey(args[0])
			}
			fmt.Fprintln(cmd.OutOrStdout(), v)
			return nil
		},
	}
}

func newConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:       "set <key> <value>",
		Short:     "Write one setting to the config file",
		Args:      cobra.ExactArgs(2),
		ValidArgs: configKeys,
		Example: `  graf config set base_url https://graf.example.com
  graf config set model gemini/gemini-3.6-flash`,
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := args[0], args[1]
			// Read-modify-write: Save preserves keys this CLI does not own, so
			// loading first is what keeps the other settings intact.
			current, err := cfg.Load()
			if err != nil {
				return err
			}
			switch key {
			case "base_url":
				current.BaseURL = value
			case "model":
				current.Model = value
			case "reasoning":
				current.Reasoning = value
			case "fda_version":
				n, err := strconv.Atoi(value)
				if err != nil {
					return clierr.Usagef("fda_version must be a number, got %q", value)
				}
				current.FdaVersion = n
			case "auto_update":
				b, err := strconv.ParseBool(value)
				if err != nil {
					return clierr.Usagef("auto_update must be true or false, got %q", value)
				}
				current.AutoUpdate = &b
			default:
				return unknownKey(key)
			}
			if err := cfg.Save(current); err != nil {
				return err
			}
			p, _ := cfg.Path()
			fmt.Fprintf(cmd.ErrOrStderr(), "wrote %s to %s\n", key, p)
			return nil
		},
	}
}

func unknownKey(key string) error {
	return clierr.Usagef("unknown setting %q", key).
		WithHint("known settings: base_url, model, reasoning, fda_version")
}
