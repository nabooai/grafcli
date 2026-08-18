package cmd

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/nabooai/grafcli/internal/api"
	"github.com/nabooai/grafcli/internal/clierr"
	"github.com/nabooai/grafcli/internal/ui"
	"github.com/spf13/cobra"
)

func newReplayCmd() *cobra.Command {
	var (
		steps    bool
		showIn   bool
		plain    bool
		width    int
		realtime bool
	)
	c := &cobra.Command{
		Use:   "replay <file>",
		Short: "Re-render a recorded event stream offline",
		Long: `Replay a stream captured with "graf ask --record".

The recording is fed through the same parser and renderer the live path uses,
so what you see is what the run showed — which is the point: a rendering bug
can be reproduced without spending another turn.`,
		Example: `  graf ask "what changed?" --events --record run.sse
  graf replay run.sse
  graf replay run.sse --realtime`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := os.Open(args[0])
			if err != nil {
				return clierr.New("not_found", clierr.NotFound, "could not open %s: %v", args[0], err)
			}
			defer f.Close()

			renderer := ui.New(os.Stderr, !plain && ui.ColorEnabled(os.Stderr), termWidth(width))
			renderer.ShowSteps(steps)
			renderer.ShowInput(showIn)
			renderer.SetContext(ui.Context{Endpoint: "recording: " + args[0]})
			renderer.Start()

			stdout := cmd.OutOrStdout()
			var last time.Time
			res, parseErr := api.Parse(f, func(e api.Event) {
				if realtime {
					// Recordings carry no timestamps, so pacing is a fixed
					// beat per event: enough to watch, not a reconstruction.
					if !last.IsZero() {
						time.Sleep(120 * time.Millisecond)
					}
					last = time.Now()
				}
				renderer.Handle(e)
				if e.Type == "message_update" && e.DeltaKind == "text_delta" {
					fmt.Fprint(stdout, e.Delta)
				}
			})
			if res != nil && res.Answer != "" {
				fmt.Fprintln(stdout)
			}
			if parseErr != nil {
				return parseErr
			}
			renderer.Finish(res)
			// Failed returns a *clierr.Error, so returning it directly would
			// hand back a non-nil error interface wrapping a nil pointer.
			if e := res.Failed(); e != nil {
				return e
			}
			return nil
		},
	}
	fl := c.Flags()
	fl.BoolVar(&steps, "steps", false, "Show step kinds hidden by default")
	fl.BoolVar(&showIn, "show-input", false, "Show the full model input")
	fl.BoolVar(&plain, "plain", false, "Disable color")
	fl.IntVar(&width, "width", 0, "Render at a fixed width")
	fl.BoolVar(&realtime, "realtime", false, "Pace the replay instead of rendering instantly")
	return c
}

var _ = io.Discard
