package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/mickzijdel/flotilla/internal/daemon"
	"github.com/mickzijdel/flotilla/internal/fleet"
	"github.com/spf13/cobra"
)

func inboxCmd(_ *fleet.Fleet) *cobra.Command {
	var asJSON, watch bool
	var since string
	c := &cobra.Command{
		Use:   "inbox",
		Short: "Show daemon events (agent done, PR opened, submit skipped)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := daemon.DefaultPaths()
			var sinceT time.Time
			if since != "" {
				t, err := time.Parse(time.RFC3339, since)
				if err != nil {
					return fmt.Errorf("invalid --since %q (want RFC3339): %w", since, err)
				}
				sinceT = t
			}
			if watch {
				return watchInbox(cmd.Context(), p.Inbox(), sinceT, cmd.OutOrStdout())
			}
			evs, err := daemon.ReadEvents(p.Inbox(), sinceT)
			if err != nil {
				return err
			}
			return printEvents(cmd.OutOrStdout(), evs, asJSON)
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "output JSONL")
	c.Flags().BoolVar(&watch, "watch", false, "stream new events as they arrive")
	c.Flags().StringVar(&since, "since", "", "only events after this RFC3339 timestamp")
	c.MarkFlagsMutuallyExclusive("json", "watch")
	return c
}

func printEvents(out io.Writer, evs []daemon.InboxEvent, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(out)
		for _, e := range evs {
			if err := enc.Encode(e); err != nil {
				return err
			}
		}
		return nil
	}
	for _, e := range evs {
		if _, err := fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", e.TS.Format(time.RFC3339), e.Agent, e.Type, e.Message); err != nil {
			return err
		}
	}
	return nil
}

// watchInbox prints existing events, then polls for new ones every 200ms.
func watchInbox(ctx context.Context, path string, since time.Time, out io.Writer) error {
	printed := 0
	for {
		evs, err := daemon.ReadEvents(path, since)
		if err != nil {
			return err
		}
		for _, e := range evs[min(printed, len(evs)):] {
			if _, err := fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", e.TS.Format(time.RFC3339), e.Agent, e.Type, e.Message); err != nil {
				return err
			}
		}
		printed = len(evs)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(200 * time.Millisecond):
		}
	}
}
