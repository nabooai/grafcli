package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/nabooai/grafcli/internal/clierr"
	"github.com/spf13/cobra"
)

func newChatsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "chats",
		Aliases: []string{"conversations"},
		Short:   "List, inspect and remove conversations",
		Long: `Conversations are the threads "graf ask" runs in. Continuing one gives the
agent the earlier turns as context.`,
	}
	c.AddCommand(newChatsListCmd(), newChatsShowCmd(), newChatsNewCmd(),
		newChatsRenameCmd(), newChatsRmCmd())
	return c
}

func newChatsListCmd() *cobra.Command {
	var asJSON bool
	var limit int
	c := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List conversations, newest first",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), global.timeout)
			defer cancel()
			client, err := newClient(ctx)
			if err != nil {
				return err
			}
			convs, err := client.ListConversations(ctx)
			if err != nil {
				return err
			}
			if limit > 0 && len(convs) > limit {
				convs = convs[:limit]
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(convs)
			}
			// Piped output is tab separated so cut(1) and awk(1) work; a
			// terminal gets aligned columns instead.
			out := cmd.OutOrStdout()
			if !isTTY(os.Stdout) {
				for _, c := range convs {
					fmt.Fprintf(out, "%s\t%s\t%s\n", c.ID, c.Updated().UTC().Format(time.RFC3339), c.Title)
				}
				return nil
			}
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			for _, c := range convs {
				fmt.Fprintf(w, "%s\t%s\t%s\n", c.ID, relTime(c.Updated()), c.Title)
			}
			return w.Flush()
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "Emit full records as JSON")
	c.Flags().IntVar(&limit, "limit", 0, "Show at most N conversations")
	return c
}

func newChatsShowCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "show <id>",
		Short: "Print a conversation's messages",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), global.timeout)
			defer cancel()
			client, err := newClient(ctx)
			if err != nil {
				return err
			}
			conv, err := client.GetConversation(ctx, args[0])
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(conv)
			}
			out := cmd.OutOrStdout()
			for _, m := range conv.Messages {
				fmt.Fprintf(out, "%s:\n%s\n\n", m.Role, m.Content)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "Emit the full record as JSON")
	return c
}

func newChatsNewCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "new [title]",
		Short: "Create an empty conversation and print its id",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), global.timeout)
			defer cancel()
			client, err := newClient(ctx)
			if err != nil {
				return err
			}
			var title string
			if len(args) == 1 {
				title = args[0]
			}
			conv, err := client.CreateConversation(ctx, title)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), conv.ID)
			return nil
		},
	}
	return c
}

func newChatsRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rename <id> <title>",
		Short: "Retitle a conversation",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), global.timeout)
			defer cancel()
			client, err := newClient(ctx)
			if err != nil {
				return err
			}
			return client.RenameConversation(ctx, args[0], args[1])
		},
	}
}

func newChatsRmCmd() *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:   "rm <id>...",
		Short: "Delete conversations",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), global.timeout)
			defer cancel()
			// Deletion is irreversible and the server keeps no undo, so it
			// needs an explicit yes whenever nobody can be asked.
			if !yes && noInput() {
				return clierr.Usagef("refusing to delete without confirmation").
					WithHint("stdin is not a terminal, so there is nobody to ask").
					WithFix("graf chats rm %s --yes", args[0])
			}
			client, err := newClient(ctx)
			if err != nil {
				return err
			}
			if !yes {
				fmt.Fprintf(os.Stderr, "delete %d conversation(s)? [y/N] ", len(args))
				var answer string
				fmt.Scanln(&answer)
				if answer != "y" && answer != "Y" {
					return clierr.New("canceled", clierr.Generic, "canceled")
				}
			}
			for _, id := range args {
				if err := client.DeleteConversation(ctx, id); err != nil {
					return err
				}
			}
			return nil
		},
	}
	c.Flags().BoolVar(&yes, "yes", false, "Do not ask for confirmation")
	return c
}

// relTime renders an age the way a human reads it.
func relTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Format("2006-01-02")
}
